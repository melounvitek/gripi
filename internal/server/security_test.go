package server_test

import (
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"

	gripi "github.com/melounvitek/gripi"
	"github.com/melounvitek/gripi/internal/config"
	"github.com/melounvitek/gripi/internal/server"
)

const maxRequestBodyBytes = int64(64 << 20)

func TestHandlerRejectsOversizedBodiesBeforeParsing(t *testing.T) {
	cfg := testConfig(t)
	cfg.AdminPassword = "secret"
	handler := newHandler(t, cfg)
	request := httptest.NewRequest(http.MethodPost, "/browser-access/request", nil)
	request.ContentLength = maxRequestBodyBytes + 1
	request.Body = panicReadCloser{}
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d", response.Code)
	}
	if !strings.Contains(response.Body.String(), "Request body too large") {
		t.Fatalf("body = %q", response.Body.String())
	}
}

func TestHandlerRejectsOversizedUnknownLengthBodiesBeforeSecurityAndBrowserAccess(t *testing.T) {
	cfg := testConfig(t)
	cfg.AdminPassword = "secret"
	cfg.BrowserAuthDisabled = false
	handler := newHandler(t, cfg)
	request := httptest.NewRequest(http.MethodPost, "http://example.com/missing", nil)
	request.Body = io.NopCloser(repeatingReader{'x'})
	request.ContentLength = -1
	request.TransferEncoding = []string{"chunked"}
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, body = %q", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Content-Security-Policy"); got != "" {
		t.Fatalf("Content-Security-Policy = %q", got)
	}
	if got := response.Header().Values("Set-Cookie"); len(got) != 0 {
		t.Fatalf("Set-Cookie = %#v", got)
	}
	if _, err := os.Stat(cfg.BrowserAccessPath); !os.IsNotExist(err) {
		t.Fatalf("browser state exists or stat failed: %v", err)
	}
}

func TestHandlerNormalizesAllowedHostsAndDefaultsToLoopback(t *testing.T) {
	tests := []struct {
		name      string
		address   string
		permitted []string
		host      string
	}{
		{name: "loopback address permits localhost", address: "127.0.0.1:4567", host: "LOCALHOST:9999"},
		{name: "loopback address permits subdomains of localhost", address: "[::1]:4567", host: "app.localhost"},
		{name: "configured DNS host ignores case and port", address: "0.0.0.0:4567", permitted: []string{"Gateway.Example:443"}, host: "gateway.example:4567"},
		{name: "configured IPv4 host preserves optional ports", address: "0.0.0.0:4567", permitted: []string{"192.0.2.10:443"}, host: "192.0.2.10:4567"},
		{name: "configured IPv6 host is canonicalized", address: "[::]:4567", permitted: []string{"[0:0:0:0:0:0:0:1]:9999"}, host: "[::1]:4567"},
		{name: "configured wildcard matches hostname suffix", address: "0.0.0.0:4567", permitted: []string{".Example:443"}, host: "app.example:4567"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := testConfig(t)
			cfg.Production = true
			cfg.Address = test.address
			cfg.PermittedHosts = test.permitted
			handler := newHandler(t, cfg)
			request := httptest.NewRequest(http.MethodGet, "/", nil)
			request.Host = test.host
			request.RemoteAddr = "127.0.0.1:1234"
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			if response.Code != http.StatusNotFound {
				t.Fatalf("status = %d, body = %q", response.Code, response.Body.String())
			}
		})
	}
}

func TestHandlerRejectsAnEmptyConfiguredWildcardSuffix(t *testing.T) {
	cfg := testConfig(t)
	cfg.Production = true
	cfg.Address = "0.0.0.0:4567"
	cfg.PermittedHosts = []string{"."}
	handler := newHandler(t, cfg)
	request := httptest.NewRequest(http.MethodGet, "http://attacker.example./", nil)
	request.RemoteAddr = "127.0.0.1:1234"
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, body = %q", response.Code, response.Body.String())
	}
}

