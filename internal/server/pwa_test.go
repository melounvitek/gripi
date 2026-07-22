package server_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandlerServesThePWAResources(t *testing.T) {
	handler := newHandler(t, testConfig(t))

	manifestResponse := performRequest(handler, http.MethodGet, "http://example.com/manifest.webmanifest", "")
	if manifestResponse.Code != http.StatusOK || manifestResponse.Header().Get("Content-Type") != "application/manifest+json" {
		t.Fatalf("manifest response = %d, %q", manifestResponse.Code, manifestResponse.Header().Get("Content-Type"))
	}
	var manifest struct {
		Name     string `json:"name"`
		StartURL string `json:"start_url"`
		Display  string `json:"display"`
		Icons    []struct {
			Source  string `json:"src"`
			Purpose string `json:"purpose"`
		} `json:"icons"`
	}
	if err := json.Unmarshal(manifestResponse.Body.Bytes(), &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Name != "Gripi" || manifest.StartURL != "/" || manifest.Display != "standalone" || len(manifest.Icons) != 2 || manifest.Icons[1].Purpose != "maskable" {
		t.Fatalf("manifest = %#v", manifest)
	}

	for _, path := range []string{"/app-icon.svg", "/app-icon-maskable.svg"} {
		response := performRequest(handler, http.MethodGet, "http://example.com"+path, "")
		if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "image/svg+xml" {
			t.Fatalf("%s response = %d, %q", path, response.Code, response.Header().Get("Content-Type"))
		}
		for _, text := range []string{`viewBox="0 0 800 800"`, `fill="#F24405"`, `fill="#F1EFE9"`} {
			if !strings.Contains(response.Body.String(), text) {
				t.Fatalf("%s does not contain %q", path, text)
			}
		}
	}

	worker := performRequest(handler, http.MethodGet, "http://example.com/service-worker.js", "")
	if worker.Code != http.StatusOK || worker.Header().Get("Content-Type") != "application/javascript;charset=utf-8" || worker.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("worker response = %d, Content-Type %q, Cache-Control %q", worker.Code, worker.Header().Get("Content-Type"), worker.Header().Get("Cache-Control"))
	}
	for _, text := range []string{"self.registration.showNotification", `["gripi-notification", "gripi-notification-test"].includes(data.type)`, "notificationclick"} {
		if !strings.Contains(worker.Body.String(), text) {
			t.Fatalf("worker does not contain %q", text)
		}
	}
}

func TestPWARoutesReturnNotFoundForWrongMethodsAndPreserveHead(t *testing.T) {
	handler := newHandler(t, testConfig(t))
	paths := []string{
		"/manifest.webmanifest",
		"/app-icon.svg",
		"/app-icon-maskable.svg",
		"/service-worker.js",
		"/notification-test",
	}
	for _, path := range paths {
		wrongMethod := performRequest(handler, http.MethodPost, "http://example.com"+path, "")
		if wrongMethod.Code != http.StatusNotFound {
			t.Fatalf("POST %s status = %d", path, wrongMethod.Code)
		}
		head := performRequest(handler, http.MethodHead, "http://example.com"+path, "")
		if head.Code != http.StatusOK {
			t.Fatalf("HEAD %s status = %d", path, head.Code)
		}
	}
}

func TestHandlerServesTheNotificationTestTemplateWithFirstTapControls(t *testing.T) {
	handler := newHandler(t, testConfig(t))
	response := performRequest(handler, http.MethodGet, "http://example.com/notification-test", "")

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	for _, text := range []string{
		"Notification test",
		"navigator.serviceWorker.register",
		"Notification.requestPermission",
		"gripiElectron",
		"worker.active.postMessage",
		`<button type="button" data-enable>`,
		`enableButton.addEventListener("click"`,
		`sendButton.addEventListener("click"`,
	} {
		if !strings.Contains(response.Body.String(), text) {
			t.Fatalf("body does not contain %q", text)
		}
	}
	if strings.Contains(response.Body.String(), "iPhone") || strings.Contains(response.Body.String(), "iOS") {
		t.Fatal("notification template contains platform-specific instructions")
	}
}

func TestStaticAssetsRemainNoCache(t *testing.T) {
	handler := newHandler(t, testConfig(t))
	for _, path := range []string{"/assets/app.css", "/assets/app.js", "/apple-touch-icon.png"} {
		request := httptest.NewRequest(http.MethodGet, "http://example.com"+path, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s status = %d", path, response.Code)
		}
		if got := response.Header().Get("Cache-Control"); got != "no-cache" {
			t.Fatalf("%s Cache-Control = %q", path, got)
		}
	}
}
