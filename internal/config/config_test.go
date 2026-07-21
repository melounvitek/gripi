package config_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/melounvitek/gripi/internal/config"
)

func TestLoadReadsGatewayEnvWithoutOverridingProcessEnvironment(t *testing.T) {
	home := t.TempDir()
	envPath := filepath.Join(home, "gateway.env")
	if err := os.WriteFile(envPath, []byte("GRIPI_ADMIN_PASSWORD='from-file'\nGRIPI_SESSIONS_ROOT=/from/file\nGRIPI_MULTI_USER_MODE=yes\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load([]string{
		"HOME=" + home,
		"GRIPI_ENV_PATH=" + envPath,
		"GRIPI_ADMIN_PASSWORD=from-process",
		"GRIPI_SESSIONS_ROOT=/from/process",
		"GRIPI_HOST=localhost",
		"GRIPI_PORT=7654",
	})
	if err != nil {
		t.Fatal(err)
	}

	if cfg.AdminPassword != "from-process" {
		t.Fatalf("AdminPassword = %q", cfg.AdminPassword)
	}
	if cfg.SessionsRoot != "/from/process" {
		t.Fatalf("SessionsRoot = %q", cfg.SessionsRoot)
	}
	if !cfg.MultiUserMode {
		t.Fatal("MultiUserMode = false")
	}
	if cfg.Address != "localhost:7654" {
		t.Fatalf("Address = %q", cfg.Address)
	}
}

func TestLoadKeepsTheFirstDuplicateValueFromTheGatewayEnv(t *testing.T) {
	home := t.TempDir()
	envPath := filepath.Join(home, "gateway.env")
	if err := os.WriteFile(envPath, []byte("GRIPI_ADMIN_PASSWORD=first\nGRIPI_ADMIN_PASSWORD=second\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load([]string{"HOME=" + home, "GRIPI_ENV_PATH=" + envPath})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AdminPassword != "first" {
		t.Fatalf("AdminPassword = %q", cfg.AdminPassword)
	}
}

func TestLoadDefaults(t *testing.T) {
	home := t.TempDir()
	cfg, err := config.Load([]string{
		"HOME=" + home,
		"GRIPI_ENV_PATH=" + filepath.Join(home, "missing"),
		"GRIPI_ADMIN_PASSWORD=secret",
	})
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Address != "127.0.0.1:4567" {
		t.Fatalf("Address = %q", cfg.Address)
	}
	if cfg.SessionsRoot != filepath.Join(home, ".pi", "agent", "sessions") {
		t.Fatalf("SessionsRoot = %q", cfg.SessionsRoot)
	}
	if cfg.AttachmentsRoot != filepath.Join(home, ".pi", "gripi", "attachments") {
		t.Fatalf("AttachmentsRoot = %q", cfg.AttachmentsRoot)
	}
	if cfg.RPCIdleTimeout != 5*time.Minute || cfg.RPCIdleSweep != 30*time.Second {
		t.Fatalf("RPC intervals = %s, %s", cfg.RPCIdleTimeout, cfg.RPCIdleSweep)
	}
	if len(cfg.PiCommand) != 2 || cfg.PiCommand[0] != "pi" || cfg.PiCommand[1] != "--approve" {
		t.Fatalf("PiCommand = %#v", cfg.PiCommand)
	}
	if !cfg.Production {
		t.Fatal("Production = false")
	}
}

func TestLoadAllowsMissingPasswordOnlyWhenBrowserAuthenticationIsDisabled(t *testing.T) {
	home := t.TempDir()
	base := []string{"HOME=" + home, "GRIPI_ENV_PATH=" + filepath.Join(home, "missing")}

	if _, err := config.Load(base); err == nil {
		t.Fatal("Load succeeded without an admin password")
	}
	cfg, err := config.Load(append(base, "GRIPI_BROWSER_AUTH_DISABLED=1"))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.BrowserAuthDisabled {
		t.Fatal("BrowserAuthDisabled = false")
	}
}

func TestLoadValidatesRuntimeConfiguration(t *testing.T) {
	home := t.TempDir()
	base := []string{
		"HOME=" + home,
		"GRIPI_ENV_PATH=" + filepath.Join(home, "missing"),
		"GRIPI_ADMIN_PASSWORD=secret",
	}
	tests := []struct {
		name string
		env  []string
	}{
		{"partial Pi runtime", []string{"GRIPI_NODE=/opt/node"}},
		{"negative idle timeout", []string{"GRIPI_RPC_IDLE_TIMEOUT_SECONDS=-1"}},
		{"overflowing idle timeout", []string{"GRIPI_RPC_IDLE_TIMEOUT_SECONDS=9223372036854775807"}},
		{"zero sweep", []string{"GRIPI_RPC_IDLE_SWEEP_SECONDS=0"}},
		{"infinite sweep", []string{"GRIPI_RPC_IDLE_SWEEP_SECONDS=Inf"}},
		{"sub-nanosecond sweep", []string{"GRIPI_RPC_IDLE_SWEEP_SECONDS=1e-100"}},
		{"rounded overflowing sweep", []string{"GRIPI_RPC_IDLE_SWEEP_SECONDS=9223372036.854776"}},
		{"overflowing sweep", []string{"GRIPI_RPC_IDLE_SWEEP_SECONDS=1e20"}},
		{"invalid port", []string{"GRIPI_PORT=70000"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := config.Load(append(base, tt.env...)); err == nil {
				t.Fatal("Load succeeded")
			}
		})
	}
}

func TestGatewayEnvCannotChangeTheRuntimeEnvironment(t *testing.T) {
	home := t.TempDir()
	envPath := filepath.Join(home, "gateway.env")
	if err := os.WriteFile(envPath, []byte("GRIPI_ADMIN_PASSWORD=secret\nGRIPI_ENV=development\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load([]string{"HOME=" + home, "GRIPI_ENV_PATH=" + envPath})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Production {
		t.Fatal("Production = false")
	}
}

func TestLoadPinsTheConfiguredPiRuntime(t *testing.T) {
	home := t.TempDir()
	cfg, err := config.Load([]string{
		"HOME=" + home,
		"GRIPI_ENV_PATH=" + filepath.Join(home, "missing"),
		"GRIPI_ADMIN_PASSWORD=secret",
		"GRIPI_NODE=/opt/node",
		"GRIPI_PI=/opt/pi",
		"GRIPI_AUTO_APPROVE_PROJECTS=0",
		"RACK_ENV=development",
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(cfg.PiCommand) != 2 || cfg.PiCommand[0] != "/opt/node" || cfg.PiCommand[1] != "/opt/pi" {
		t.Fatalf("PiCommand = %#v", cfg.PiCommand)
	}
	if cfg.Production {
		t.Fatal("Production = true")
	}
}