func TestHandlerAllowsDevelopmentTestAndIPHostsButStillBlocksArbitraryDNS(t *testing.T) {
	cfg := testConfig(t)
	cfg.Address = "0.0.0.0:4567"
	cfg.PermittedHosts = nil
	handler := newHandler(t, cfg)
	for _, host := range []string{"project.test", "192.0.2.10", "localhost"} {
		request := httptest.NewRequest(http.MethodGet, "/", nil)
		request.Host = host
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d", host, response.Code)
		}
	}
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Host = "attacker.example"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("attacker status = %d", response.Code)
	}
}

func TestHandlerRejectsOversizedUnknownLengthBodiesBeforeHostAuthorization(t *testing.T) {
	cfg := testConfig(t)
	cfg.Production = true
	cfg.PermittedHosts = []string{"gateway.example"}
	handler := newHandler(t, cfg)
	request := httptest.NewRequest(http.MethodPost, "http://attacker.example/missing", nil)
	request.Body = io.NopCloser(repeatingReader{'x'})
	request.ContentLength = -1
	request.TransferEncoding = []string{"chunked"}
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, body = %q", response.Code, response.Body.String())
	}
}

func TestHandlerRejectsInvalidDirectAndTrustedForwardedAuthorities(t *testing.T) {
	invalidAuthorities := map[string]string{
		"userinfo delimiter":  "user@gateway.example",
		"open redirect shape": "gateway.example:@evil.example",
		"path":                "gateway.example/path",
		"query":               "gateway.example?next=evil.example",
		"fragment":            "gateway.example#evil.example",
		"control character":   "gateway.example\x00.evil.example",
		"empty port":          "gateway.example:",
		"non-numeric port":    "gateway.example:https",
		"negative port":       "gateway.example:-1",
		"out-of-range port":   "gateway.example:65536",
		"unbracketed IPv6":    "2001:db8::1",
	}
	for _, source := range []string{"direct Host", "trusted X-Forwarded-Host"} {
		for name, invalidAuthority := range invalidAuthorities {
			t.Run(source+"/"+name, func(t *testing.T) {
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
				if source == "direct Host" {
					request.Host = invalidAuthority
				} else {
					request.Header.Set("X-Forwarded-Host", invalidAuthority)
					request.Header.Set("X-Forwarded-Proto", "https")
				}
				response := httptest.NewRecorder()

				handler.ServeHTTP(response, request)

				if response.Code != http.StatusForbidden {
					t.Fatalf("status = %d, Location = %q, body = %q", response.Code, response.Header().Get("Location"), response.Body.String())
				}
				if location := response.Header().Get("Location"); location != "" {
					t.Fatalf("Location = %q", location)
				}
			})
		}
	}
}

func TestHandlerRejectsInvalidTrustedForwardedPorts(t *testing.T) {
	for name, port := range map[string]string{
		"control":      "443\x00",
		"non-numeric":  "https",
		"negative":     "-1",
		"zero":         "0",
		"out of range": "65536",
	} {
		t.Run(name, func(t *testing.T) {
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
			request.Header.Set("X-Forwarded-Host", "gateway.example")
			request.Header.Set("X-Forwarded-Proto", "https")
			request.Header.Set("X-Forwarded-Port", port)
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			if response.Code != http.StatusForbidden || response.Header().Get("Location") != "" {
				t.Fatalf("status = %d, Location = %q, body = %q", response.Code, response.Header().Get("Location"), response.Body.String())
			}
		})
	}
}

func TestHandlerRejectsUnpermittedForwardedAuthorities(t *testing.T) {
	cfg := testConfig(t)
	cfg.Production = true
	cfg.PermittedHosts = []string{"gateway.example"}
	handler := newHandler(t, cfg)
	for name, header := range map[string]string{
		"X-Forwarded-Host": "gateway.example, attacker.example",
		"Forwarded":        `for=192.0.2.10;host="gateway.example";proto=https, for=192.0.2.11;host="attacker.example"`,
	} {
		request := httptest.NewRequest(http.MethodGet, "http://gateway.example/", nil)
		request.Header.Set(name, header)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "attacker.example") {
			t.Fatalf("%s response = %d, %q", name, response.Code, response.Body.String())
		}
	}
}

