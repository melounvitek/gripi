package server_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	gripi "github.com/melounvitek/gripi"
	"github.com/melounvitek/gripi/internal/config"
	"github.com/melounvitek/gripi/internal/server"
)

func TestHandlerServesEmbeddedFrontendAssets(t *testing.T) {
	handler, err := server.NewHandler(config.Config{}, gripi.WebFiles)
	if err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodGet, "http://app.test/assets/app.css", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	if !strings.Contains(response.Body.String(), "--") {
		t.Fatal("response does not contain the application stylesheet")
	}
	if got := response.Header().Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("Cache-Control = %q", got)
	}
}

func TestHandlerDoesNotListAssetDirectories(t *testing.T) {
	handler, err := server.NewHandler(config.Config{}, gripi.WebFiles)
	if err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodGet, "http://app.test/assets/", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d", response.Code)
	}
}

func TestMultiUserModeFailsClosedForApplicationRoutesButServesStaticAssets(t *testing.T) {
	handler, err := server.NewHandler(config.Config{MultiUserMode: true}, gripi.WebFiles)
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"/", "/prompt", "/events?session=%2Ftmp%2Fsession"} {
		method := http.MethodGet
		if target == "/prompt" {
			method = http.MethodPost
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(method, "http://app.test"+target, nil))
		if response.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s = %d %s", target, response.Code, response.Body.String())
		}
	}
	asset := httptest.NewRecorder()
	handler.ServeHTTP(asset, httptest.NewRequest(http.MethodGet, "http://app.test/assets/app.css", nil))
	if asset.Code != http.StatusOK {
		t.Fatalf("asset = %d %s", asset.Code, asset.Body.String())
	}
}

func TestHandlerDoesNotTreatUnknownPathsAsStaticFiles(t *testing.T) {
	handler, err := server.NewHandler(config.Config{}, gripi.WebFiles)
	if err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodGet, "http://app.test/missing", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d", response.Code)
	}
}
