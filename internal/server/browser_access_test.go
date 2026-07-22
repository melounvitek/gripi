package server_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/melounvitek/gripi/internal/access"
)

func TestUnknownBrowserGetsAnAccessCookieAndGateWithoutCreatingState(t *testing.T) {
	cfg := testConfig(t)
	cfg.AdminPassword = "secret"
	cfg.BrowserAuthDisabled = false
	handler := newHandler(t, cfg)
	request := httptest.NewRequest(http.MethodGet, "http://example.com/?session=one", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d", response.Code)
	}
	for _, text := range []string{"Browser access required", "Ask access", `action="/browser-access/request"`, `type="submit"`} {
		if !strings.Contains(response.Body.String(), text) {
			t.Fatalf("body does not contain %q", text)
		}
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies = %#v", cookies)
	}
	cookie := cookies[0]
	if cookie.Name != "gripi_browser" || len(cookie.Value) != 64 || cookie.Path != "/" || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode || cookie.MaxAge != 365*24*60*60 {
		t.Fatalf("cookie = %#v", cookie)
	}
	if cookie.Secure {
		t.Fatal("HTTP cookie is secure")
	}
	if _, err := os.Stat(cfg.BrowserAccessPath); !os.IsNotExist(err) {
		t.Fatalf("browser state exists or stat failed: %v", err)
	}
}

func TestFullBrowserAccessQueueRendersAFriendlyRequestError(t *testing.T) {
	cfg := testConfig(t)
	cfg.AdminPassword = "secret"
	cfg.BrowserAuthDisabled = false
	store := access.NewBrowserStore(cfg.BrowserAccessPath)
	for index := 0; index < access.MaxPendingRequests; index++ {
		if _, err := store.RequestAccess(strings.Repeat("x", index+1), "", ""); err != nil {
			t.Fatal(err)
		}
	}
	handler := newHandler(t, cfg)
	response := performForm(handler, "/browser-access/request", url.Values{"return_to": {"/?session=original"}}, "gripi_browser=new-token")

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", response.Code)
	}
	if !strings.Contains(response.Body.String(), "Too many pending browser access requests") {
		t.Fatalf("body = %q", response.Body.String())
	}
	if strings.Count(response.Body.String(), `value="/?session=original"`) != 2 {
		t.Fatal("safe return target was not preserved in both forms")
	}
}

func TestBrowserEnforcementIsDisabledForAuthDisabledAndMultiUserModes(t *testing.T) {
	tests := []struct {
		name      string
		disabled  bool
		multiUser bool
	}{
		{name: "browser authentication disabled", disabled: true},
		{name: "multi-user workspace mode", multiUser: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := testConfig(t)
			cfg.AdminPassword = "secret"
			cfg.BrowserAuthDisabled = test.disabled
			cfg.MultiUserMode = test.multiUser
			handler := newHandler(t, cfg)
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://example.com/", nil))

			expected := http.StatusNotFound
			if test.multiUser {
				expected = http.StatusServiceUnavailable
			}
			if response.Code != expected {
				t.Fatalf("status = %d, body = %q", response.Code, response.Body.String())
			}
			if strings.Contains(response.Body.String(), "Browser access required") {
				t.Fatal("browser access gate was rendered")
			}
			if len(response.Header().Values("Set-Cookie")) != 0 {
				t.Fatalf("Set-Cookie = %#v", response.Header().Values("Set-Cookie"))
			}
		})
	}
}

func TestDisabledBrowserAccessEndpointStatusesMatchTheGatewayContract(t *testing.T) {
	handler := newHandler(t, testConfig(t))
	for _, endpoint := range []string{"/browser-access/request", "/browser-access/admin-login"} {
		response := performForm(handler, endpoint, nil, "")
		if response.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d", endpoint, response.Code)
		}
	}
	status := performRequest(handler, http.MethodGet, "http://example.com/browser-access/status", "")
	if status.Code != http.StatusNotFound {
		t.Fatalf("status endpoint = %d", status.Code)
	}
	pending := performRequest(handler, http.MethodGet, "http://example.com/browser-access/pending", "")
	if pending.Code != http.StatusForbidden {
		t.Fatalf("pending endpoint = %d", pending.Code)
	}
	if len(pending.Header().Values("Set-Cookie")) != 0 {
		t.Fatalf("pending endpoint cookies = %#v", pending.Header().Values("Set-Cookie"))
	}
	for _, endpoint := range []string{"/browser-access/approve", "/browser-access/deny"} {
		response := performForm(handler, endpoint, nil, "")
		if response.Code != http.StatusForbidden {
			t.Fatalf("%s status = %d", endpoint, response.Code)
		}
	}
}