func TestHandlerRejectsMalformedRFCForwardedQuoting(t *testing.T) {
	invalidHeaders := []struct {
		name  string
		value string
	}{
		{name: "quoted separators cannot inject a later host", value: `host="attacker.example,host=gateway.example;proto=https"`},
		{name: "unterminated quoted host", value: `host="gateway.example`},
		{name: "escaped closing quote", value: `host="gateway.example\"`},
		{name: "invalid quoted escape", value: `host="gateway.example\q"`},
		{name: "characters after quoted host", value: `host="gateway.example"suffix`},
		{name: "empty element", value: `,host=gateway.example`},
		{name: "missing assignment", value: `for;host=gateway.example`},
		{name: "empty value", value: `for=;host=gateway.example`},
		{name: "invalid parameter name", value: `bad name=value;host=gateway.example`},
		{name: "duplicate parameter", value: `host=attacker.example;host=gateway.example`},
	}
	for _, test := range invalidHeaders {
		t.Run(test.name, func(t *testing.T) {
			cfg := testConfig(t)
			cfg.Production = true
			cfg.PermittedHosts = []string{"gateway.example"}
			handler := newHandler(t, cfg)
			request := httptest.NewRequest(http.MethodGet, "http://gateway.example/", nil)
			request.RemoteAddr = "127.0.0.1:1234"
			request.Header.Set("Forwarded", test.value)
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			if response.Code != http.StatusForbidden {
				t.Fatalf("status = %d, body = %q", response.Code, response.Body.String())
			}
		})
	}
}

func TestHandlerUsesTheLastValidRFCForwardedHost(t *testing.T) {
	tests := []struct {
		name        string
		headerLines []string
		wantStatus  int
	}{
		{
			name:        "last element with an RFC quoted pair",
			headerLines: []string{`for=192.0.2.10;host="attacker.example", for=192.0.2.11;host="gateway\.example"`},
			wantStatus:  http.StatusNotFound,
		},
		{
			name:        "Go hex escapes are not decoded",
			headerLines: []string{`host="gateway\x2eexample"`},
			wantStatus:  http.StatusForbidden,
		},
		{
			name:        "last header line",
			headerLines: []string{`host="gateway.example"`, `host="attacker.example"`},
			wantStatus:  http.StatusForbidden,
		},
		{
			name:        "authorized last header line",
			headerLines: []string{`host="attacker.example"`, `host="gateway.example"`},
			wantStatus:  http.StatusNotFound,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := testConfig(t)
			cfg.Production = true
			cfg.PermittedHosts = []string{"gateway.example"}
			handler := newHandler(t, cfg)
			request := httptest.NewRequest(http.MethodGet, "http://gateway.example/", nil)
			request.RemoteAddr = "127.0.0.1:1234"
			for _, line := range test.headerLines {
				request.Header.Add("Forwarded", line)
			}
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, body = %q", response.Code, response.Body.String())
			}
		})
	}
}

func TestHandlerExplainsBlockedHostsWithoutReflectingInvalidMarkup(t *testing.T) {
	cfg := testConfig(t)
	cfg.Production = true
	cfg.Address = "127.0.0.1:4567"
	cfg.PermittedHosts = nil
	handler := newHandler(t, cfg)

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Host = "attacker.example:443"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d", response.Code)
	}
	if got := response.Header().Get("Content-Security-Policy"); got != "default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'" {
		t.Fatalf("Content-Security-Policy = %q", got)
	}
	for _, text := range []string{"Gateway hostname blocked", "GRIPI_PERMITTED_HOSTS=attacker.example", "Only continue if you recognize", "systemctl --user restart gripi.service"} {
		if !strings.Contains(response.Body.String(), text) {
			t.Fatalf("body does not contain %q", text)
		}
	}

	invalid := httptest.NewRequest(http.MethodGet, "/", nil)
	invalid.Host = "evil<script>.example"
	invalidResponse := httptest.NewRecorder()
	handler.ServeHTTP(invalidResponse, invalid)
	if !strings.Contains(invalidResponse.Body.String(), "Add the intended exact hostname to GRIPI_PERMITTED_HOSTS") {
		t.Fatalf("body = %q", invalidResponse.Body.String())
	}
	if strings.Contains(invalidResponse.Body.String(), "evil&lt;script&gt;") || strings.Contains(invalidResponse.Body.String(), "evil<script>") {
		t.Fatal("invalid hostname was reflected")
	}

	proxy := httptest.NewRequest(http.MethodGet, "/", nil)
	proxy.Host = "remote.example"
	proxy.RemoteAddr = "127.0.0.1:1234"
	proxy.Header.Set("X-Forwarded-Host", "remote.example")
	proxy.Header.Set("X-Forwarded-Proto", "https")
	proxyResponse := httptest.NewRecorder()
	handler.ServeHTTP(proxyResponse, proxy)
	if !strings.Contains(proxyResponse.Body.String(), "GRIPI_TRUST_PROXY_HEADERS=1") {
		t.Fatalf("proxy diagnostic body = %q", proxyResponse.Body.String())
	}
}

