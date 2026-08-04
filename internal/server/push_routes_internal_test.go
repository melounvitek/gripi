package server

import (
	"context"
	"crypto/elliptic"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	gripi "github.com/melounvitek/gripi"
	"github.com/melounvitek/gripi/internal/config"
	"github.com/melounvitek/gripi/internal/push"
)

func TestWebPushRoutesRegisterAndRemoveTheCurrentOwnersSubscription(t *testing.T) {
	handler := newPushTestHandler(t, false, true)
	app := handler.app
	subscription := pushRouteSubscription("device")

	configResponse := pushRequest(handler, http.MethodGet, "/web-push/config", "", "")
	if configResponse.Code != http.StatusOK || configResponse.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("config response = %d, Cache-Control %q", configResponse.Code, configResponse.Header().Get("Cache-Control"))
	}
	var pushConfig struct {
		PublicKey string `json:"public_key"`
	}
	if err := json.Unmarshal(configResponse.Body.Bytes(), &pushConfig); err != nil || pushConfig.PublicKey == "" {
		t.Fatalf("config = %#v, %v", pushConfig, err)
	}

	registered := pushRequest(handler, http.MethodPut, "/web-push/subscription", pushJSON(t, map[string]any{
		"endpoint": subscription.Endpoint, "expirationTime": nil, "keys": subscription.Keys,
	}), "")
	if registered.Code != http.StatusNoContent {
		t.Fatalf("register response = %d %s", registered.Code, registered.Body.String())
	}
	stored, err := app.pushSubscriptions.List(singleUserOwner)
	if err != nil || len(stored) != 1 || stored[0] != subscription {
		t.Fatalf("stored subscriptions = %#v, %v", stored, err)
	}

	removed := pushRequest(handler, http.MethodDelete, "/web-push/subscription", pushJSON(t, map[string]string{"endpoint": subscription.Endpoint}), "")
	if removed.Code != http.StatusNoContent {
		t.Fatalf("remove response = %d %s", removed.Code, removed.Body.String())
	}
	stored, err = app.pushSubscriptions.List(singleUserOwner)
	if err != nil || len(stored) != 0 {
		t.Fatalf("subscriptions after removal = %#v, %v", stored, err)
	}
}

func TestWebPushRoutesRequireApprovedAccessAndPreserveOwnerIsolation(t *testing.T) {
	handler := newPushTestHandler(t, false, false)
	subscription := pushRouteSubscription("browser")

	blocked := pushRequest(handler, http.MethodPut, "/web-push/subscription", pushJSON(t, subscription), "")
	if blocked.Code != http.StatusForbidden {
		t.Fatalf("unapproved response = %d", blocked.Code)
	}

	if _, err := handler.app.browserStore.ApproveCurrentBrowser("approved-browser", "test"); err != nil {
		t.Fatal(err)
	}
	approved := pushRequest(handler, http.MethodPut, "/web-push/subscription", pushJSON(t, subscription), "gripi_browser=approved-browser")
	if approved.Code != http.StatusNoContent {
		t.Fatalf("approved response = %d %s", approved.Code, approved.Body.String())
	}
	owner, ok := handler.app.pushOwner(requestWithCookie("gripi_browser=approved-browser"))
	if !ok {
		t.Fatal("approved browser has no push owner")
	}
	stored, err := handler.app.pushSubscriptions.List(owner)
	if err != nil || len(stored) != 1 {
		t.Fatalf("approved browser subscriptions = %#v, %v", stored, err)
	}
}

func TestWebPushRoutesScopeSubscriptionsToTheApprovedWorkspace(t *testing.T) {
	handler := newPushTestHandler(t, true, false)
	for _, workspace := range []string{"workspace-a", "workspace-b"} {
		if err := handler.app.workspaceStore.ApproveWorkspace(workspace); err != nil {
			t.Fatal(err)
		}
	}
	subscription := pushRouteSubscription("workspace")
	registered := pushRequest(handler, http.MethodPut, "/web-push/subscription", pushJSON(t, subscription), "gripi_workspace=workspace-a")
	if registered.Code != http.StatusNoContent {
		t.Fatalf("register response = %d %s", registered.Code, registered.Body.String())
	}

	otherRemoval := pushRequest(handler, http.MethodDelete, "/web-push/subscription", pushJSON(t, map[string]string{"endpoint": subscription.Endpoint}), "gripi_workspace=workspace-b")
	if otherRemoval.Code != http.StatusNoContent {
		t.Fatalf("other workspace removal = %d %s", otherRemoval.Code, otherRemoval.Body.String())
	}
	stored, err := handler.app.pushSubscriptions.List("workspace:workspace-a")
	if err != nil || len(stored) != 1 {
		t.Fatalf("workspace-a subscriptions = %#v, %v", stored, err)
	}
}

