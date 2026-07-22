package server

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
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

	(&application{unknownBodySpools: make(chan struct{}, unknownBodySpoolLimit)}).limitRequestBody(next).ServeHTTP(response, request)

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

func TestUnknownLengthBodyGatePreservesNonHijackingOverloadResponse(t *testing.T) {
	for _, protocol := range []struct {
		name       string
		major      int
		minor      int
		identifier string
	}{
		{"HTTP/1.1 recorder", 1, 1, "HTTP/1.1"},
		{"HTTP/2 fallback", 2, 0, "HTTP/2.0"},
	} {
		t.Run(protocol.name, func(t *testing.T) {
			spools := make(chan struct{}, unknownBodySpoolLimit)
			for range unknownBodySpoolLimit {
				spools <- struct{}{}
			}
			body := &trackedReadCloser{Reader: bytes.NewReader([]byte("incomplete"))}
			request := httptest.NewRequest(http.MethodPost, "http://example.com/upload", nil)
			request.ContentLength = -1
			request.TransferEncoding = []string{"chunked"}
			request.Body = body
			request.ProtoMajor = protocol.major
			request.ProtoMinor = protocol.minor
			request.Proto = protocol.identifier
			response := httptest.NewRecorder()

			(&application{unknownBodySpools: spools}).limitRequestBody(http.NotFoundHandler()).ServeHTTP(response, request)

			if response.Code != http.StatusServiceUnavailable || response.Body.String() != unknownBodyOverloadBody {
				t.Fatalf("response = %d %q", response.Code, response.Body.String())
			}
			expectedConnection := ""
			expectedBodyClosed := true
			if protocol.major == 1 {
				expectedConnection = "close"
				expectedBodyClosed = false
			}
			if response.Header().Get("Connection") != expectedConnection || !request.Close || body.closed != expectedBodyClosed {
				t.Fatalf("close state = header %q, request %v, body %v", response.Header().Get("Connection"), request.Close, body.closed)
			}
		})
	}
}

func TestHTTP2UnknownLengthOverloadRespondsAndCancelsTheRequestStream(t *testing.T) {
	spools := make(chan struct{}, unknownBodySpoolLimit)
	for range unknownBodySpoolLimit {
		spools <- struct{}{}
	}
	body := &stalledReadCloser{closed: make(chan struct{})}
	server := httptest.NewUnstartedServer((&application{unknownBodySpools: spools}).limitRequestBody(http.NotFoundHandler()))
	server.EnableHTTP2 = true
	server.Config.ReadTimeout = 3 * time.Second
	server.StartTLS()
	defer server.Close()
	defer body.Close()

	request, err := http.NewRequest(http.MethodPost, server.URL+"/upload", body)
	if err != nil {
		t.Fatal(err)
	}
	request.ContentLength = -1
	started := time.Now()
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	contents, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if response.ProtoMajor != 2 || response.StatusCode != http.StatusServiceUnavailable || string(contents) != unknownBodyOverloadBody {
		t.Fatalf("response = %s %d %q", response.Proto, response.StatusCode, contents)
	}
	if response.Header.Get("Connection") != "" {
		t.Fatalf("HTTP/2 response contains Connection header %q", response.Header.Get("Connection"))
	}
	if elapsed := time.Since(started); elapsed >= time.Second {
		t.Fatalf("HTTP/2 overload response took %s", elapsed)
	}
	select {
	case <-body.closed:
	case <-time.After(time.Second):
		t.Fatal("HTTP/2 request stream was not cancelled")
	}
}

func TestKnownLength64MiBBodyStreamsWithoutUsingUnknownLengthSpoolSlots(t *testing.T) {
	spools := make(chan struct{}, unknownBodySpoolLimit)
	for range unknownBodySpoolLimit {
		spools <- struct{}{}
	}
	app := &application{unknownBodySpools: spools}
	request := httptest.NewRequest(http.MethodPost, "http://example.com/upload", nil)
	request.ContentLength = maxRequestBodyBytes
	request.Body = io.NopCloser(io.LimitReader(zeroReader{}, maxRequestBodyBytes))
	response := httptest.NewRecorder()
	app.limitRequestBody(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		written, err := io.Copy(io.Discard, request.Body)
		if err != nil {
			t.Fatal(err)
		}
		if written != maxRequestBodyBytes {
			t.Fatalf("body bytes = %d", written)
		}
		response.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d", response.Code)
	}
}

func TestRawServerReadDeadlineRemovesIncompleteBodySpool(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("TMPDIR", tempDir)
	app := &application{unknownBodySpools: make(chan struct{}, unknownBodySpoolLimit)}
	address, stop := startRawBodyServer(t, app.limitRequestBody(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusNoContent)
	})), 300*time.Millisecond)
	defer stop()

	connection := incompleteChunkedRequest(t, address)
	defer connection.Close()
	waitForSpoolCount(t, tempDir, 1)
	if err := connection.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(connection)
	waitForSpoolCount(t, tempDir, 0)
}

