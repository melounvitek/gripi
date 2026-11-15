package hermesdashboard_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/melounvitek/gripi/internal/hermesdashboard"
)

// fakeDashboard serves the speech and file endpoints of Hermes's dashboard to the session that its last login started.
type fakeDashboard struct {
	url       string
	mu        sync.Mutex
	logins    int
	session   string
	refuse    bool                // refuses every session, as if each had expired
	received  []map[string]string // the bodies of the speech calls
	files     map[string][]byte   // the files on the dashboard's machine, by path
	hang      bool                // answers no file request until it is cancelled
	fileCalls int
}

func startFakeDashboard(t *testing.T) (*fakeDashboard, *hermesdashboard.Client) {
	t.Helper()
	dashboard := &fakeDashboard{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /auth/password-login", func(response http.ResponseWriter, request *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(request.Body).Decode(&body)
		if body["provider"] != "basic" || body["username"] != "me" || body["password"] != "secret" {
			answer(response, http.StatusUnauthorized, map[string]string{"detail": "Invalid credentials"})
			return
		}
		dashboard.mu.Lock()
		dashboard.logins++
		dashboard.session = strings.Repeat("s", dashboard.logins)
		http.SetCookie(response, &http.Cookie{Name: "hermes_session_at", Value: dashboard.session, Path: "/", HttpOnly: true})
		dashboard.mu.Unlock()
		answer(response, http.StatusOK, map[string]any{"ok": true, "next": "/"})
	})
	mux.HandleFunc("POST /api/audio/transcribe", dashboard.api(func(map[string]string) (int, any) {
		return http.StatusOK, map[string]any{"ok": true, "transcript": "Hello there", "provider": "local"}
	}))
	mux.HandleFunc("POST /api/audio/speak", dashboard.api(func(body map[string]string) (int, any) {
		if body["text"] == "Fail" {
			return http.StatusInternalServerError, map[string]string{"detail": "Speech synthesis failed"}
		}
		audio := base64.StdEncoding.EncodeToString([]byte("spoken: " + body["text"]))
		return http.StatusOK, map[string]any{"ok": true, "data_url": "data:audio/mpeg;base64," + audio, "mime_type": "audio/mpeg", "provider": "piper"}
	}))
	// Like the dashboard, which answers a range of a file.
	mux.HandleFunc("GET /api/files/download", func(response http.ResponseWriter, request *http.Request) {
		if !dashboard.authorized(request) {
			answer(response, http.StatusUnauthorized, map[string]string{"detail": "Unauthorized"})
			return
		}
		dashboard.mu.Lock()
		contents, found := dashboard.files[request.URL.Query().Get("path")]
		dashboard.fileCalls++
		hang := dashboard.hang
		dashboard.mu.Unlock()
		if hang {
			<-request.Context().Done()
			return
		}
		if !found {
			answer(response, http.StatusNotFound, map[string]string{"detail": "Path not found"})
			return
		}
		http.ServeContent(response, request, "", time.Time{}, bytes.NewReader(contents))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	dashboard.url = server.URL
	return dashboard, hermesdashboard.New(server.URL, "me", "secret")
}

func (dashboard *fakeDashboard) api(handle func(map[string]string) (int, any)) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		if !dashboard.authorized(request) {
			answer(response, http.StatusUnauthorized, map[string]string{"error": "unauthenticated", "detail": "Unauthorized", "reason": "invalid_or_expired_session"})
			return
		}
		var body map[string]string
		_ = json.NewDecoder(request.Body).Decode(&body)
		dashboard.mu.Lock()
		dashboard.received = append(dashboard.received, body)
		dashboard.mu.Unlock()
		status, reply := handle(body)
		answer(response, status, reply)
	}
}

func (dashboard *fakeDashboard) authorized(request *http.Request) bool {
	cookie, err := request.Cookie("hermes_session_at")
	dashboard.mu.Lock()
	defer dashboard.mu.Unlock()
	return err == nil && cookie.Value == dashboard.session && !dashboard.refuse
}

func (dashboard *fakeDashboard) loginCount() int {
	dashboard.mu.Lock()
	defer dashboard.mu.Unlock()
	return dashboard.logins
}

func answer(response http.ResponseWriter, status int, body any) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(body)
}

func TestTranscribeAndSpeakShareOneLogin(t *testing.T) {
	dashboard, client := startFakeDashboard(t)

	transcript, err := client.Transcribe(context.Background(), []byte("recording"), "audio/webm;codecs=opus")
	if err != nil || transcript != "Hello there" {
		t.Fatalf("Transcribe = %q, %v", transcript, err)
	}
	audio, mimeType, err := client.Speak(context.Background(), "Hi")
	if err != nil || string(audio) != "spoken: Hi" || mimeType != "audio/mpeg" {
		t.Fatalf("Speak = %q, %q, %v", audio, mimeType, err)
	}

	if logins := dashboard.loginCount(); logins != 1 {
		t.Fatalf("logged in %d times", logins)
	}
	dashboard.mu.Lock()
	sent := dashboard.received[0]
	dashboard.mu.Unlock()
	if sent["data_url"] != "data:audio/webm;codecs=opus;base64,"+base64.StdEncoding.EncodeToString([]byte("recording")) || sent["mime_type"] != "audio/webm;codecs=opus" {
		t.Fatalf("the dashboard got the recording %v", sent)
	}
}