func TestHandlerRequiresHTTPSForProductionRemoteClientsAndTrustsConfiguredProxyHeaders(t *testing.T) {
	tests := []struct {
		name          string
		remoteAddr    string
		trustProxy    bool
		forwarded     string
		absoluteHTTPS bool
		wantStatus    int
	}{
		{name: "remote HTTP is rejected", remoteAddr: "192.0.2.10:1234", wantStatus: http.StatusForbidden},
		{name: "loopback HTTP is allowed", remoteAddr: "127.0.0.1:1234", wantStatus: http.StatusNotFound},
		{name: "trusted forwarded HTTPS is allowed", remoteAddr: "192.0.2.10:1234", trustProxy: true, forwarded: "https, http", wantStatus: http.StatusNotFound},
		{name: "untrusted forwarded HTTPS is rejected", remoteAddr: "192.0.2.10:1234", forwarded: "https", wantStatus: http.StatusForbidden},
		{name: "trusted forwarded HTTP overrides direct HTTPS", remoteAddr: "192.0.2.10:1234", trustProxy: true, forwarded: "http", wantStatus: http.StatusForbidden},
		{name: "absolute HTTPS target over plain HTTP is rejected", remoteAddr: "192.0.2.10:1234", absoluteHTTPS: true, wantStatus: http.StatusForbidden},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := testConfig(t)
			cfg.Production = true
			cfg.Address = "0.0.0.0:4567"
			cfg.PermittedHosts = []string{"gateway.example"}
			cfg.TrustProxyHeaders = test.trustProxy
			handler := newHandler(t, cfg)
			request := httptest.NewRequest(http.MethodGet, "https://gateway.example/", nil)
			if test.name != "trusted forwarded HTTP overrides direct HTTPS" {
				request = httptest.NewRequest(http.MethodGet, "http://gateway.example/", nil)
			}
			if test.absoluteHTTPS {
				request.URL.Scheme = "https"
			}
			request.RemoteAddr = test.remoteAddr
			request.Header.Set("X-Forwarded-Proto", test.forwarded)
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, body = %q", response.Code, response.Body.String())
			}
			if test.wantStatus == http.StatusForbidden && response.Body.String() != "Remote Gripi access requires HTTPS. See docs/configuration.md." {
				t.Fatalf("body = %q", response.Body.String())
			}
		})
	}
}

