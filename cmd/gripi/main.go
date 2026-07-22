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
	if len(os.Args) > 1 && os.Args[1] == "password" {
		if err := ensurePassword(); err != nil {
			log.Fatal(err)
		}
		return
	}
	cfg, err := config.Load(os.Environ())
	if err != nil {
		log.Fatal(err)
	}
	handler, err := gateway.NewHandler(cfg, gripi.WebFiles)
	if err != nil {
		log.Fatal(err)
	}

	server := newHTTPServer(handler)
	listener, err := net.Listen("tcp", cfg.Address)
	if err != nil {
		log.Fatal(err)
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
