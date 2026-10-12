// Package hermesdashboard reaches the speech endpoints of Hermes Agent's dashboard, which Hermes Desktop uses too.
package hermesdashboard

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"sync"
	"time"
)

// Client logs in with the dashboard's password login and keeps its session cookies, which the dashboard refreshes.
type Client struct {
	baseURL, username, password string
	httpClient                  *http.Client
	loginMu                     sync.Mutex
	loggedIn                    bool
}

func New(baseURL, username, password string) *Client {
	jar, _ := cookiejar.New(nil)
	return &Client{
		baseURL: strings.TrimSuffix(baseURL, "/"), username: username, password: password,
		// Transcribing a long recording on a CPU takes seconds, and the first call loads the model.
		httpClient: &http.Client{Jar: jar, Timeout: time.Minute},
	}
}

// Transcribe answers what the recording says, which is empty when it has no speech.
func (client *Client) Transcribe(ctx context.Context, audio []byte, mimeType string) (string, error) {
	var answer struct {
		Transcript string `json:"transcript"`
	}
	err := client.post(ctx, "/api/audio/transcribe", map[string]string{
		"data_url":  "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(audio),
		"mime_type": mimeType,
	}, &answer)
	return answer.Transcript, err
}

// Speak answers the text spoken, as audio of the MIME type.
func (client *Client) Speak(ctx context.Context, text string) ([]byte, string, error) {
	var answer struct {
		DataURL  string `json:"data_url"`
		MimeType string `json:"mime_type"`
	}
	if err := client.post(ctx, "/api/audio/speak", map[string]string{"text": text}, &answer); err != nil {
		return nil, "", err
	}
	header, data, found := strings.Cut(answer.DataURL, ",")
	if !found || !strings.HasSuffix(header, ";base64") {
		return nil, "", errors.New("the dashboard's speech is not a base64 data URL")
	}
	audio, err := base64.StdEncoding.DecodeString(data)
	return audio, answer.MimeType, err
}

func (client *Client) post(ctx context.Context, path string, payload map[string]string, answer any) error {
	body, _ := json.Marshal(payload)
	if err := client.login(ctx, false); err != nil {
		return err
	}
	err := client.send(ctx, path, body, answer)
	var failure *statusError
	if errors.As(err, &failure) && failure.status == http.StatusUnauthorized {
		// The dashboard's session has expired.
		if err := client.login(ctx, true); err != nil {
			return err
		}
		err = client.send(ctx, path, body, answer)
	}
	return err
}

// login logs in unless an earlier login still counts, or always when forced.
func (client *Client) login(ctx context.Context, force bool) error {
	client.loginMu.Lock()
	defer client.loginMu.Unlock()
	if client.loggedIn && !force {
		return nil
	}
	client.loggedIn = false
	body, _ := json.Marshal(map[string]string{"provider": "basic", "username": client.username, "password": client.password})
	if err := client.send(ctx, "/auth/password-login", body, nil); err != nil {
		return fmt.Errorf("log in: %w", err)
	}
	client.loggedIn = true
	return nil
}

func (client *Client) send(ctx context.Context, path string, body []byte, answer any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, client.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.httpClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		var failure struct {
			Detail any `json:"detail"`
			Error  any `json:"error"`
		}
		_ = json.NewDecoder(response.Body).Decode(&failure)
		// FastAPI's detail is a list for an invalid request, and only a text is worth showing.
		message, _ := failure.Detail.(string)
		if message == "" {
			message, _ = failure.Error.(string)
		}
		if message == "" {
			message = response.Status
		}
		return &statusError{status: response.StatusCode, message: message}
	}
	if answer == nil {
		return nil
	}
	return json.NewDecoder(response.Body).Decode(answer)
}

type statusError struct {
	status  int
	message string
}

func (err *statusError) Error() string {
	return err.message
}