func TestBrowserRequestStatusPendingApprovalAndDenialFlow(t *testing.T) {
	cfg := testConfig(t)
	cfg.AdminPassword = "secret"
	cfg.BrowserAuthDisabled = false
	handler := newHandler(t, cfg)

	blocked := performRequest(handler, http.MethodGet, "http://example.com/", "")
	browserCookie := responseCookie(t, blocked)
	requested := performForm(handler, "/browser-access/request", nil, browserCookie)
	if requested.Code != http.StatusSeeOther || requested.Header().Get("Location") != "http://example.com/" {
		t.Fatalf("request response = %d, Location = %q", requested.Code, requested.Header().Get("Location"))
	}

	status := performRequest(handler, http.MethodGet, "http://example.com/browser-access/status", browserCookie)
	assertJSON(t, status, map[string]any{"status": "pending"})

	store := access.NewBrowserStore(cfg.BrowserAccessPath)
	pending, err := store.PendingRequests()
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending requests = %#v, %v", pending, err)
	}
	if !pending[0].Requested || pending[0].IP != "192.0.2.1" {
		t.Fatalf("pending request = %#v", pending[0])
	}
	if _, err := store.ApproveCurrentBrowser("approver", "Admin browser"); err != nil {
		t.Fatal(err)
	}
	approverCookie := "gripi_browser=approver"
	pendingResponse := performRequest(handler, http.MethodGet, "http://example.com/browser-access/pending", approverCookie)
	if pendingResponse.Code != http.StatusOK || !strings.Contains(pendingResponse.Body.String(), pending[0].Code) {
		t.Fatalf("pending response = %d, %q", pendingResponse.Code, pendingResponse.Body.String())
	}

	denied := performForm(handler, "/browser-access/deny", url.Values{"code": {pending[0].Code}}, approverCookie)
	assertJSON(t, denied, map[string]any{"ok": true})
	status = performRequest(handler, http.MethodGet, "http://example.com/browser-access/status", browserCookie)
	assertJSON(t, status, map[string]any{"status": "denied"})

	rerequested := performForm(handler, "/browser-access/request", nil, browserCookie)
	if rerequested.Code != http.StatusSeeOther {
		t.Fatalf("rerequest status = %d", rerequested.Code)
	}
	pending, err = store.PendingRequests()
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending requests = %#v, %v", pending, err)
	}
	approved := performForm(handler, "/browser-access/approve", url.Values{"code": {pending[0].Code}}, approverCookie)
	assertJSON(t, approved, map[string]any{"ok": true})
	status = performRequest(handler, http.MethodGet, "http://example.com/browser-access/status", browserCookie)
	assertJSON(t, status, map[string]any{"status": "approved"})
	allowed := performRequest(handler, http.MethodGet, "http://example.com/", browserCookie)
	if allowed.Code != http.StatusNotFound || strings.Contains(allowed.Body.String(), "Browser access required") {
		t.Fatalf("allowed response = %d, %q", allowed.Code, allowed.Body.String())
	}
}

func TestUnknownLengthBrowserAccessFormIsPreservedForParsing(t *testing.T) {
	cfg := testConfig(t)
	cfg.AdminPassword = "secret"
	cfg.BrowserAuthDisabled = false
	handler := newHandler(t, cfg)
	request := httptest.NewRequest(http.MethodPost, "http://example.com/browser-access/request", nil)
	request.Body = io.NopCloser(strings.NewReader("return_to=%2Fnotification-test"))
	request.ContentLength = -1
	request.TransferEncoding = []string{"chunked"}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Cookie", "gripi_browser=browser")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "http://example.com/notification-test" {
		t.Fatalf("response = %d, Location = %q, body = %q", response.Code, response.Header().Get("Location"), response.Body.String())
	}
	pending, err := access.NewBrowserStore(cfg.BrowserAccessPath).PendingRequests()
	if err != nil || len(pending) != 1 || pending[0].Token != "browser" {
		t.Fatalf("pending requests = %#v, %v", pending, err)
	}
}

