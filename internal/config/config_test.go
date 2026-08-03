package config_test

import (
	"os"
	"path/filepath"
	"strings"
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
		"GRIPI_PINNED_SESSIONS_PATH=/isolated/pinned.json",
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
	if cfg.PinnedSessionsPath != "/isolated/pinned.json" {
		t.Fatalf("PinnedSessionsPath = %q", cfg.PinnedSessionsPath)
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
	if cfg.PiAgentDir != filepath.Join(home, ".pi", "agent") {
		t.Fatalf("PiAgentDir = %q", cfg.PiAgentDir)
	}
	if cfg.AttachmentsRoot != filepath.Join(home, ".pi", "gripi", "attachments") {
		t.Fatalf("AttachmentsRoot = %q", cfg.AttachmentsRoot)
	}
	if cfg.WebPushVAPIDPath != filepath.Join(home, ".pi", "gripi", "web-push-vapid.json") {
		t.Fatalf("WebPushVAPIDPath = %q", cfg.WebPushVAPIDPath)
	}
	if cfg.PushSubscriptionsPath != filepath.Join(home, ".pi", "gripi", "push-subscriptions.json") {
		t.Fatalf("PushSubscriptionsPath = %q", cfg.PushSubscriptionsPath)
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

func TestLoadUsesConfiguredWebPushStatePaths(t *testing.T) {
	home := t.TempDir()
	cfg, err := config.Load([]string{
		"HOME=" + home,
		"GRIPI_ENV_PATH=" + filepath.Join(home, "missing"),
		"GRIPI_ADMIN_PASSWORD=secret",
		"GRIPI_WEB_PUSH_VAPID_PATH=/state/vapid.json",
		"GRIPI_PUSH_SUBSCRIPTIONS_PATH=/state/subscriptions.json",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.WebPushVAPIDPath != "/state/vapid.json" || cfg.PushSubscriptionsPath != "/state/subscriptions.json" {
		t.Fatalf("Web Push paths = %q, %q", cfg.WebPushVAPIDPath, cfg.PushSubscriptionsPath)
	}
}

func TestLoadUsesThePiAgentDirectoryFromTheProcessEnvironment(t *testing.T) {
	home := t.TempDir()
	custom := filepath.Join(home, "custom-agent")
	cfg, err := config.Load([]string{
		"HOME=" + home,
		"PI_CODING_AGENT_DIR=" + custom,
		"GRIPI_ENV_PATH=" + filepath.Join(home, "missing"),
		"GRIPI_ADMIN_PASSWORD=secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PiAgentDir != custom {
		t.Fatalf("PiAgentDir = %q", cfg.PiAgentDir)
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

func TestLoadAcceptsDocumentedBooleanValues(t *testing.T) {
	home := t.TempDir()
	base := []string{
		"HOME=" + home,
		"GRIPI_ENV_PATH=" + filepath.Join(home, "missing"),
		"GRIPI_ADMIN_PASSWORD=secret",
	}
	for _, test := range []struct {
		value    string
		expected bool
	}{
		{"1", true}, {"true", true}, {"yes", true}, {"on", true}, {" YeS ", true},
		{"", false}, {"0", false}, {"false", false}, {"no", false}, {"off", false}, {" OFF ", false},
	} {
		t.Run(test.value, func(t *testing.T) {
			cfg, err := config.Load(append(base, "GRIPI_MULTI_USER_MODE="+test.value))
			if err != nil {
				t.Fatal(err)
			}
			if cfg.MultiUserMode != test.expected {
				t.Fatalf("MultiUserMode = %v", cfg.MultiUserMode)
			}
		})
	}
	cfg, err := config.Load(append(base, "GRIPI_AUTO_APPROVE_PROJECTS="))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AutoApproveProjects || len(cfg.PiCommand) != 1 {
		t.Fatalf("blank auto approve = %v, command = %#v", cfg.AutoApproveProjects, cfg.PiCommand)
	}
}

func TestLoadAppliesEveryBooleanSettingAndDefault(t *testing.T) {
	home := t.TempDir()
	base := []string{
		"HOME=" + home,
		"GRIPI_ENV_PATH=" + filepath.Join(home, "missing"),
		"GRIPI_ADMIN_PASSWORD=secret",
	}
	tests := []struct {
		key      string
		fallback bool
		value    func(config.Config) bool
	}{
		{"GRIPI_AUTO_APPROVE_PROJECTS", true, func(cfg config.Config) bool { return cfg.AutoApproveProjects }},
		{"GRIPI_BROWSER_AUTH_DISABLED", false, func(cfg config.Config) bool { return cfg.BrowserAuthDisabled }},
		{"GRIPI_MULTI_USER_MODE", false, func(cfg config.Config) bool { return cfg.MultiUserMode }},
		{"GRIPI_ALLOW_INSECURE_REMOTE_HTTP", false, func(cfg config.Config) bool { return cfg.AllowInsecureRemoteHTTP }},
		{"GRIPI_TRUST_PROXY_HEADERS", false, func(cfg config.Config) bool { return cfg.TrustProxyHeaders }},
		{"GRIPI_RESOURCE_MONITORING", false, func(cfg config.Config) bool { return cfg.ResourceMonitoringEnabled }},
		{"GRIPI_RPC_DIAGNOSTICS", false, func(cfg config.Config) bool { return cfg.RPCDiagnosticsEnabled }},
	}
	for _, test := range tests {
		t.Run(test.key, func(t *testing.T) {
			defaults, err := config.Load(base)
			if err != nil {
				t.Fatal(err)
			}
			if actual := test.value(defaults); actual != test.fallback {
				t.Fatalf("default = %v", actual)
			}
			enabled, err := config.Load(append(base, test.key+"=1"))
			if err != nil {
				t.Fatal(err)
			}
			if !test.value(enabled) {
				t.Fatal("true value was not applied")
			}
			disabled, err := config.Load(append(base, test.key+"=0"))
			if err != nil {
				t.Fatal(err)
			}
			if test.value(disabled) {
				t.Fatal("false value was not applied")
			}
		})
	}
}

func TestLoadRejectsMalformedBooleanValues(t *testing.T) {
	home := t.TempDir()
	base := []string{
		"HOME=" + home,
		"GRIPI_ENV_PATH=" + filepath.Join(home, "missing"),
		"GRIPI_ADMIN_PASSWORD=secret",
	}
	for _, key := range []string{
		"GRIPI_AUTO_APPROVE_PROJECTS",
		"GRIPI_BROWSER_AUTH_DISABLED",
		"GRIPI_MULTI_USER_MODE",
		"GRIPI_ALLOW_INSECURE_REMOTE_HTTP",
		"GRIPI_TRUST_PROXY_HEADERS",
		"GRIPI_RESOURCE_MONITORING",
		"GRIPI_RPC_DIAGNOSTICS",
	} {
		t.Run(key, func(t *testing.T) {
			if _, err := config.Load(append(base, key+"=sometimes")); err == nil || !strings.Contains(err.Error(), key) {
				t.Fatalf("Load error = %v", err)
			}
		})
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
		"APP_ENV=development",
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