func TestWebPushRoutesRejectCrossOriginMalformedAndWrongMethodRequests(t *testing.T) {
	handler := newPushTestHandler(t, false, true)
	subscription := pushRouteSubscription("invalid")

	crossOriginRequest := httptest.NewRequest(http.MethodPut, "http://example.com/web-push/subscription", strings.NewReader(pushJSON(t, subscription)))
	crossOriginRequest.Header.Set("Content-Type", "application/json")
	crossOriginRequest.Header.Set("Origin", "https://attacker.example")
	crossOriginResponse := httptest.NewRecorder()
	handler.ServeHTTP(crossOriginResponse, crossOriginRequest)
	if crossOriginResponse.Code != http.StatusForbidden {
		t.Fatalf("cross-origin response = %d", crossOriginResponse.Code)
	}

	malformed := pushRequest(handler, http.MethodPut, "/web-push/subscription", `{"unknown":true}`, "")
	if malformed.Code != http.StatusBadRequest {
		t.Fatalf("malformed response = %d", malformed.Code)
	}
	wrongMethod := pushRequest(handler, http.MethodPost, "/web-push/subscription", "", "")
	if wrongMethod.Code != http.StatusNotFound {
		t.Fatalf("wrong method response = %d", wrongMethod.Code)
	}
}

func TestWebPushPresenceTracksTheFocusedSessionAcrossApprovedBrowsers(t *testing.T) {
	handler := newPushTestHandler(t, false, false)
	for _, token := range []string{"desktop", "phone"} {
		if _, err := handler.app.browserStore.ApproveCurrentBrowser(token, "test"); err != nil {
			t.Fatal(err)
		}
	}
	sessionPath := filepath.Join(handler.app.config.SessionsRoot, "focused.jsonl")

	focused := pushRequest(handler, http.MethodPut, "/web-push/presence", pushJSON(t, map[string]any{
		"client_id": "desktop-window", "session": sessionPath, "focused": true,
	}), "gripi_browser=desktop")
	if focused.Code != http.StatusNoContent {
		t.Fatalf("focused response = %d %s", focused.Code, focused.Body.String())
	}
	if !handler.app.notificationPresence.Focused(singleUserOwner, sessionPath) {
		t.Fatal("desktop focus was not visible to the single user")
	}

	cleared := pushRequest(handler, http.MethodPut, "/web-push/presence", pushJSON(t, map[string]any{
		"client_id": "desktop-window", "session": "", "focused": false,
	}), "gripi_browser=desktop")
	if cleared.Code != http.StatusNoContent || handler.app.notificationPresence.Focused(singleUserOwner, sessionPath) {
		t.Fatalf("cleared response = %d, focused = %t", cleared.Code, handler.app.notificationPresence.Focused(singleUserOwner, sessionPath))
	}
}

func TestWebPushPresenceIsIsolatedByWorkspace(t *testing.T) {
	handler := newPushTestHandler(t, true, false)
	for _, workspace := range []string{"workspace-a", "workspace-b"} {
		if err := handler.app.workspaceStore.ApproveWorkspace(workspace); err != nil {
			t.Fatal(err)
		}
	}
	sessionPath := filepath.Join(handler.app.config.SessionsRoot, "focused.jsonl")
	if _, err := handler.app.ownershipStore.Claim(sessionPath, "workspace-a"); err != nil {
		t.Fatal(err)
	}

	response := pushRequest(handler, http.MethodPut, "/web-push/presence", pushJSON(t, map[string]any{
		"client_id": "workspace-window", "session": sessionPath, "focused": true,
	}), "gripi_workspace=workspace-a")
	if response.Code != http.StatusNoContent {
		t.Fatalf("presence response = %d %s", response.Code, response.Body.String())
	}
	if !handler.app.notificationPresence.Focused("workspace:workspace-a", sessionPath) {
		t.Fatal("workspace focus was not tracked")
	}
	if handler.app.notificationPresence.Focused("workspace:workspace-b", sessionPath) {
		t.Fatal("workspace focus leaked to another owner")
	}

	foreign := pushRequest(handler, http.MethodPut, "/web-push/presence", pushJSON(t, map[string]any{
		"client_id": "foreign-window", "session": sessionPath, "focused": true,
	}), "gripi_workspace=workspace-b")
	if foreign.Code != http.StatusForbidden {
		t.Fatalf("foreign workspace response = %d", foreign.Code)
	}
}

