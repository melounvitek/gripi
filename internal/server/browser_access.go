package server

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/netip"
	"regexp"
	"strings"

	"github.com/melounvitek/gripi/internal/access"
)

var accessCodePattern = regexp.MustCompile(`^[A-Z0-9]{4}-[A-Z0-9]{4}$`)

var browserAccessPaths = map[string]struct{}{
	"/browser-access/request":     {},
	"/browser-access/admin-login": {},
	"/browser-access/status":      {},
	"/browser-access/pending":     {},
	"/browser-access/approve":     {},
	"/browser-access/deny":        {},
}

type accessPageData struct {
	Request           *access.PendingRequest
	ReturnTo          string
	Error             string
	BrowserAccessPath string
	Nonce             string
}

func (app *application) registerBrowserAccessRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /browser-access/request", app.requestBrowserAccess)
	mux.HandleFunc("/browser-access/request", http.NotFound)
	mux.HandleFunc("POST /browser-access/admin-login", app.adminLogin)
	mux.HandleFunc("/browser-access/admin-login", http.NotFound)
	mux.HandleFunc("GET /browser-access/status", app.browserAccessStatus)
	mux.HandleFunc("/browser-access/status", http.NotFound)
	mux.HandleFunc("GET /browser-access/pending", app.pendingBrowserAccess)
	mux.HandleFunc("/browser-access/pending", http.NotFound)
	mux.HandleFunc("POST /browser-access/approve", app.approveBrowserAccess)
	mux.HandleFunc("/browser-access/approve", http.NotFound)
	mux.HandleFunc("POST /browser-access/deny", app.denyBrowserAccess)
	mux.HandleFunc("/browser-access/deny", http.NotFound)
}

func (app *application) enforceBrowserAccess(next http.Handler) http.Handler {
	if !app.browserAccessEnabled() || app.config.MultiUserMode {
		return next
	}
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if _, accessEndpoint := browserAccessPaths[request.URL.Path]; accessEndpoint || staticAssetPath(request.URL.Path) {
			next.ServeHTTP(response, request)
			return
		}
		token, err := app.browserToken(response, request)
		if err != nil {
			writeInternalError(response, "load browser token", err)
			return
		}
		approved, err := app.browserStore.Approved(token)
		if err != nil {
			writeInternalError(response, "check browser approval", err)
			return
		}
		if approved {
			next.ServeHTTP(response, request)
			return
		}
		pending, found, err := app.browserStore.PendingRequest(token)
		if err != nil {
			writeInternalError(response, "load browser access request", err)
			return
		}
		var pendingPointer *access.PendingRequest
		if found {
			pendingPointer = &pending
		}
		returnTo := request.URL.RequestURI()
		if returnTo == "" {
			returnTo = "/"
		}
		app.renderAccessPage(response, request, http.StatusForbidden, pendingPointer, returnTo, "")
	})
}

