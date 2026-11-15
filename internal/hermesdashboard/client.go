// Package hermesdashboard reaches the speech and file endpoints of Hermes Agent's dashboard, which Hermes Desktop uses too.
package hermesdashboard

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
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

// ReadFile reads at most limit bytes from the start of a file on the dashboard's machine, and answers them with the
// file's size. The dashboard reads only regular files.
func (client *Client) ReadFile(ctx context.Context, path string, limit int64) ([]byte, int64, error) {
	header := http.Header{"Range": {fmt.Sprintf("bytes=0-%d", limit-1)}}
	response, err := client.call(ctx, http.MethodGet, "/api/files/download?path="+url.QueryEscape(path), nil, header)
	if err != nil {
		return nil, 0, err
	}
	defer response.Body.Close()
	size := response.ContentLength
	if response.StatusCode == http.StatusPartialContent {
		// Such as "bytes 0-99/216".
		_, total, _ := strings.Cut(response.Header.Get("Content-Range"), "/")
		if size, err = strconv.ParseInt(total, 10, 64); err != nil {
			return nil, 0, fmt.Errorf("the dashboard didn't say the file's size: %w", err)
		}
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, limit))
	return data, size, err
}

func (client *Client) post(ctx context.Context, path string, payload map[string]string, answer any) error {
	body, _ := json.Marshal(payload)
	response, err := client.call(ctx, http.MethodPost, path, body, jsonHeader)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if answer == nil {
		return nil
	}
	return json.NewDecoder(response.Body).Decode(answer)
}

var jsonHeader = http.Header{"Content-Type": {"application/json"}}

// call sends a request after logging in, unless an earlier login still counts, and again after logging in again when
// the dashboard's session has expired.
func (client *Client) call(ctx context.Context, method, path string, body []byte, header http.Header) (*http.Response, error) {
	if err := client.login(ctx, false); err != nil {
		return nil, err
	}
	response, err := client.send(ctx, method, path, body, header)
	var failure *statusError
	if errors.As(err, &failure) && failure.status == http.StatusUnauthorized {
		if err := client.login(ctx, true); err != nil {
			return nil, err
		}
		response, err = client.send(ctx, method, path, body, header)
	}
	return response, err
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
	response, err := client.send(ctx, http.MethodPost, "/auth/password-login", body, jsonHeader)
	if err != nil {
		return fmt.Errorf("log in: %w", err)
	}
	_ = response.Body.Close()
	client.loggedIn = true
	return nil
}

// send returns the response to a request that succeeded, whose body the caller closes.
func (client *Client) send(ctx context.Context, method, path string, body []byte, header http.Header) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, method, client.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	maps.Copy(request.Header, header)
	response, err := client.httpClient.Do(request)
	if err != nil {
		return nil, err
	}
	if response.StatusCode == http.StatusOK || response.StatusCode == http.StatusPartialContent {
		return response, nil
	}
	defer response.Body.Close()
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
	return nil, &statusError{status: response.StatusCode, message: message}
}

type statusError struct {
	status  int
	message string
}

func (err *statusError) Error() string {
	return err.message
}