func TestCallsAtTheSameTimeLogInOnce(t *testing.T) {
	dashboard, client := startFakeDashboard(t)

	var group sync.WaitGroup
	for range 5 {
		group.Go(func() {
			if _, err := client.Transcribe(context.Background(), []byte("recording"), "audio/webm"); err != nil {
				t.Error(err)
			}
		})
	}
	group.Wait()

	if logins := dashboard.loginCount(); logins != 1 {
		t.Fatalf("logged in %d times", logins)
	}
}

func TestAnExpiredSessionLogsInAgain(t *testing.T) {
	dashboard, client := startFakeDashboard(t)
	if _, err := client.Transcribe(context.Background(), []byte("recording"), "audio/webm"); err != nil {
		t.Fatal(err)
	}
	dashboard.mu.Lock()
	dashboard.session = "expired"
	dashboard.mu.Unlock()

	if transcript, err := client.Transcribe(context.Background(), []byte("recording"), "audio/webm"); err != nil || transcript != "Hello there" {
		t.Fatalf("Transcribe after the session expired = %q, %v", transcript, err)
	}
	if logins := dashboard.loginCount(); logins != 2 {
		t.Fatalf("logged in %d times", logins)
	}
}

func TestACallRefusedAfterLoggingInAgainFails(t *testing.T) {
	dashboard, client := startFakeDashboard(t)
	dashboard.mu.Lock()
	dashboard.refuse = true
	dashboard.mu.Unlock()

	if _, err := client.Transcribe(context.Background(), []byte("recording"), "audio/webm"); err == nil || err.Error() != "Unauthorized" {
		t.Fatalf("Transcribe = %v", err)
	}
	if logins := dashboard.loginCount(); logins != 2 {
		t.Fatalf("logged in %d times", logins)
	}
}

func TestErrorsCarryTheDashboardsReason(t *testing.T) {
	dashboard, client := startFakeDashboard(t)
	if _, _, err := client.Speak(context.Background(), "Fail"); err == nil || err.Error() != "Speech synthesis failed" {
		t.Fatalf("Speak = %v", err)
	}

	wrongPassword := hermesdashboard.New(dashboard.url, "me", "wrong")
	if _, _, err := wrongPassword.Speak(context.Background(), "Hi"); err == nil || err.Error() != "log in: Invalid credentials" {
		t.Fatalf("Speak with a wrong password = %v", err)
	}
}

func TestReadFileReadsTheStartOfAFileOnTheDashboardsMachine(t *testing.T) {
	dashboard, client := startFakeDashboard(t)
	path := "/home/hermes/page shot.png"
	contents := bytes.Repeat([]byte("png!"), 250)
	dashboard.mu.Lock()
	dashboard.files = map[string][]byte{path: contents}
	dashboard.mu.Unlock()
	ctx := context.Background()

	if data, size, err := client.ReadFile(ctx, path, 100); err != nil || !bytes.Equal(data, contents[:100]) || size != 1000 {
		t.Fatalf("ReadFile of 100 bytes = %d bytes of a file of %d, %v", len(data), size, err)
	}
	// A limit past the file's end reads all of it, also after the session has expired.
	dashboard.mu.Lock()
	dashboard.session = "expired"
	dashboard.mu.Unlock()
	if data, size, err := client.ReadFile(ctx, path, 5000); err != nil || !bytes.Equal(data, contents) || size != 1000 {
		t.Fatalf("ReadFile of 5000 bytes = %d bytes of a file of %d, %v", len(data), size, err)
	}
	if _, _, err := client.ReadFile(ctx, "/home/hermes/gone.png", 100); err == nil || err.Error() != "Path not found" {
		t.Fatalf("ReadFile of a missing file = %v", err)
	}
}

func TestADashboardThatDoesNotAnswerPausesReadingFiles(t *testing.T) {
	dashboard, client := startFakeDashboard(t)
	dashboard.mu.Lock()
	dashboard.files, dashboard.hang = map[string][]byte{"/home/hermes/shot.png": []byte("png!")}, true
	dashboard.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, _, err := client.ReadFile(ctx, "/home/hermes/shot.png", 100); err == nil {
		t.Fatal("ReadFile from a dashboard that doesn't answer succeeded")
	}

	// Every image would otherwise wait as long, while the gateway holds Hermes's updates.
	started := time.Now()
	if _, _, err := client.ReadFile(context.Background(), "/home/hermes/shot.png", 100); err == nil || time.Since(started) > time.Second {
		t.Fatalf("ReadFile right after = %v, after %v", err, time.Since(started))
	}
	dashboard.mu.Lock()
	calls := dashboard.fileCalls
	dashboard.mu.Unlock()
	if calls != 1 {
		t.Fatalf("the dashboard got %d file requests", calls)
	}
}
