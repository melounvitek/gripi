package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	gripi "github.com/melounvitek/gripi"
	"github.com/melounvitek/gripi/internal/config"
	gateway "github.com/melounvitek/gripi/internal/server"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func serve() error {
	cfg, err := config.Load(os.Environ())
	if err != nil {
		return err
	}
	handler, err := gateway.NewHandler(cfg, gripi.WebFiles)
	if err != nil {
		return err
	}

	server := newHTTPServer(handler)
	listener, err := net.Listen("tcp", cfg.Address)
	if err != nil {
		return err
	}
	var localServer *http.Server
	if local, ok := handler.(interface{ Local() http.Handler }); ok && local.Local() != nil {
		if localServer, err = startLocalServer(local.Local(), cfg.SocketPath); err != nil {
			log.Printf("gripi commands are unavailable: %v", err)
		}
	}
	_, missingPi := exec.LookPath(cfg.PiCommand[0])
	if missingPi == nil && len(cfg.PiCommand) > 1 && !strings.HasPrefix(cfg.PiCommand[1], "-") {
		// With GRIPI_NODE and GRIPI_PI, Pi's script follows the Node executable.
		_, missingPi = os.Stat(cfg.PiCommand[1])
	}
	if missingPi != nil {
		log.Printf("Warning: Pi was not found (%v). Sessions cannot start until Pi is installed (https://pi.dev/).", missingPi)
	}

	shutdownSignal, stopSignals := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stopSignals()
	serveErrors := make(chan error, 1)
	go func() {
		log.Printf("Gripi listening on %s", cfg.Address)
		serveErrors <- server.Serve(listener)
	}()

	exitCode := 0
	select {
	case err := <-serveErrors:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("gateway server: %v", err)
			exitCode = 1
		}
	case <-shutdownSignal.Done():
		if err := listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			log.Printf("close gateway listener: %v", err)
		}
		// Both listeners stop before either drains: a request accepted
		// during a drain would lose its Pi process moments later.
		if localServer != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := localServer.Shutdown(ctx); err != nil {
				log.Printf("gripi command socket shutdown: %v", err)
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			log.Printf("gateway shutdown: %v", err)
		}
		if err := <-serveErrors; err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
			log.Printf("gateway server: %v", err)
		}
	}
	if closer, ok := handler.(interface{ Close(context.Context) error }); ok {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := closer.Close(ctx); err != nil {
			log.Printf("close RPC clients: %v", err)
			exitCode = 1
		}
	}
	if exitCode != 0 {
		os.Exit(exitCode)
	}
	return nil
}

const (
	// Ten minutes keeps the 64 MiB upload contract usable down to roughly 110 KiB/s on VPN links.
	serverReadTimeout = 10 * time.Minute
	serverIdleTimeout = 2 * time.Minute
)

func newHTTPServer(handler http.Handler) *http.Server {
	return &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       serverReadTimeout,
		IdleTimeout:       serverIdleTimeout,
	}
}

// startLocalServer serves gripi commands on a socket that only the gateway's
// user can open, which stands in for browser authentication.
func startLocalServer(handler http.Handler, path string) (*http.Server, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("%s exists and is not a socket", path)
		}
		connection, err := net.DialTimeout("unix", path, time.Second)
		if err == nil {
			connection.Close()
			return nil, fmt.Errorf("another gateway is listening on %s", path)
		}
		// Only a refused connection proves that no gateway is listening.
		if !errors.Is(err, syscall.ECONNREFUSED) {
			return nil, err
		}
		if err := os.Remove(path); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	previous := syscall.Umask(0177)
	listener, err := net.Listen("unix", path)
	syscall.Umask(previous)
	if err != nil {
		return nil, err
	}
	server := newHTTPServer(handler)
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("gripi command socket: %v", err)
		}
	}()
	return server, nil
}

func ensurePassword() error {
	home := os.Getenv("HOME")
	if home == "" {
		return errors.New("HOME is required")
	}
	configDirectory := os.Getenv("GRIPI_CONFIG_DIR")
	if configDirectory == "" {
		configDirectory = filepath.Join(home, ".config", "gripi")
	}
	path := os.Getenv("GRIPI_ENV_PATH")
	if path == "" {
		path = filepath.Join(configDirectory, "env")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	unlock, err := lockPasswordFile(path)
	if err != nil {
		return err
	}
	defer unlock()

	contents, err := os.ReadFile(path)
	missing := errors.Is(err, fs.ErrNotExist)
	if err != nil && !missing {
		return err
	}
	if !missing {
		if err := os.Chmod(path, 0600); err != nil {
			return err
		}
	}
	for _, line := range strings.Split(string(contents), "\n") {
		password, found := configuredPassword(line)
		if !found {
			continue
		}
		if password == "" {
			return fmt.Errorf("GRIPI_ADMIN_PASSWORD is empty in %s; remove the line or set a password", path)
		}
		return nil
	}

	value := make([]byte, 12)
	if _, err := rand.Read(value); err != nil {
		return err
	}
	password := hex.EncodeToString(value)
	addition := ""
	if len(contents) > 0 && contents[len(contents)-1] != '\n' {
		addition = "\n"
	}
	addition += "GRIPI_ADMIN_PASSWORD=" + password + "\n"
	if err := writePasswordFile(path, contents, addition); err != nil {
		return err
	}
	if err := os.Chmod(path, 0600); err != nil {
		return err
	}
	fmt.Printf("Generated GRIPI_ADMIN_PASSWORD in %s\nAdmin password: %s\nYou should change it by editing %s\n", path, password, path)
	return nil
}

func configuredPassword(line string) (string, bool) {
	key, value, found := strings.Cut(strings.TrimSpace(line), "=")
	if !found || strings.TrimSpace(key) != "GRIPI_ADMIN_PASSWORD" {
		return "", false
	}
	value = strings.TrimSpace(value)
	if len(value) >= 2 && ((value[0] == '\'' && value[len(value)-1] == '\'') || (value[0] == '"' && value[len(value)-1] == '"')) {
		value = value[1 : len(value)-1]
	}
	return value, true
}

func writePasswordFile(path string, contents []byte, addition string) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".gripi-env-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(contents); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.WriteString(addition); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return replacePasswordFile(temporaryPath, path)
}
