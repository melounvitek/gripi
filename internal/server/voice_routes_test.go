package server_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gripi "github.com/melounvitek/gripi"
	"github.com/melounvitek/gripi/internal/config"
	gateway "github.com/melounvitek/gripi/internal/server"
)

// voiceHandler runs the gateway with the fake Hermes and voice through a fake of Hermes's dashboard, which fails every
// speech call with its detail when fail is set. It returns the gateway and the speech calls that the dashboard got.
func voiceHandler(t *testing.T, fail bool) (http.Handler, *[]map[string]string) {
	t.Helper()
	var received []map[string]string
	mux := http.NewServeMux()
	mux.HandleFunc("POST /auth/password-login", func(response http.ResponseWriter, _ *http.Request) {
		http.SetCookie(response, &http.Cookie{Name: "hermes_session_at", Value: "session", Path: "/"})
		_, _ = io.WriteString(response, `{"ok":true,"next":"/"}`)
	})
	speech := func(answer func(map[string]string) string) http.HandlerFunc {
		return func(response http.ResponseWriter, request *http.Request) {
			var body map[string]string
			_ = json.NewDecoder(request.Body).Decode(&body)
			received = append(received, body)
			if fail {
				response.WriteHeader(http.StatusInternalServerError)
				_, _ = io.WriteString(response, `{"detail":"No speech provider is configured"}`)
				return
			}
			_, _ = io.WriteString(response, answer(body))
		}
	}
	mux.HandleFunc("POST /api/audio/transcribe", speech(func(map[string]string) string {
		return `{"ok":true,"transcript":"List the files","provider":"local"}`
	}))
	mux.HandleFunc("POST /api/audio/speak", speech(func(body map[string]string) string {
		audio := base64.StdEncoding.EncodeToString([]byte("spoken: " + body["text"]))
		return `{"ok":true,"data_url":"data:audio/mpeg;base64,` + audio + `","mime_type":"audio/mpeg","provider":"piper"}`
	}))
	dashboard := httptest.NewServer(mux)
	t.Cleanup(dashboard.Close)

	cfg := hermesConfig(t)
	cfg.HermesDashboardURL, cfg.HermesDashboardUsername, cfg.HermesDashboardPassword = dashboard.URL, "me", "secret"
	handler, err := gateway.NewHandlerWithHermes(cfg, gripi.WebFiles)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = handler.(interface{ Close(context.Context) error }).Close(context.Background()) })
	return handler, &received
}

func postVoice(handler http.Handler, target, contentType, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("Origin", "http://example.com")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func TestVoiceRoutesExistOnlyWhenVoiceIsOn(t *testing.T) {
	handler := hermesHandler(t)
	if response := postVoice(handler, "/voice/transcribe", "audio/webm", "recording"); response.Code != http.StatusNotFound {
		t.Fatalf("transcribe without voice = %d", response.Code)
	}
	if response := postVoice(handler, "/voice/speak", "application/x-www-form-urlencoded", "text=Hello"); response.Code != http.StatusNotFound {
		t.Fatalf("speak without voice = %d", response.Code)
	}
}

func TestOnlyHermesSessionsOfferAVoiceConversationWhenVoiceIsOn(t *testing.T) {
	cfg := hermesConfig(t, map[string]any{"sessionId": "hermes-chat", "cwd": t.TempDir(), "title": "Hermes chat", "updatedAt": "2026-03-01T10:00:00Z"})
	piSession := filepath.Join(cfg.SessionsRoot, "pi.jsonl")
	if err := os.MkdirAll(cfg.SessionsRoot, 0700); err != nil {
		t.Fatal(err)
	}
	records := `{"type":"session","version":3,"id":"pi-chat","timestamp":"2026-02-01T10:00:00Z","cwd":"` + cfg.Home + `"}` + "\n" +
		`{"type":"message","id":"u1","parentId":null,"timestamp":"2026-02-01T10:00:01Z","message":{"role":"user","content":[{"type":"text","text":"Pi chat"}]}}` + "\n"
	if err := os.WriteFile(piSession, []byte(records), 0600); err != nil {
		t.Fatal(err)
	}
	page := func(cfg config.Config, session string) string {
		handler, err := gateway.NewHandlerWithHermes(cfg, gripi.WebFiles)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = handler.(interface{ Close(context.Context) error }).Close(context.Background()) })
		return getPage(t, handler, "/?"+url.Values{"session": {session}, "session_only": {"1"}}.Encode())
	}
	const button = `aria-label="Start a voice conversation"`

	if strings.Contains(page(cfg, "hermes:hermes-chat"), button) {
		t.Error("a Hermes session offers voice while it is off")
	}
	cfg.HermesDashboardURL, cfg.HermesDashboardUsername, cfg.HermesDashboardPassword = "http://127.0.0.1:8787", "me", "secret"
	if hermes := page(cfg, "hermes:hermes-chat"); !strings.Contains(hermes, button) || !strings.Contains(hermes, `aria-label="Voice conversation with Hermes" data-voice-call hidden`) {
		t.Error("a Hermes session lacks the voice button or the hidden call line")
	}
	if strings.Contains(page(cfg, piSession), button) {
		t.Error("a Pi session offers voice")
	}
}

