package server

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/melounvitek/gripi/internal/access"
)

const workspaceCookieName = "gripi_workspace"

var (
	workspaceTokenPattern = regexp.MustCompile(`^piu_[A-Za-z0-9_-]{16,}$`)
	workspaceKeyClasses   = []*regexp.Regexp{
		regexp.MustCompile(`[a-z]`),
		regexp.MustCompile(`[A-Z]`),
		regexp.MustCompile(`[0-9]`),
		regexp.MustCompile(`[^a-zA-Z0-9]`),
	}
	workspacePaths = map[string]bool{
		"/workspace-key":            true,
		"/workspace-token/generate": true,
		"/workspace-access/status":  true,
		"/workspace-access/pending": true,
		"/workspace-access/approve": true,
		"/workspace-access/deny":    true,
	}
)

type workspacePageData struct {
	Pending          *access.WorkspaceRequest
	BootstrapPending *access.WorkspaceRequest
	GeneratedToken   string
	ReturnTo         string
	Error            string
	Bootstrap        bool
	AutoApprove      bool
	AccessPath       string
	Nonce            string
}

func (app *application) registerWorkspaceRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /workspace-token/generate", app.generateWorkspaceToken)
	mux.HandleFunc("POST /workspace-key", app.submitWorkspaceKey)
	mux.HandleFunc("GET /workspace-access/status", app.workspaceStatus)
	mux.HandleFunc("GET /workspace-access/pending", app.pendingWorkspaceAccess)
	mux.HandleFunc("POST /workspace-access/approve", app.approveWorkspaceAccess)
	mux.HandleFunc("POST /workspace-access/deny", app.denyWorkspaceAccess)
}

func (app *application) enforceWorkspaceAccess(next http.Handler) http.Handler {
	if !app.config.MultiUserMode {
		return next
	}
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if workspacePaths[request.URL.Path] || staticAssetPath(request.URL.Path) {
			next.ServeHTTP(response, request)
			return
		}
		approved, err := app.approvedWorkspace(request)
		if err != nil {
			writeInternalError(response, "check workspace approval", err)
			return
		}
		if approved {
			next.ServeHTTP(response, request)
			return
		}
		bootstrap, err := app.workspaceBootstrapRequired()
		if err != nil {
			writeInternalError(response, "check workspace bootstrap state", err)
			return
		}
		app.renderWorkspacePage(response, request, http.StatusForbidden, workspacePageData{ReturnTo: request.URL.RequestURI(), Bootstrap: bootstrap})
	})
}

