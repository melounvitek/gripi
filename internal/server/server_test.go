package server_test

import (
	"bytes"
	"compress/gzip"
	"io"
	"io/fs"
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
	if got := response.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q", got)
	}
}

func TestHandlerPacksTextAssetsForClientsThatAcceptGzip(t *testing.T) {
	handler, err := server.NewHandler(config.Config{}, gripi.WebFiles)
	if err != nil {
		t.Fatal(err)
	}
	gateway := httptest.NewServer(handler)
	defer gateway.Close()
	// Without this the client would ask for gzip and unpack the response on its own.
	client := &http.Client{Transport: &http.Transport{DisableCompression: true}}
	defer client.CloseIdleConnections()
	fetch := func(method, path string, headers map[string]string) (*http.Response, []byte) {
		t.Helper()
		request, err := http.NewRequest(method, gateway.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		for name, value := range headers {
			request.Header.Set(name, value)
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		return response, body
	}
	gzipAccepted := map[string]string{"Accept-Encoding": "gzip, deflate, br"}

	for _, path := range []string{"/assets/app.css", "/assets/app.js", "/assets/vendor/xterm/xterm.mjs"} {
		file, err := fs.ReadFile(gripi.WebFiles, "public"+path)
		if err != nil {
			t.Fatal(err)
		}
		plain, plainBody := fetch(http.MethodGet, path, nil)
		if plain.StatusCode != http.StatusOK || plain.Header.Get("Content-Encoding") != "" || !bytes.Equal(plainBody, file) {
			t.Fatalf("%s without gzip = %d, Content-Encoding = %q, %d bytes", path, plain.StatusCode, plain.Header.Get("Content-Encoding"), len(plainBody))
		}

		packed, packedBody := fetch(http.MethodGet, path, gzipAccepted)
		if packed.StatusCode != http.StatusOK || packed.Header.Get("Content-Encoding") != "gzip" || packed.Header.Get("Vary") != "Accept-Encoding" {
			t.Fatalf("%s = %d, Content-Encoding = %q, Vary = %q", path, packed.StatusCode, packed.Header.Get("Content-Encoding"), packed.Header.Get("Vary"))
		}
		if packed.Header.Get("Cache-Control") != "no-store" || packed.Header.Get("Content-Type") != plain.Header.Get("Content-Type") {
			t.Fatalf("%s Cache-Control = %q, Content-Type = %q", path, packed.Header.Get("Cache-Control"), packed.Header.Get("Content-Type"))
		}
		if packed.ContentLength != int64(len(packedBody)) || len(packedBody) >= len(file) {
			t.Fatalf("%s Content-Length = %d, body = %d bytes, file = %d bytes", path, packed.ContentLength, len(packedBody), len(file))
		}
		unpacker, err := gzip.NewReader(bytes.NewReader(packedBody))
		if err != nil {
			t.Fatal(err)
		}
		if unpacked, err := io.ReadAll(unpacker); err != nil || !bytes.Equal(unpacked, file) {
			t.Fatalf("%s unpacks to %d bytes (%v), file = %d bytes", path, len(unpacked), err, len(file))
		}

		head, _ := fetch(http.MethodHead, path, gzipAccepted)
		if head.StatusCode != http.StatusOK || head.Header.Get("Content-Encoding") != "gzip" || head.ContentLength != packed.ContentLength {
			t.Fatalf("HEAD %s = %d, Content-Encoding = %q, Content-Length = %d", path, head.StatusCode, head.Header.Get("Content-Encoding"), head.ContentLength)
		}

		part, partBody := fetch(http.MethodGet, path, map[string]string{"Accept-Encoding": "gzip", "Range": "bytes=10-109"})
		if part.StatusCode != http.StatusPartialContent || part.Header.Get("Content-Encoding") != "" || !bytes.Equal(partBody, file[10:110]) {
			t.Fatalf("%s range = %d, Content-Encoding = %q, body = %q", path, part.StatusCode, part.Header.Get("Content-Encoding"), partBody)
		}
	}

	for _, path := range []string{"/assets/notification-badge.png", "/apple-touch-icon.png"} {
		file, err := fs.ReadFile(gripi.WebFiles, "public"+path)
		if err != nil {
			t.Fatal(err)
		}
		image, imageBody := fetch(http.MethodGet, path, gzipAccepted)
		if image.StatusCode != http.StatusOK || image.Header.Get("Content-Encoding") != "" || !bytes.Equal(imageBody, file) {
			t.Fatalf("%s = %d, Content-Encoding = %q, %d bytes", path, image.StatusCode, image.Header.Get("Content-Encoding"), len(imageBody))
		}
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
	root := t.TempDir()
	handler, err := server.NewHandler(config.Config{MultiUserMode: true, BrowserAuthDisabled: true, WorkspaceSecretPath: root + "/secret", WorkspaceAccessPath: root + "/access.json", WorkspaceOwnershipPath: root + "/owners.json"}, gripi.WebFiles)
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
		if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "User token") {
			t.Fatalf("%s = %d %s", target, response.Code, response.Body.String())
		}
	}
	for _, target := range []string{"/assets/app.css", "/manifest.webmanifest", "/app-icon.svg", "/app-icon-maskable.svg", "/service-worker.js"} {
		asset := httptest.NewRecorder()
		handler.ServeHTTP(asset, httptest.NewRequest(http.MethodGet, "http://app.test"+target, nil))
		if asset.Code != http.StatusOK || strings.Contains(asset.Body.String(), "User token") {
			t.Fatalf("%s = %d %s", target, asset.Code, asset.Body.String())
		}
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