func TestWebPushPresenceRejectsInvalidClientAndSessionValues(t *testing.T) {
	handler := newPushTestHandler(t, false, true)
	for _, input := range []map[string]any{
		{"client_id": "invalid client", "session": filepath.Join(handler.app.config.SessionsRoot, "session.jsonl"), "focused": true},
		{"client_id": "client", "session": filepath.Join(t.TempDir(), "outside.jsonl"), "focused": true},
		{"client_id": "client", "session": "", "focused": true},
	} {
		response := pushRequest(handler, http.MethodPut, "/web-push/presence", pushJSON(t, input), "")
		if response.Code != http.StatusBadRequest {
			t.Fatalf("invalid presence %#v response = %d", input, response.Code)
		}
	}
	wrongMethod := pushRequest(handler, http.MethodPost, "/web-push/presence", "", "")
	if wrongMethod.Code != http.StatusNotFound {
		t.Fatalf("wrong method response = %d", wrongMethod.Code)
	}
}

func TestWebPushTestDeliversOnlyToTheCurrentOwner(t *testing.T) {
	handler := newPushTestHandler(t, true, false)
	if err := handler.app.workspaceStore.ApproveWorkspace("workspace-a"); err != nil {
		t.Fatal(err)
	}
	fake := &fakePushNotifier{}
	handler.app.pushNotifier = fake

	response := pushRequest(handler, http.MethodPost, "/web-push/test", "", "gripi_workspace=workspace-a")
	if response.Code != http.StatusNoContent {
		t.Fatalf("test response = %d %s", response.Code, response.Body.String())
	}
	if fake.owner != "workspace:workspace-a" || !strings.Contains(string(fake.payload), `"type":"gripi-notification-test"`) {
		t.Fatalf("delivery = owner %q, payload %s", fake.owner, fake.payload)
	}
}

type fakePushNotifier struct {
	owner   string
	payload []byte
}

func (notifier *fakePushNotifier) Deliver(_ context.Context, owner string, payload []byte) error {
	notifier.owner = owner
	notifier.payload = append([]byte(nil), payload...)
	return nil
}

func newPushTestHandler(t *testing.T, multiUser, authDisabled bool) *Handler {
	t.Helper()
	root := t.TempDir()
	cfg := config.Config{
		Address:                "127.0.0.1:4567",
		Home:                   root,
		SessionsRoot:           root,
		BrowserAuthDisabled:    authDisabled,
		MultiUserMode:          multiUser,
		AdminPassword:          "secret",
		BrowserAccessPath:      filepath.Join(root, "browser-access.json"),
		WorkspaceSecretPath:    filepath.Join(root, "workspace-secret"),
		WorkspaceAccessPath:    filepath.Join(root, "workspace-access.json"),
		WorkspaceOwnershipPath: filepath.Join(root, "session-owners.json"),
		WebPushVAPIDPath:       filepath.Join(root, "vapid.json"),
		PushSubscriptionsPath:  filepath.Join(root, "subscriptions.json"),
		RestartPath:            filepath.Join(root, "restart-request"),
		PermittedHosts:         []string{"example.com"},
	}
	result, err := newHandler(cfg, gripi.WebFiles, randomBrowserToken)
	if err != nil {
		t.Fatal(err)
	}
	return result.(*Handler)
}

func pushRequest(handler http.Handler, method, path, body, cookie string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, "http://example.com"+path, strings.NewReader(body))
	request.Header.Set("Origin", "http://example.com")
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if cookie != "" {
		request.Header.Set("Cookie", cookie)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func requestWithCookie(cookie string) *http.Request {
	request := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	request.Header.Set("Cookie", cookie)
	return request
}

func pushRouteSubscription(suffix string) push.Subscription {
	curve := elliptic.P256()
	return push.Subscription{
		Endpoint: "https://push.example/sub/" + suffix,
		Keys: push.SubscriptionKeys{
			Auth:   base64.RawURLEncoding.EncodeToString([]byte("0123456789abcdef")),
			P256dh: base64.RawURLEncoding.EncodeToString(elliptic.Marshal(curve, curve.Params().Gx, curve.Params().Gy)),
		},
	}
}

func pushJSON(t *testing.T, value any) string {
	t.Helper()
	contents, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(contents)
}
