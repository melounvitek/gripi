package server

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestUnknownLengthBodyGatePreservesMultipartAndRemovesItsSpoolFile(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("TMPDIR", tempDir)
	var contents bytes.Buffer
	writer := multipart.NewWriter(&contents)
	if err := writer.WriteField("code", "ABCD-1234"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	body := &trackedReadCloser{Reader: bytes.NewReader(contents.Bytes())}
	request := httptest.NewRequest(http.MethodPost, "http://example.com/upload", nil)
	request.Body = body
	request.ContentLength = -1
	request.TransferEncoding = []string{"chunked"}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response := httptest.NewRecorder()
	var spoolPath string
	next := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		matches, err := filepath.Glob(filepath.Join(tempDir, "gripi-request-body-*"))
		if err != nil {
			t.Fatal(err)
		}
		if len(matches) != 1 {
			t.Fatalf("spool files while handling request = %#v", matches)
		}
		spoolPath = matches[0]
		if err := request.ParseMultipartForm(1 << 20); err != nil {
			t.Fatal(err)
		}
		if got := request.FormValue("code"); got != "ABCD-1234" {
			t.Fatalf("code = %q", got)
		}
		response.WriteHeader(http.StatusNoContent)
	})

	(&application{}).limitRequestBody(next).ServeHTTP(response, request)

	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body = %q", response.Code, response.Body.String())
	}
	if !body.closed {
		t.Fatal("original request body was not closed")
	}
	if _, err := os.Stat(spoolPath); !os.IsNotExist(err) {
		t.Fatalf("spool file exists or stat failed: %v", err)
	}
}

type trackedReadCloser struct {
	io.Reader
	closed bool
}

func (body *trackedReadCloser) Close() error {
	body.closed = true
	return nil
}
