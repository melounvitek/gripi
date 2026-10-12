package hermesdashboard_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/melounvitek/gripi/internal/hermesdashboard"
)

// fakeDashboard serves the speech endpoints of Hermes's dashboard to the session that its last login started.
type fakeDashboard struct {
	url      string
	mu       sync.Mutex
	logins   int
	session  string
	refuse   bool                // refuses every session, as if each had expired
	received []map[string]string // the bodies of the speech calls
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
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	dashboard.url = server.URL
	return dashboard, hermesdashboard.New(server.URL, "me", "secret")
}

func (dashboard *fakeDashboard) api(handle func(map[string]string) (int, any)) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		cookie, err := request.Cookie("hermes_session_at")
		dashboard.mu.Lock()
		valid := err == nil && cookie.Value == dashboard.session && !dashboard.refuse
		dashboard.mu.Unlock()
		if !valid {
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