func TestHandlerProtectsUnsafeRequestOrigins(t *testing.T) {
	tests := []struct {
		name          string
		origin        string
		fetchSite     string
		trustProxy    bool
		forwarded     string
		forwardedHost string
		wantStatus    int
		wantBody      string
	}{
		{name: "same origin", origin: "http://gateway.example", wantStatus: http.StatusSeeOther},
		{name: "same origin opaque origin", origin: "null", fetchSite: "same-origin", wantStatus: http.StatusSeeOther},
		{name: "cross origin", origin: "https://other.example", wantStatus: http.StatusForbidden, wantBody: "Cross-origin request blocked"},
		{name: "cross site metadata", origin: "http://gateway.example", fetchSite: "cross-site", wantStatus: http.StatusForbidden, wantBody: "Cross-origin request blocked"},
		{name: "trusted forwarded origin", origin: "https://public.example", trustProxy: true, forwarded: "https", forwardedHost: "public.example", wantStatus: http.StatusSeeOther},
		{name: "trusted forwarded chain uses authorized last host", origin: "https://public.example", trustProxy: true, forwarded: "https", forwardedHost: "attacker.example, public.example", wantStatus: http.StatusSeeOther},
		{name: "trusted forwarded chain rejects unselected first host", origin: "https://attacker.example", trustProxy: true, forwarded: "https", forwardedHost: "attacker.example, public.example", wantStatus: http.StatusForbidden, wantBody: "Cross-origin request blocked"},
		{name: "untrusted forwarded origin explains configuration", origin: "https://public.example", forwarded: "https", wantStatus: http.StatusForbidden, wantBody: "Trusted proxy configuration required"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := testConfig(t)
			cfg.AdminPassword = "secret"
			cfg.BrowserAuthDisabled = false
			cfg.PermittedHosts = []string{"gateway.example", "public.example"}
			cfg.TrustProxyHeaders = test.trustProxy
			handler := newHandler(t, cfg)
			request := httptest.NewRequest(http.MethodPost, "http://gateway.example/browser-access/request", strings.NewReader("return_to=%2F"))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			request.Header.Set("Origin", test.origin)
			request.Header.Set("Sec-Fetch-Site", test.fetchSite)
			request.Header.Set("X-Forwarded-Proto", test.forwarded)
			request.Header.Set("X-Forwarded-Host", test.forwardedHost)
			request.RemoteAddr = "127.0.0.1:1234"
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, body = %q", response.Code, response.Body.String())
			}
			if test.wantBody != "" && !strings.Contains(response.Body.String(), test.wantBody) {
				t.Fatalf("body does not contain %q", test.wantBody)
			}
		})
	}
}

func TestHandlerAddsSecurityHeadersAndUsesTheCSPNonceInTemplates(t *testing.T) {
	cfg := testConfig(t)
	handler := newHandler(t, cfg)
	request := httptest.NewRequest(http.MethodGet, "https://example.com/notification-test", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	if got := response.Header().Get("Cache-Control"); got != "private, no-store" {
		t.Fatalf("Cache-Control = %q", got)
	}
	if got := response.Header().Get("Referrer-Policy"); got != "no-referrer" {
		t.Fatalf("Referrer-Policy = %q", got)
	}
	if got := response.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q", got)
	}
	if got := response.Header().Get("X-XSS-Protection"); got != "" {
		t.Fatalf("X-XSS-Protection = %q", got)
	}
	if got := response.Header().Get("Strict-Transport-Security"); got != "max-age=31536000" {
		t.Fatalf("Strict-Transport-Security = %q", got)
	}
	nonceMatch := regexp.MustCompile(`script-src 'self' 'nonce-([^']+)'`).FindStringSubmatch(response.Header().Get("Content-Security-Policy"))
	if len(nonceMatch) != 2 {
		t.Fatalf("Content-Security-Policy = %q", response.Header().Get("Content-Security-Policy"))
	}
	templateNonce := regexp.MustCompile(`script nonce="([^"]+)"`).FindStringSubmatch(response.Body.String())
	if len(templateNonce) != 2 || html.UnescapeString(templateNonce[1]) != nonceMatch[1] {
		t.Fatal("template nonce does not match the CSP nonce")
	}
}

type panicReadCloser struct{}

func (panicReadCloser) Read([]byte) (int, error) { panic("request body was read") }
func (panicReadCloser) Close() error             { return nil }

type repeatingReader struct{ value byte }

func (reader repeatingReader) Read(buffer []byte) (int, error) {
	for index := range buffer {
		buffer[index] = reader.value
	}
	return len(buffer), nil
}

func testConfig(t *testing.T) config.Config {
	t.Helper()
	return config.Config{
		Address:             "127.0.0.1:4567",
		BrowserAuthDisabled: true,
		BrowserAccessPath:   t.TempDir() + "/browser-access.json",
		PermittedHosts:      []string{"example.com", "gateway.example"},
	}
}

func newHandler(t *testing.T, cfg config.Config) http.Handler {
	t.Helper()
	handler, err := server.NewHandler(cfg, gripi.WebFiles)
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

var _ io.ReadCloser = panicReadCloser{}