func TestRedirectUsesCanonicalAuthorizedAuthorities(t *testing.T) {
	tests := []struct {
		name          string
		permitted     string
		directHost    string
		forwardedHost string
		forwardedPort string
		wantLocation  string
	}{
		{name: "direct hostname and port", permitted: "gateway.example", directHost: "Gateway.Example:00443", wantLocation: "http://gateway.example:443/notification-test"},
		{name: "direct IPv4 and port", permitted: "192.0.2.10", directHost: "192.0.2.10:8080", wantLocation: "http://192.0.2.10:8080/notification-test"},
		{name: "direct bracketed IPv6 and port", permitted: "[::1]", directHost: "[0:0:0:0:0:0:0:1]:00443", wantLocation: "http://[::1]:443/notification-test"},
		{name: "trusted last forwarded hostname and port", permitted: "gateway.example", directHost: "gateway.example", forwardedHost: "attacker.example, Gateway.Example:00443", wantLocation: "https://gateway.example:443/notification-test"},
		{name: "trusted separate forwarded port", permitted: "gateway.example", directHost: "gateway.example", forwardedHost: "gateway.example", forwardedPort: "08443", wantLocation: "https://gateway.example:8443/notification-test"},
		{name: "trusted last forwarded host and port", permitted: "gateway.example", directHost: "gateway.example", forwardedHost: "attacker.example, gateway.example", forwardedPort: "8443, 00443", wantLocation: "https://gateway.example:443/notification-test"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := testConfig(t)
			cfg.AdminPassword = "secret"
			cfg.BrowserAuthDisabled = false
			cfg.Production = true
			cfg.TrustProxyHeaders = true
			cfg.PermittedHosts = []string{test.permitted}
			handler := newHandler(t, cfg)
			request := httptest.NewRequest(http.MethodPost, "http://gateway.example/browser-access/request", strings.NewReader("return_to=%2Fnotification-test"))
			request.Host = test.directHost
			request.RemoteAddr = "127.0.0.1:1234"
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			request.Header.Set("Cookie", "gripi_browser=browser")
			if test.forwardedHost != "" {
				request.Header.Set("X-Forwarded-Host", test.forwardedHost)
				request.Header.Set("X-Forwarded-Proto", "https")
				request.Header.Set("X-Forwarded-Port", test.forwardedPort)
			}
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			if response.Code != http.StatusSeeOther || response.Header().Get("Location") != test.wantLocation {
				t.Fatalf("response = %d, Location = %q", response.Code, response.Header().Get("Location"))
			}
		})
	}
}

func TestRFCForwardedDoesNotAffectRedirectSchemeOrAuthority(t *testing.T) {
	cfg := testConfig(t)
	cfg.AdminPassword = "secret"
	cfg.BrowserAuthDisabled = false
	cfg.Production = true
	cfg.TrustProxyHeaders = true
	cfg.PermittedHosts = []string{"gateway.example", "public.example"}
	handler := newHandler(t, cfg)
	request := httptest.NewRequest(http.MethodPost, "http://gateway.example/browser-access/request", strings.NewReader("return_to=%2Fnotification-test"))
	request.RemoteAddr = "127.0.0.1:1234"
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Cookie", "gripi_browser=browser")
	request.Header.Set("Forwarded", `host="public.example";proto=https`)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "http://gateway.example/notification-test" {
		t.Fatalf("response = %d, Location = %q", response.Code, response.Header().Get("Location"))
	}
}