func TestRawServerRejectsIncompleteUnknownBodyImmediatelyWhenSpoolsAreFull(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("TMPDIR", tempDir)
	app := &application{unknownBodySpools: make(chan struct{}, unknownBodySpoolLimit)}
	var activeRequests atomic.Int32
	limited := app.limitRequestBody(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusNoContent)
	}))
	handler := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		activeRequests.Add(1)
		defer activeRequests.Add(-1)
		limited.ServeHTTP(response, request)
	})
	const readTimeout = 3 * time.Second
	address, stop := startRawBodyServer(t, handler, readTimeout)
	defer stop()

	held := make([]net.Conn, 0, unknownBodySpoolLimit)
	for range unknownBodySpoolLimit {
		held = append(held, incompleteChunkedRequest(t, address))
	}
	waitForSpoolCount(t, tempDir, unknownBodySpoolLimit)
	waitForActiveRequestCount(t, &activeRequests, unknownBodySpoolLimit)

	connection := incompleteChunkedRequest(t, address)
	defer connection.Close()
	started := time.Now()
	if err := connection.SetReadDeadline(time.Now().Add(750 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(connection)
	response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodPost})
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusServiceUnavailable || string(body) != unknownBodyOverloadBody || !response.Close {
		t.Fatalf("response = %d, close %v, body %q", response.StatusCode, response.Close, body)
	}
	if elapsed := time.Since(started); elapsed >= readTimeout/3 {
		t.Fatalf("overload response took %s with %s read timeout", elapsed, readTimeout)
	}
	if _, err := reader.ReadByte(); !errors.Is(err, io.EOF) {
		t.Fatalf("connection remained open: %v", err)
	}
	waitForActiveRequestCount(t, &activeRequests, unknownBodySpoolLimit)
	waitForSpoolCount(t, tempDir, unknownBodySpoolLimit)

	for _, connection := range held {
		connection.Close()
	}
	waitForActiveRequestCount(t, &activeRequests, 0)
	waitForSpoolCount(t, tempDir, 0)
}

func TestRawServerAcceptsLegitimateUnknownLengthMultipartForm(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("TMPDIR", tempDir)
	app := &application{unknownBodySpools: make(chan struct{}, unknownBodySpoolLimit)}
	handler := app.limitRequestBody(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if !parseForm(response, request) {
			return
		}
		if request.FormValue("code") != "VPN-UPLOAD" {
			http.Error(response, "wrong form", http.StatusBadRequest)
			return
		}
		response.WriteHeader(http.StatusNoContent)
	}))
	address, stop := startRawBodyServer(t, handler, time.Second)
	defer stop()

	var contents bytes.Buffer
	writer := multipart.NewWriter(&contents)
	if err := writer.WriteField("code", "VPN-UPLOAD"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	connection, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	fmt.Fprintf(connection, "POST /upload HTTP/1.1\r\nHost: example.test\r\nTransfer-Encoding: chunked\r\nContent-Type: %s\r\n\r\n%x\r\n", writer.FormDataContentType(), contents.Len())
	if _, err := connection.Write(contents.Bytes()); err != nil {
		t.Fatal(err)
	}
	fmt.Fprint(connection, "\r\n0\r\n\r\n")
	response, err := http.ReadResponse(bufio.NewReader(connection), &http.Request{Method: http.MethodPost})
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d", response.StatusCode)
	}
	waitForSpoolCount(t, tempDir, 0)
}

func startRawBodyServer(t *testing.T, handler http.Handler, readTimeout time.Duration) (string, func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler, ReadTimeout: readTimeout}
	done := make(chan struct{})
	go func() {
		_ = server.Serve(listener)
		close(done)
	}()
	return listener.Addr().String(), func() {
		server.Close()
		<-done
	}
}

func incompleteChunkedRequest(t *testing.T, address string) net.Conn {
	t.Helper()
	connection, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprint(connection, "POST /upload HTTP/1.1\r\nHost: example.test\r\nTransfer-Encoding: chunked\r\n\r\n5\r\nx")
	return connection
}

func waitForActiveRequestCount(t *testing.T, active *atomic.Int32, expected int32) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for active.Load() != expected {
		if time.Now().After(deadline) {
			t.Fatalf("active requests = %d, expected %d", active.Load(), expected)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func waitForSpoolCount(t *testing.T, directory string, expected int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		matches, err := filepath.Glob(filepath.Join(directory, "gripi-request-body-*"))
		if err != nil {
			t.Fatal(err)
		}
		if len(matches) == expected {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("spool files = %#v, expected %d", matches, expected)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

type zeroReader struct{}

func (zeroReader) Read(buffer []byte) (int, error) {
	clear(buffer)
	return len(buffer), nil
}

type stalledReadCloser struct {
	closed chan struct{}
	once   sync.Once
}

func (body *stalledReadCloser) Read([]byte) (int, error) {
	<-body.closed
	return 0, io.EOF
}

func (body *stalledReadCloser) Close() error {
	body.once.Do(func() { close(body.closed) })
	return nil
}

type trackedReadCloser struct {
	io.Reader
	closed bool
}

func (body *trackedReadCloser) Close() error {
	body.closed = true
	return nil
}
