package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/melounvitek/gripi/internal/push"
)

const (
	pushRequestBytes = 8 << 10
	singleUserOwner  = "single-user"
)

type pushNotifier interface {
	Deliver(context.Context, string, []byte) error
}

func (app *application) registerPushRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /web-push/config", app.webPushConfig)
	mux.HandleFunc("/web-push/config", http.NotFound)
	mux.HandleFunc("PUT /web-push/subscription", app.upsertPushSubscription)
	mux.HandleFunc("DELETE /web-push/subscription", app.removePushSubscription)
	mux.HandleFunc("/web-push/subscription", http.NotFound)
	mux.HandleFunc("POST /web-push/test", app.testPushNotification)
	mux.HandleFunc("/web-push/test", http.NotFound)
}

func (app *application) webPushConfig(response http.ResponseWriter, _ *http.Request) {
	keys, err := app.pushIdentity.Keys()
	if err != nil {
		writeInternalError(response, "load Web Push identity", err)
		return
	}
	response.Header().Set("Cache-Control", "no-store")
	writeJSON(response, map[string]string{"public_key": keys.PublicKey})
}

func (app *application) upsertPushSubscription(response http.ResponseWriter, request *http.Request) {
	var input struct {
		Endpoint       string                `json:"endpoint"`
		ExpirationTime json.RawMessage       `json:"expirationTime"`
		Keys           push.SubscriptionKeys `json:"keys"`
	}
	if !decodePushJSON(response, request, &input) {
		return
	}
	subscription := push.Subscription{Endpoint: input.Endpoint, Keys: input.Keys}
	owner, ok := app.pushOwner(request)
	if !ok {
		writeText(response, http.StatusForbidden, "Forbidden")
		return
	}
	if err := app.pushSubscriptions.Upsert(owner, subscription); err != nil {
		if errors.Is(err, push.ErrInvalidSubscription) || errors.Is(err, push.ErrSubscriptionLimit) {
			writeText(response, http.StatusBadRequest, err.Error())
			return
		}
		writeInternalError(response, "store Web Push subscription", err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (app *application) removePushSubscription(response http.ResponseWriter, request *http.Request) {
	var input struct {
		Endpoint string `json:"endpoint"`
	}
	if !decodePushJSON(response, request, &input) {
		return
	}
	owner, ok := app.pushOwner(request)
	if !ok {
		writeText(response, http.StatusForbidden, "Forbidden")
		return
	}
	if _, err := app.pushSubscriptions.Remove(owner, input.Endpoint); err != nil {
		if errors.Is(err, push.ErrInvalidSubscription) {
			writeText(response, http.StatusBadRequest, err.Error())
			return
		}
		writeInternalError(response, "remove Web Push subscription", err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (app *application) testPushNotification(response http.ResponseWriter, request *http.Request) {
	owner, ok := app.pushOwner(request)
	if !ok {
		writeText(response, http.StatusForbidden, "Forbidden")
		return
	}
	payload, err := json.Marshal(map[string]string{
		"type":  "gripi-notification-test",
		"title": "Gripi",
		"body":  "If you can see this, Web Push notifications are working.",
		"tag":   "gripi-notification-test",
		"url":   "/notification-test",
	})
	if err != nil {
		writeInternalError(response, "encode Web Push test", err)
		return
	}
	if err := app.pushNotifier.Deliver(request.Context(), owner, payload); err != nil {
		writeInternalError(response, "send Web Push test", err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (app *application) pushOwner(request *http.Request) (string, bool) {
	if app.config.MultiUserMode {
		owner := currentWorkspaceID(request)
		return "workspace:" + owner, owner != ""
	}
	if !app.browserAccessEnabled() {
		return singleUserOwner, true
	}
	token, ok := submittedBrowserToken(request)
	if !ok {
		return "", false
	}
	digest := sha256.Sum256([]byte(token))
	return "browser:" + hex.EncodeToString(digest[:]), true
}

func decodePushJSON(response http.ResponseWriter, request *http.Request, target any) bool {
	reader := http.MaxBytesReader(response, request.Body, pushRequestBytes)
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeText(response, http.StatusBadRequest, "Invalid Web Push request")
		return false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeText(response, http.StatusBadRequest, "Invalid Web Push request")
		return false
	}
	return true
}