func TestTrustedProxyRedirectUsesTheAuthorizedLastForwardedHost(t *testing.T) {
	cfg := testConfig(t)
	cfg.AdminPassword = "secret"
	cfg.BrowserAuthDisabled = false
	cfg.Production = true
	cfg.TrustProxyHeaders = true
	cfg.PermittedHosts = []string{"gateway.example"}
	handler := newHandler(t, cfg)
	request := httptest.NewRequest(http.MethodPost, "http://gateway.example/browser-access/request", strings.NewReader("return_to=%2Fnotification-test"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Cookie", "gripi_browser=browser")
	request.Header.Set("X-Forwarded-Host", "attacker.example, gateway.example")
	request.Header.Set("X-Forwarded-Proto", "https")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "https://gateway.example/notification-test" {
		t.Fatalf("response = %d, Location = %q", response.Code, response.Header().Get("Location"))
	}
	if got := response.Header().Get("Content-Type"); got != "text/html;charset=utf-8" {
		t.Fatalf("Content-Type = %q", got)
	}
}

func TestTrustedProxyRedirectRejectsAnInvalidForwardedScheme(t *testing.T) {
	cfg := testConfig(t)
	cfg.AdminPassword = "secret"
	cfg.BrowserAuthDisabled = false
	cfg.TrustProxyHeaders = true
	cfg.PermittedHosts = []string{"gateway.example"}
	handler := newHandler(t, cfg)
	request := httptest.NewRequest(http.MethodPost, "http://gateway.example/browser-access/request", strings.NewReader("return_to=%2Fnotification-test"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Cookie", "gripi_browser=browser")
	request.Header.Set("X-Forwarded-Host", "gateway.example")
	request.Header.Set("X-Forwarded-Proto", "https://evil.example")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "http://gateway.example/notification-test" {
		t.Fatalf("response = %d, Location = %q", response.Code, response.Header().Get("Location"))
	}
}

func TestBrowserRoutesReturnNotFoundForWrongMethodsAndPreserveHead(t *testing.T) {
	handler := newHandler(t, testConfig(t))
	for path, method := range map[string]string{
		"/browser-access/request":     http.MethodGet,
		"/browser-access/admin-login": http.MethodGet,
		"/browser-access/status":      http.MethodPost,
		"/browser-access/pending":     http.MethodPost,
		"/browser-access/approve":     http.MethodGet,
		"/browser-access/deny":        http.MethodGet,
	} {
		response := performRequest(handler, method, "http://example.com"+path, "")
		if response.Code != http.StatusNotFound {
			t.Fatalf("%s %s status = %d", method, path, response.Code)
		}
	}

	cfg := testConfig(t)
	cfg.AdminPassword = "secret"
	cfg.BrowserAuthDisabled = false
	if _, err := access.NewBrowserStore(cfg.BrowserAccessPath).ApproveCurrentBrowser("approved", "test"); err != nil {
		t.Fatal(err)
	}
	handler = newHandler(t, cfg)
	for _, path := range []string{"/browser-access/status", "/browser-access/pending"} {
		response := performRequest(handler, http.MethodHead, "http://example.com"+path, "gripi_browser=approved")
		if response.Code != http.StatusOK {
			t.Fatalf("HEAD %s status = %d", path, response.Code)
		}
	}
}

func TestBrowserRequestStoresTrustedForwardedClientIP(t *testing.T) {
	cfg := testConfig(t)
	cfg.AdminPassword = "secret"
	cfg.BrowserAuthDisabled = false
	cfg.TrustProxyHeaders = true
	handler := newHandler(t, cfg)
	request := httptest.NewRequest(http.MethodPost, "https://example.com/browser-access/request", strings.NewReader("return_to=%2F"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Cookie", "gripi_browser=browser")
	request.Header.Set("X-Forwarded-For", "198.51.100.12, 127.0.0.1")
	request.Header.Set("X-Forwarded-Proto", "https")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, body = %q", response.Code, response.Body.String())
	}
	pending, err := access.NewBrowserStore(cfg.BrowserAccessPath).PendingRequests()
	if err != nil || len(pending) != 1 || pending[0].IP != "198.51.100.12" {
		t.Fatalf("pending requests = %#v, %v", pending, err)
	}
}

func TestAdminLoginUsesSafeReturnTargetsAndRotatesTheBrowserToken(t *testing.T) {
	cfg := testConfig(t)
	cfg.AdminPassword = "secret"
	cfg.BrowserAuthDisabled = false
	handler := newHandler(t, cfg)
	store := access.NewBrowserStore(cfg.BrowserAccessPath)
	if _, err := store.RequestAccess("chosen-token", "", "Browser/1"); err != nil {
		t.Fatal(err)
	}

	wrong := performForm(handler, "/browser-access/admin-login", url.Values{
		"password":  {"wrong"},
		"return_to": {"/?session=one"},
	}, "gripi_browser=chosen-token")
	if wrong.Code != http.StatusForbidden || !strings.Contains(wrong.Body.String(), "Admin password did not match.") || len(wrong.Header().Values("Set-Cookie")) != 0 {
		t.Fatalf("wrong login = %d, %q, cookies %#v", wrong.Code, wrong.Body.String(), wrong.Header().Values("Set-Cookie"))
	}

	login := performForm(handler, "/browser-access/admin-login", url.Values{
		"password":  {"secret"},
		"return_to": {"https://evil.example/steal"},
	}, "gripi_browser=chosen-token")
	if login.Code != http.StatusSeeOther || login.Header().Get("Location") != "http://example.com/" {
		t.Fatalf("login = %d, Location = %q", login.Code, login.Header().Get("Location"))
	}
	cookie := responseCookie(t, login)
	newToken := strings.TrimPrefix(cookie, "gripi_browser=")
	if newToken == "chosen-token" || len(newToken) != 64 {
		t.Fatalf("new token = %q", newToken)
	}
	if approved, err := store.Approved("chosen-token"); err != nil || approved {
		t.Fatalf("chosen token approved = %t, %v", approved, err)
	}
	if approved, err := store.Approved(newToken); err != nil || !approved {
		t.Fatalf("new token approved = %t, %v", approved, err)
	}

	secureLogin := performFormURL(handler, "https://example.com/browser-access/admin-login", url.Values{"password": {"secret"}}, cookie)
	secureCookie := secureLogin.Result().Cookies()[0]
	if !secureCookie.Secure || !secureCookie.HttpOnly || secureCookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("secure cookie = %#v", secureCookie)
	}
}

func TestBrowserAccessEndpointsValidateCodesAndAuthorization(t *testing.T) {
	cfg := testConfig(t)
	cfg.AdminPassword = "secret"
	cfg.BrowserAuthDisabled = false
	handler := newHandler(t, cfg)

	pending := performRequest(handler, http.MethodGet, "http://example.com/browser-access/pending", "gripi_browser=unknown")
	if pending.Code != http.StatusForbidden {
		t.Fatalf("pending status = %d", pending.Code)
	}
	store := access.NewBrowserStore(cfg.BrowserAccessPath)
	if _, err := store.ApproveCurrentBrowser("approver", ""); err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []string{"/browser-access/approve", "/browser-access/deny"} {
		response := performForm(handler, endpoint, url.Values{"code": {"invalid"}}, "gripi_browser=approver")
		if response.Code != http.StatusBadRequest || response.Body.String() != "Valid code is required" {
			t.Fatalf("%s response = %d, %q", endpoint, response.Code, response.Body.String())
		}
	}
}

func TestBrowserAccessRequestAndAdminLoginAreRateLimited(t *testing.T) {
	cfg := testConfig(t)
	cfg.AdminPassword = "secret"
	cfg.BrowserAuthDisabled = false
	handler := newHandler(t, cfg)
	cookie := "gripi_browser=browser"

	for index := 0; index < 30; index++ {
		response := performForm(handler, "/browser-access/request", nil, cookie)
		if response.Code != http.StatusSeeOther {
			t.Fatalf("request %d status = %d", index+1, response.Code)
		}
	}
	limitedRequest := performForm(handler, "/browser-access/request", nil, cookie)
	if limitedRequest.Code != http.StatusTooManyRequests || limitedRequest.Body.String() != "Too many access requests" {
		t.Fatalf("limited request = %d, %q", limitedRequest.Code, limitedRequest.Body.String())
	}
	if got := limitedRequest.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Fatalf("limited request Content-Type = %q", got)
	}

	for index := 0; index < 10; index++ {
		response := performForm(handler, "/browser-access/admin-login", url.Values{"password": {"wrong"}}, cookie)
		if response.Code != http.StatusForbidden {
			t.Fatalf("login %d status = %d", index+1, response.Code)
		}
	}
	limitedLogin := performForm(handler, "/browser-access/admin-login", url.Values{"password": {"secret"}}, cookie)
	if limitedLogin.Code != http.StatusTooManyRequests || limitedLogin.Body.String() != "Too many admin login attempts" {
		t.Fatalf("limited login = %d, %q", limitedLogin.Code, limitedLogin.Body.String())
	}
}

func performForm(handler http.Handler, path string, values url.Values, cookie string) *httptest.ResponseRecorder {
	return performFormURL(handler, "http://example.com"+path, values, cookie)
}

func performFormURL(handler http.Handler, target string, values url.Values, cookie string) *httptest.ResponseRecorder {
	if values == nil {
		values = url.Values{}
	}
	request := httptest.NewRequest(http.MethodPost, target, strings.NewReader(values.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if cookie != "" {
		request.Header.Set("Cookie", cookie)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func performRequest(handler http.Handler, method, target, cookie string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, target, nil)
	if cookie != "" {
		request.Header.Set("Cookie", cookie)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func responseCookie(t *testing.T, response *httptest.ResponseRecorder) string {
	t.Helper()
	cookies := response.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("response has no cookie")
	}
	return cookies[0].Name + "=" + cookies[0].Value
}

func assertJSON(t *testing.T, response *httptest.ResponseRecorder, expected map[string]any) {
	t.Helper()
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", response.Code, response.Body.String())
	}
	var actual map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &actual); err != nil {
		t.Fatal(err)
	}
	if len(actual) != len(expected) {
		t.Fatalf("JSON = %#v", actual)
	}
	for key, value := range expected {
		if actual[key] != value {
			t.Fatalf("JSON[%q] = %#v", key, actual[key])
		}
	}
}
