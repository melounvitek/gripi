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
