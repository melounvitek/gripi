package server

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	gripi "github.com/melounvitek/gripi"
	"github.com/melounvitek/gripi/internal/config"
)

func TestBrowserTokenEntropyErrorsReturnInternalServerError(t *testing.T) {
	cfg := config.Config{
		Address:             "127.0.0.1:4567",
		AdminPassword:       "secret",
		BrowserAccessPath:   t.TempDir() + "/browser-access.json",
		PermittedHosts:      []string{"example.com"},
		BrowserAuthDisabled: false,
	}
	handler, err := newHandler(cfg, gripi.WebFiles, func() (string, error) {
		return "", errors.New("entropy unavailable")
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusInternalServerError || response.Body.String() != "Internal Server Error" {
		t.Fatalf("response = %d, %q", response.Code, response.Body.String())
	}
	if got := response.Header().Values("Set-Cookie"); len(got) != 0 {
		t.Fatalf("Set-Cookie = %#v", got)
	}
}
