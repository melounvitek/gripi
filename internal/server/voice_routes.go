package server

import (
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
)

// voiceRecordingBytes is the most that Hermes's dashboard transcribes.
const voiceRecordingBytes = 25 << 20

func (app *application) registerVoiceRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /voice/transcribe", app.transcribeVoice)
	mux.HandleFunc("POST /voice/speak", app.speakVoice)
}

func (app *application) transcribeVoice(response http.ResponseWriter, request *http.Request) {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || (!strings.HasPrefix(mediaType, "audio/") && mediaType != "video/webm") {
		writeJSONStatus(response, http.StatusUnsupportedMediaType, map[string]string{"error": "The recording must be audio"})
		return
	}
	audio, err := io.ReadAll(http.MaxBytesReader(response, request.Body, voiceRecordingBytes))
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeJSONStatus(response, http.StatusRequestEntityTooLarge, map[string]string{"error": "The recording is too long"})
		return
	}
	if err != nil || len(audio) == 0 {
		writeJSONStatus(response, http.StatusBadRequest, map[string]string{"error": "The recording is empty"})
		return
	}
	transcript, err := app.voice.Transcribe(request.Context(), audio, mediaType)
	if err != nil {
		writeDashboardError(response, request, "transcribe speech with Hermes's dashboard", err)
		return
	}
	writeJSON(response, map[string]string{"transcript": transcript})
}

func (app *application) speakVoice(response http.ResponseWriter, request *http.Request) {
	if !parseForm(response, request) {
		return
	}
	text := strings.TrimSpace(request.FormValue("text"))
	if text == "" {
		writeJSONStatus(response, http.StatusBadRequest, map[string]string{"error": "There is no text to speak"})
		return
	}
	audio, mimeType, err := app.voice.Speak(request.Context(), text)
	if err != nil {
		writeDashboardError(response, request, "synthesize speech with Hermes's dashboard", err)
		return
	}
	response.Header().Set("Content-Type", mimeType)
	response.Header().Set("Cache-Control", "no-store")
	_, _ = response.Write(audio)
}

func writeDashboardError(response http.ResponseWriter, request *http.Request, operation string, err error) {
	// The browser cancels a request it no longer needs, such as speech that the user talked over.
	if request.Context().Err() != nil {
		return
	}
	logInternalError(operation, err)
	// The log names the dashboard's URL, which the browser has no use for.
	reason := err
	var failed *url.Error
	if errors.As(err, &failed) {
		reason = failed.Err
	}
	writeJSONStatus(response, http.StatusBadGateway, map[string]string{"error": "Hermes's dashboard failed: " + reason.Error()})
}
