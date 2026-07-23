package rpc

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

type ClientFactory func(string) (RPCClient, error)

type SessionClientMover interface {
	WithClientMove(context.Context, string, bool, func(RPCClient) (string, error), func(string, string) (func() error, error), func(string, string)) (string, error)
}

func StartNewSession(ctx context.Context, cwd, sessionsRoot string, factory ClientFactory, clients *Registry, pending *PendingSessionRegistry, prepare func(string) (string, func() error, error)) (string, error) {
	client, err := factory(cwd)
	if err != nil {
		return "", err
	}
	registered := false
	defer func() {
		if !registered {
			_ = client.Close()
		}
	}()
	state, err := client.GetState(ctx)
	if err != nil {
		return "", err
	}
	path := sessionFileFromResponse(state)
	if path == "" {
		path = filepath.Join(sessionsRoot, "pending-"+randomSessionID()+".jsonl")
	}
	var rollback func() error
	if prepare != nil {
		path, rollback, err = prepare(path)
		if err != nil {
			return "", err
		}
	}
	if err := clients.Register(path, client); err != nil {
		if rollback != nil {
			return "", errors.Join(err, rollback())
		}
		return "", err
	}
	registered = true
	if _, err := os.Stat(path); err != nil {
		pending.Remember(path, cwd)
	}
	return path, nil
}

func BranchSession(ctx context.Context, previous, cwd string, clients SessionClientMover, pending *PendingSessionRegistry, switchSession func(RPCClient) (map[string]any, error), normalize func(string) (string, error), prepare func(string, string) (func() error, error)) (string, map[string]any, error) {
	_, wasPending := pending.CWD(previous)
	var actionResponse map[string]any
	path, err := clients.WithClientMove(ctx, previous, true, func(client RPCClient) (string, error) {
		var err error
		actionResponse, err = switchSession(client)
		if err != nil || responseCancelled(actionResponse) || actionResponse["success"] != true {
			return previous, err
		}
		state, err := client.GetState(ctx)
		if err != nil {
			return "", err
		}
		path := sessionFileFromResponse(state)
		if path == "" {
			return "", errors.New("Pi did not report the switched session path")
		}
		if normalize != nil {
			path, err = normalize(path)
			if err != nil {
				return "", err
			}
		}
		if path == previous {
			return "", errors.New("Pi did not report the switched session path")
		}
		return path, nil
	}, prepare, func(from, to string) {
		if wasPending {
			pending.Remap(from, to)
		}
		if _, statErr := os.Stat(to); statErr != nil {
			pending.Remember(to, cwd)
		}
	})
	return path, actionResponse, err
}

func responseCancelled(response map[string]any) bool {
	data := response
	if nested, ok := response["data"].(map[string]any); ok {
		data = nested
	}
	return data["cancelled"] == true
}

type StopResult struct {
	Forced   bool
	Stopping bool
}

func StopResultFor(err error) (StopResult, error) {
	if err == nil {
		return StopResult{}, nil
	}
	if errors.Is(err, ErrInterruptPending) || errors.Is(err, ErrClientRetiring) || errors.Is(err, ErrClientStarting) {
		return StopResult{Stopping: true}, nil
	}
	var timeout *RequestTimeoutError
	if errors.As(err, &timeout) || errors.Is(err, ErrProcessExited) || errors.Is(err, io.ErrClosedPipe) {
		return StopResult{Forced: true}, nil
	}
	return StopResult{}, err
}

func sessionFileFromResponse(response map[string]any) string {
	data := response
	if nested, ok := response["data"].(map[string]any); ok {
		data = nested
	}
	for _, key := range []string{"sessionFile", "session_file", "path"} {
		if value, ok := data[key].(string); ok && value != "" {
			return value
		}
	}
	return ""
}

func randomSessionID() string {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", value[0:4], value[4:6], value[6:8], value[8:10], value[10:16])
}