func (app *application) generateWorkspaceToken(response http.ResponseWriter, request *http.Request) {
	if !app.config.MultiUserMode {
		http.NotFound(response, request)
		return
	}
	if !parseForm(response, request) {
		return
	}
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		writeText(response, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	bootstrap, err := app.workspaceBootstrapRequired()
	if err != nil {
		writeText(response, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	app.renderWorkspacePage(response, request, http.StatusOK, workspacePageData{GeneratedToken: "piu_" + base64.RawURLEncoding.EncodeToString(value), ReturnTo: safeReturnTo(request.FormValue("return_to")), Bootstrap: bootstrap})
}

func (app *application) submitWorkspaceKey(response http.ResponseWriter, request *http.Request) {
	if !app.config.MultiUserMode {
		http.NotFound(response, request)
		return
	}
	if !parseForm(response, request) {
		return
	}
	returnTo := safeReturnTo(request.FormValue("return_to"))
	key := strings.TrimSpace(request.FormValue("workspace_key"))
	bootstrap, err := app.workspaceBootstrapRequired()
	if err != nil {
		writeText(response, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	if !validWorkspaceKey(key) {
		app.renderWorkspacePage(response, request, http.StatusForbidden, workspacePageData{ReturnTo: returnTo, Bootstrap: bootstrap, Error: "Enter a valid user token. Generate a new token if you do not have one yet."})
		return
	}
	workspaceID := access.WorkspaceID(app.workspaceSecret, key)
	approved, err := app.workspaceStore.Approved(workspaceID)
	if err != nil {
		writeText(response, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	if approved || app.config.BrowserAuthDisabled {
		if !approved {
			err = app.workspaceStore.ApproveWorkspace(workspaceID)
		}
		if err != nil {
			writeText(response, http.StatusInternalServerError, "Internal Server Error")
			return
		}
		app.setWorkspaceCookie(response, request, workspaceID)
		redirect(response, request, returnTo, app.config.TrustProxyHeaders)
		return
	}
	if bootstrap {
		if !app.adminLimiter.Allow(clientIP(request.RemoteAddr)) {
			writeText(response, http.StatusTooManyRequests, "Too many admin login attempts")
			return
		}
		if secureEqual(request.FormValue("admin_password"), app.config.AdminPassword) {
			if err := app.workspaceStore.ApproveWorkspace(workspaceID); err != nil {
				writeText(response, http.StatusInternalServerError, "Internal Server Error")
				return
			}
			app.setWorkspaceCookie(response, request, workspaceID)
			redirect(response, request, returnTo, app.config.TrustProxyHeaders)
			return
		}
		pending, requestErr := app.requestWorkspaceAccess(response, request, workspaceID)
		if requestErr != nil {
			return
		}
		app.renderWorkspacePage(response, request, http.StatusForbidden, workspacePageData{ReturnTo: returnTo, Bootstrap: true, Error: "Admin password did not match.", BootstrapPending: &pending})
		return
	}
	pending, err := app.requestWorkspaceAccess(response, request, workspaceID)
	if err != nil {
		return
	}
	app.renderWorkspacePage(response, request, http.StatusForbidden, workspacePageData{Pending: &pending, ReturnTo: returnTo})
}

func (app *application) requestWorkspaceAccess(response http.ResponseWriter, request *http.Request, workspaceID string) (access.WorkspaceRequest, error) {
	if !app.accessLimiter.Allow(clientIP(request.RemoteAddr)) {
		writeText(response, http.StatusTooManyRequests, "Too many access requests")
		return access.WorkspaceRequest{}, errors.New("rate limited")
	}
	token, err := app.browserToken(response, request)
	if err == nil {
		var full *access.WorkspacePendingRequestsFullError
		var pending access.WorkspaceRequest
		pending, err = app.workspaceStore.RequestAccess(workspaceID, token)
		if errors.As(err, &full) {
			writeText(response, http.StatusServiceUnavailable, "Too many pending workspace access requests")
			return access.WorkspaceRequest{}, err
		}
		if err == nil {
			return pending, nil
		}
	}
	writeText(response, http.StatusInternalServerError, "Internal Server Error")
	return access.WorkspaceRequest{}, err
}

func (app *application) workspaceStatus(response http.ResponseWriter, request *http.Request) {
	if !app.config.MultiUserMode {
		http.NotFound(response, request)
		return
	}
	code := request.URL.Query().Get("code")
	if !accessCodePattern.MatchString(code) {
		writeText(response, http.StatusBadRequest, "Valid code is required")
		return
	}
	pending, found, err := app.workspaceStore.RequestForCode(code)
	if err != nil {
		writeText(response, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	status := "unknown"
	if found {
		approved, approvalErr := app.workspaceStore.Approved(pending.WorkspaceID)
		if approvalErr != nil {
			writeText(response, http.StatusInternalServerError, "Internal Server Error")
			return
		}
		if approved {
			status = "approved"
			if token, valid := submittedBrowserToken(request); valid && token == pending.BrowserToken {
				app.setWorkspaceCookie(response, request, pending.WorkspaceID)
			}
		} else if pending.DeniedAt != "" {
			status = "denied"
		} else {
			status = "pending"
		}
	}
	writeJSON(response, map[string]string{"status": status})
}

func (app *application) pendingWorkspaceAccess(response http.ResponseWriter, request *http.Request) {
	if !app.requireApprovedWorkspace(response, request) {
		return
	}
	requests, err := app.workspaceStore.PendingRequests()
	if err != nil {
		writeText(response, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	type visibleRequest struct {
		Code        string `json:"code"`
		CreatedAt   string `json:"created_at"`
		RequestedAt string `json:"requested_at"`
	}
	visible := make([]visibleRequest, 0, len(requests))
	for _, pending := range requests {
		visible = append(visible, visibleRequest{Code: pending.Code, CreatedAt: pending.CreatedAt, RequestedAt: pending.RequestedAt})
	}
	writeJSON(response, map[string]any{"requests": visible})
}

func (app *application) approveWorkspaceAccess(response http.ResponseWriter, request *http.Request) {
	app.resolveWorkspaceAccess(response, request, app.workspaceStore.ApproveCode)
}

func (app *application) denyWorkspaceAccess(response http.ResponseWriter, request *http.Request) {
	app.resolveWorkspaceAccess(response, request, app.workspaceStore.DenyCode)
}

func (app *application) resolveWorkspaceAccess(response http.ResponseWriter, request *http.Request, resolve func(string) (access.WorkspaceRequest, bool, error)) {
	if !app.requireApprovedWorkspace(response, request) || !parseForm(response, request) {
		return
	}
	code := request.FormValue("code")
	if !accessCodePattern.MatchString(code) {
		writeText(response, http.StatusBadRequest, "Valid code is required")
		return
	}
	_, found, err := resolve(code)
	if err != nil {
		writeText(response, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	writeJSON(response, map[string]bool{"ok": found})
}

func (app *application) workspaceBootstrapRequired() (bool, error) {
	if app.config.BrowserAuthDisabled {
		return false, nil
	}
	approved, err := app.workspaceStore.AnyApproved()
	return !approved, err
}

func (app *application) requireApprovedWorkspace(response http.ResponseWriter, request *http.Request) bool {
	if !app.config.MultiUserMode {
		http.NotFound(response, request)
		return false
	}
	approved, err := app.approvedWorkspace(request)
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

func (app *application) approvedWorkspace(request *http.Request) (bool, error) {
	return app.workspaceStore.Approved(currentWorkspaceID(request))
}

func currentWorkspaceID(request *http.Request) string {
	cookie, err := request.Cookie(workspaceCookieName)
	if err != nil {
		return ""
	}
	return cookie.Value
}

func (app *application) setWorkspaceCookie(response http.ResponseWriter, request *http.Request, workspaceID string) {
	http.SetCookie(response, &http.Cookie{
		Name:     workspaceCookieName,
		Value:    workspaceID,
		Path:     "/",
		MaxAge:   365 * 24 * 60 * 60,
		HttpOnly: true,
		Secure:   app.secureTransport(request),
		SameSite: http.SameSiteLaxMode,
	})
}

func validWorkspaceKey(key string) bool {
	if len(key) > 256 {
		return false
	}
	if workspaceTokenPattern.MatchString(key) {
		return true
	}
	if utf8.RuneCountInString(key) < 12 {
		return false
	}
	classes := 0
	for _, pattern := range workspaceKeyClasses {
		if pattern.MatchString(key) {
			classes++
		}
	}
	return classes >= 3
}

func (app *application) renderWorkspacePage(response http.ResponseWriter, request *http.Request, status int, data workspacePageData) {
	data.AutoApprove = app.config.BrowserAuthDisabled
	data.AccessPath = app.config.WorkspaceAccessPath
	data.Nonce = requestNonce(request)
	if data.ReturnTo == "" {
		data.ReturnTo = "/"
	}
	response.Header().Set("Content-Type", "text/html; charset=utf-8")
	response.WriteHeader(status)
	_ = app.templates.ExecuteTemplate(response, "workspace_key.html", data)
}