func TestTranscribeSendsTheRecordingToHermessDashboard(t *testing.T) {
	handler, received := voiceHandler(t, false)

	response := postVoice(handler, "/voice/transcribe", "audio/webm;codecs=opus", "recording")
	if response.Code != http.StatusOK || strings.TrimSpace(response.Body.String()) != `{"transcript":"List the files"}` {
		t.Fatalf("transcribe = %d %s", response.Code, response.Body.String())
	}
	if sent := (*received)[0]; sent["data_url"] != "data:audio/webm;base64,"+base64.StdEncoding.EncodeToString([]byte("recording")) || sent["mime_type"] != "audio/webm" {
		t.Fatalf("the dashboard got %v", sent)
	}
}

func TestTranscribeRefusesWhatIsNotARecording(t *testing.T) {
	handler, received := voiceHandler(t, false)

	if response := postVoice(handler, "/voice/transcribe", "text/plain", "recording"); response.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("transcribe of text = %d", response.Code)
	}
	if response := postVoice(handler, "/voice/transcribe", "audio/webm", ""); response.Code != http.StatusBadRequest {
		t.Fatalf("transcribe of nothing = %d", response.Code)
	}
	if len(*received) != 0 {
		t.Fatalf("the dashboard got %v", *received)
	}
}

func TestSpeakAnswersTheAudioOfHermessDashboard(t *testing.T) {
	handler, received := voiceHandler(t, false)

	response := postVoice(handler, "/voice/speak", "application/x-www-form-urlencoded", url.Values{"text": {"Two files."}}.Encode())
	if response.Code != http.StatusOK || response.Body.String() != "spoken: Two files." {
		t.Fatalf("speak = %d %s", response.Code, response.Body.String())
	}
	if response.Header().Get("Content-Type") != "audio/mpeg" || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("speak headers = %v", response.Header())
	}
	if (*received)[0]["text"] != "Two files." {
		t.Fatalf("the dashboard got %v", *received)
	}
}

func TestVoiceAnswersBadGatewayWithTheDashboardsReason(t *testing.T) {
	handler, _ := voiceHandler(t, true)

	for _, response := range []*httptest.ResponseRecorder{
		postVoice(handler, "/voice/transcribe", "audio/webm", "recording"),
		postVoice(handler, "/voice/speak", "application/x-www-form-urlencoded", "text=Hello"),
	} {
		var payload map[string]string
		_ = json.Unmarshal(response.Body.Bytes(), &payload)
		if response.Code != http.StatusBadGateway || payload["error"] != "Hermes's dashboard failed: No speech provider is configured" {
			t.Fatalf("failed speech call = %d %s", response.Code, response.Body.String())
		}
	}
}
