package server_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func localHandler(t *testing.T, handler http.Handler) http.Handler {
	t.Helper()
	return handler.(interface{ Local() http.Handler }).Local()
}

func TestLocalHandlerServesNoBrowserRoutes(t *testing.T) {
	local := localHandler(t, newHandler(t, testConfig(t)))
	for _, target := range []string{"/", "/sidebar", "/assets/app.css", "/browser-access/status", "/service-worker.js"} {
		response := httptest.NewRecorder()
		local.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://gripi"+target, nil))
		if response.Code != http.StatusNotFound {
			t.Fatalf("%s = %d %s", target, response.Code, response.Body.String())
		}
	}
}

func TestLocalHandlerIsUnavailableInMultiUserMode(t *testing.T) {
	cfg := testConfig(t)
	cfg.MultiUserMode = true
	if local := localHandler(t, newHandler(t, cfg)); local != nil {
		t.Fatal("multi-user gateway offers a handler that bypasses workspace access")
	}
}
