// Package dockerproxy serves a Docker API endpoint that forwards to the real one and moves host paths between two
// folders, so that containers started from a private view of the file system bind the folders the view shows.
package dockerproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Options struct {
	Listen   string // the Unix socket to serve, made private to the user; its folder must exist
	Upstream string // the real Docker endpoint: unix:// or tcp:// without TLS
	// From and To are absolute folders: paths at or under From reach Docker under To, and Docker's answers show
	// paths under To under From again.
	From, To string
}

// Serve forwards Docker API requests on options.Listen to options.Upstream until ctx ends, and returns ctx's error.
func Serve(ctx context.Context, options Options) error {
	dial, err := dialer(options.Upstream)
	if err != nil {
		return err
	}
	if !filepath.IsAbs(options.From) || !filepath.IsAbs(options.To) {
		return fmt.Errorf("the folders to map must be absolute: %q, %q", options.From, options.To)
	}
	toDocker := mapping{filepath.Clean(options.From), filepath.Clean(options.To)}
	fromDocker := mapping{toDocker.to, toDocker.from}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(request *httputil.ProxyRequest) {
			request.Out.URL.Scheme, request.Out.URL.Host = "http", "docker"
		},
		Transport: &http.Transport{DialContext: dial, DisableCompression: true},
		ModifyResponse: func(response *http.Response) error {
			rewrite := responseRewrite(response.Request)
			if rewrite == nil || response.StatusCode != http.StatusOK {
				return nil
			}
			body, err := io.ReadAll(response.Body)
			response.Body.Close()
			if err != nil {
				return err
			}
			body = rewriteJSON(body, rewrite, fromDocker)
			response.Body = io.NopCloser(bytes.NewReader(body))
			response.ContentLength = int64(len(body))
			response.Header.Set("Content-Length", strconv.Itoa(len(body)))
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			// The docker CLI shows the message as an error response from the daemon.
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			json.NewEncoder(w).Encode(map[string]string{"message": "Gripi's Docker proxy: " + err.Error()})
		},
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rewrite := requestRewrite(r); rewrite != nil {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			body = rewriteJSON(body, rewrite, toDocker)
			r.Body = io.NopCloser(bytes.NewReader(body))
			r.ContentLength = int64(len(body))
			r.TransferEncoding = nil
		}
		proxy.ServeHTTP(w, r)
	})

	listener, err := net.Listen("unix", options.Listen)
	if err != nil {
		return err
	}
	if err := os.Chmod(options.Listen, 0600); err != nil {
		listener.Close()
		return err
	}
	// Requests share ctx, so that its end also closes attached streams, which the server no longer tracks.
	server := &http.Server{Handler: handler, BaseContext: func(net.Listener) context.Context { return ctx }}
	stop := context.AfterFunc(ctx, func() { server.Close() })
	defer stop()
	err = server.Serve(listener)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

func dialer(upstream string) (func(ctx context.Context, network, address string) (net.Conn, error), error) {
	var network, address string
	if path, ok := strings.CutPrefix(upstream, "unix://"); ok {
		network, address = "unix", path
	} else if parsed, err := url.Parse(upstream); err == nil && parsed.Scheme == "tcp" && parsed.Host != "" {
		network, address = "tcp", parsed.Host
	} else {
		return nil, fmt.Errorf("unsupported Docker endpoint %q: only unix:// and tcp:// without TLS work", upstream)
	}
	var dialer net.Dialer
	return func(ctx context.Context, _, _ string) (net.Conn, error) {
		return dialer.DialContext(ctx, network, address)
	}, nil
}

// Upstream returns the Docker endpoint that the docker CLI uses with env: DOCKER_HOST, else the current context's
// endpoint, else the default socket.
func Upstream(env []string) string {
	var host string
	for _, variable := range env {
		if value, ok := strings.CutPrefix(variable, "DOCKER_HOST="); ok {
			host = value
		}
	}
	if host != "" {
		return host
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "docker", "context", "inspect", "--format", "{{.Endpoints.docker.Host}}")
	command.Env = env
	if output, err := command.Output(); err == nil && len(bytes.TrimSpace(output)) > 0 {
		return string(bytes.TrimSpace(output))
	}
	return "unix:///var/run/docker.sock"
}