func (app *application) requestBrowserAccess(response http.ResponseWriter, request *http.Request) {
	if !app.browserAccessEnabled() {
		http.NotFound(response, request)
		return
	}
	if !app.accessLimiter.Allow(clientIP(request.RemoteAddr)) {
		writeText(response, http.StatusTooManyRequests, "Too many access requests")
		return
	}
	if !parseForm(response, request) {
		return
	}
	token, err := app.browserToken(response, request)
	if err != nil {
		writeText(response, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	_, err = app.browserStore.RequestAccess(token, app.browserRequestIP(request), request.UserAgent())
	if err != nil {
		var full *access.PendingRequestsFullError
		if errors.As(err, &full) {
			app.renderAccessPage(
				response,
				request,
				http.StatusServiceUnavailable,
				nil,
				safeReturnTo(request.Form.Get("return_to")),
				"Too many pending browser access requests. Try again after an existing request is approved or denied.",
			)
			return
		}
		writeInternalError(response, "request browser access", err)
		return
	}
	redirect(response, request, safeReturnTo(request.Form.Get("return_to")), app.config.TrustProxyHeaders)
}

func (app *application) adminLogin(response http.ResponseWriter, request *http.Request) {
	if !app.browserAccessEnabled() {
		http.NotFound(response, request)
		return
	}
	if !app.adminLimiter.Allow(clientIP(request.RemoteAddr)) {
		writeText(response, http.StatusTooManyRequests, "Too many admin login attempts")
		return
	}
	if !parseForm(response, request) {
		return
	}
	returnTo := safeReturnTo(request.Form.Get("return_to"))
	if secureEqual(request.Form.Get("password"), app.config.AdminPassword) {
		oldToken, _ := submittedBrowserToken(request)
		newToken, err := app.newBrowserToken()
		if err != nil {
			writeText(response, http.StatusInternalServerError, "Internal Server Error")
			return
		}
		if _, err := app.browserStore.ReplaceBrowserToken(oldToken, newToken, request.UserAgent()); err != nil {
			writeText(response, http.StatusInternalServerError, "Internal Server Error")
			return
		}
		app.setBrowserToken(response, request, newToken)
		redirect(response, request, returnTo, app.config.TrustProxyHeaders)
		return
	}

	token, err := app.browserToken(response, request)
	if err != nil {
		writeText(response, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	pending, found, err := app.browserStore.PendingRequest(token)
	if err != nil {
		writeText(response, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	var pendingPointer *access.PendingRequest
	if found {
		pendingPointer = &pending
	}
	app.renderAccessPage(response, request, http.StatusForbidden, pendingPointer, returnTo, "Admin password did not match.")
}

func (app *application) browserAccessStatus(response http.ResponseWriter, request *http.Request) {
	if !app.browserAccessEnabled() {
		http.NotFound(response, request)
		return
	}
	token, err := app.browserToken(response, request)
	if err != nil {
		writeText(response, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	status, err := app.browserStore.PendingStatus(token)
	if err != nil {
		writeText(response, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	writeJSON(response, struct {
		Status access.Status `json:"status"`
	}{status})
}

func (app *application) pendingBrowserAccess(response http.ResponseWriter, request *http.Request) {
	if !app.requireApprovedBrowser(response, request) {
		return
	}
	requests, err := app.browserStore.PendingRequests()
	if err != nil {
		writeText(response, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	writeJSON(response, struct {
		Requests []access.PendingRequest `json:"requests"`
	}{requests})
}

func (app *application) approveBrowserAccess(response http.ResponseWriter, request *http.Request) {
	app.resolveBrowserAccess(response, request, app.browserStore.ApproveCode)
}

func (app *application) denyBrowserAccess(response http.ResponseWriter, request *http.Request) {
	app.resolveBrowserAccess(response, request, app.browserStore.DenyCode)
}

func (app *application) resolveBrowserAccess(response http.ResponseWriter, request *http.Request, resolve func(string) (access.PendingRequest, bool, error)) {
	if !app.requireApprovedBrowser(response, request) {
		return
	}
	if !parseForm(response, request) {
		return
	}
	code := request.Form.Get("code")
	if !accessCodePattern.MatchString(code) {
		writeText(response, http.StatusBadRequest, "Valid code is required")
		return
	}
	_, found, err := resolve(code)
	if err != nil {
		writeText(response, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	writeJSON(response, struct {
		OK bool `json:"ok"`
	}{found})
}

func (app *application) requireApprovedBrowser(response http.ResponseWriter, request *http.Request) bool {
	if !app.browserAccessEnabled() {
		writeText(response, http.StatusForbidden, "Forbidden")
		return false
	}
	approved, err := app.approvedBrowser(response, request)
	if err != nil {
		writeText(response, http.StatusInternalServerError, "Internal Server Error")
		return false
	}
	if !approved {
		writeText(response, http.StatusForbidden, "Forbidden")
		return false
	}
	return true
}

func (app *application) approvedBrowser(response http.ResponseWriter, request *http.Request) (bool, error) {
	token, err := app.browserToken(response, request)
	if err != nil {
		return false, err
	}
	return app.browserStore.Approved(token)
}

func (app *application) browserAccessEnabled() bool {
	return !app.config.BrowserAuthDisabled && app.config.AdminPassword != ""
}

func (app *application) browserRequestIP(request *http.Request) string {
	if app.config.TrustProxyHeaders {
		forwarded := firstForwardedValue(request.Header.Get("X-Forwarded-For"))
		if address, err := netip.ParseAddr(forwarded); err == nil {
			return address.String()
		}
	}
	return clientIP(request.RemoteAddr)
}

func (app *application) browserToken(response http.ResponseWriter, request *http.Request) (string, error) {
	if token, valid := submittedBrowserToken(request); valid {
		return token, nil
	}
	token, err := app.newBrowserToken()
	if err != nil {
		return "", err
	}
	app.setBrowserToken(response, request, token)
	return token, nil
}

func submittedBrowserToken(request *http.Request) (string, bool) {
	cookie, err := request.Cookie("gripi_browser")
	if err != nil || cookie.Value == "" || len(cookie.Value) > 128 {
		return "", false
	}
	return cookie.Value, true
}

func (app *application) setBrowserToken(response http.ResponseWriter, request *http.Request, token string) {
	http.SetCookie(response, &http.Cookie{
		Name:     "gripi_browser",
		Value:    token,
		Path:     "/",
		MaxAge:   365 * 24 * 60 * 60,
		HttpOnly: true,
		Secure:   app.secureTransport(request),
		SameSite: http.SameSiteLaxMode,
	})
}

func (app *application) renderAccessPage(response http.ResponseWriter, request *http.Request, status int, pending *access.PendingRequest, returnTo, message string) {
	response.Header().Set("Content-Type", "text/html; charset=utf-8")
	response.WriteHeader(status)
	_ = app.templates.ExecuteTemplate(response, "access_blocked.html", accessPageData{
		Request:           pending,
		ReturnTo:          returnTo,
		Error:             message,
		BrowserAccessPath: app.config.BrowserAccessPath,
		Nonce:             requestNonce(request),
	})
}

func safeReturnTo(value string) string {
	if len(value) <= 2048 && strings.HasPrefix(value, "/") && !strings.HasPrefix(value, "//") {
		return value
	}
	return "/"
}

func secureEqual(left, right string) bool {
	leftDigest := sha256.Sum256([]byte(left))
	rightDigest := sha256.Sum256([]byte(right))
	return subtle.ConstantTimeCompare(leftDigest[:], rightDigest[:]) == 1
}

func randomBrowserToken() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func redirect(response http.ResponseWriter, request *http.Request, target string, trustProxy bool) {
	response.Header().Set("Content-Type", "text/html;charset=utf-8")
	response.Header().Set("Location", absoluteRedirectURL(request, target, trustProxy))
	response.WriteHeader(http.StatusSeeOther)
}

func writeJSON(response http.ResponseWriter, value any) {
	contents, err := json.Marshal(value)
	if err != nil {
		writeText(response, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	if response.Header().Get("Content-Type") == "" {
		response.Header().Set("Content-Type", "application/json")
	}
	_, _ = response.Write(contents)
}

func staticAssetPath(path string) bool {
	if strings.HasPrefix(path, "/assets/") {
		return true
	}
	switch path {
	case "/apple-touch-icon.png", "/manifest.webmanifest", "/app-icon.svg", "/app-icon-maskable.svg", "/service-worker.js":
		return true
	default:
		return false
	}
}
