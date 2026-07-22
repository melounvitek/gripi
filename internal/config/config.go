package config

import (
	"bufio"
	"errors"
	"fmt"
	"math"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Address                   string
	Environment               string
	Production                bool
	Home                      string
	EnvPath                   string
	SessionsRoot              string
	AttachmentsRoot           string
	SessionCwdsPath           string
	ReadStatePath             string
	PinnedSessionsPath        string
	BrowserAccessPath         string
	WorkspaceSecretPath       string
	WorkspaceAccessPath       string
	WorkspaceOwnershipPath    string
	RestartPath               string
	AdminPassword             string
	BrowserAuthDisabled       bool
	MultiUserMode             bool
	AutoApproveProjects       bool
	AllowInsecureRemoteHTTP   bool
	TrustProxyHeaders         bool
	PermittedHosts            []string
	ResourceMonitoringEnabled bool
	RPCDiagnosticsEnabled     bool
	RPCIdleTimeout            time.Duration
	RPCIdleSweep              time.Duration
	PiCommand                 []string
}

func Load(environ []string) (Config, error) {
	process := environmentMap(environ)
	home := process["HOME"]
	if home == "" {
		return Config{}, errors.New("HOME is required")
	}

	envPath := valueOr(process, "GRIPI_ENV_PATH", filepath.Join(home, ".config", "gripi", "env"))
	fromFile, err := readEnvFile(envPath)
	if err != nil {
		return Config{}, fmt.Errorf("read %s: %w", envPath, err)
	}
	values := fromFile
	for key, value := range process {
		values[key] = value
	}

	host := firstNonempty(process["GRIPI_BIND_HOST"], process["GRIPI_HOST"], "127.0.0.1")
	port, err := integerInRange(firstNonempty(process["GRIPI_PORT"], "4567"), 1, 65535)
	if err != nil {
		return Config{}, fmt.Errorf("GRIPI_PORT: %w", err)
	}

	idleTimeout, err := nonnegativeInteger(values, "GRIPI_RPC_IDLE_TIMEOUT_SECONDS", 300)
	if err != nil {
		return Config{}, err
	}
	idleSweep, err := positiveDurationSeconds(values, "GRIPI_RPC_IDLE_SWEEP_SECONDS", 30)
	if err != nil {
		return Config{}, err
	}
	maximumDuration := time.Duration(1<<63 - 1)
	if idleTimeout > int64(maximumDuration/time.Second) {
		return Config{}, errors.New("GRIPI_RPC_IDLE_TIMEOUT_SECONDS is too large")
	}

	nodePath := strings.TrimSpace(values["GRIPI_NODE"])
	piPath := strings.TrimSpace(values["GRIPI_PI"])
	if (nodePath == "") != (piPath == "") {
		return Config{}, errors.New("GRIPI_NODE and GRIPI_PI must be set together")
	}
	piCommand := []string{"pi"}
	if nodePath != "" {
		piCommand = []string{nodePath, piPath}
	}
	autoApprove := booleanDefault(values, "GRIPI_AUTO_APPROVE_PROJECTS", true)
	if autoApprove {
		piCommand = append(piCommand, "--approve")
	}

	browserAuthDisabled := boolean(values["GRIPI_BROWSER_AUTH_DISABLED"])
	adminPassword := values["GRIPI_ADMIN_PASSWORD"]
	if adminPassword == "" && !browserAuthDisabled {
		return Config{}, fmt.Errorf("GRIPI_ADMIN_PASSWORD is required; set it in %s or in the gateway process environment", envPath)
	}

	environment := firstNonempty(process["APP_ENV"], "production")
	cfg := Config{
		Address:                   net.JoinHostPort(strings.TrimSuffix(strings.TrimPrefix(host, "["), "]"), strconv.Itoa(port)),
		Environment:               environment,
		Production:                environment != "development" && environment != "test",
		Home:                      home,
		EnvPath:                   envPath,
		SessionsRoot:              valueOr(values, "GRIPI_SESSIONS_ROOT", filepath.Join(home, ".pi", "agent", "sessions")),
		AttachmentsRoot:           valueOr(values, "GRIPI_ATTACHMENTS_ROOT", filepath.Join(home, ".pi", "gripi", "attachments")),
		SessionCwdsPath:           valueOr(values, "GRIPI_SESSION_CWDS_PATH", filepath.Join(home, ".config", "gripi", "pinned-dirs")),
		ReadStatePath:             valueOr(values, "GRIPI_READ_STATE_PATH", filepath.Join(home, ".pi", "gripi", "read-state.json")),
		PinnedSessionsPath:        filepath.Join(home, ".pi", "gripi", "pinned-sessions.json"),
		BrowserAccessPath:         valueOr(values, "GRIPI_BROWSER_ACCESS_PATH", filepath.Join(home, ".pi", "gripi", "browser-access.json")),
		WorkspaceSecretPath:       valueOr(values, "GRIPI_WORKSPACE_SECRET_PATH", filepath.Join(home, ".pi", "gripi", "workspace-secret")),
		WorkspaceAccessPath:       valueOr(values, "GRIPI_WORKSPACE_ACCESS_PATH", filepath.Join(home, ".pi", "gripi", "workspace-access.json")),
		WorkspaceOwnershipPath:    valueOr(values, "GRIPI_WORKSPACE_OWNERSHIP_PATH", filepath.Join(home, ".pi", "gripi", "session-owners.json")),
		RestartPath:               valueOr(process, "GRIPI_RESTART_PATH", filepath.Join(home, ".pi", "gripi", "restart-request")),
		AdminPassword:             adminPassword,
		BrowserAuthDisabled:       browserAuthDisabled,
		MultiUserMode:             boolean(values["GRIPI_MULTI_USER_MODE"]),
		AutoApproveProjects:       autoApprove,
		AllowInsecureRemoteHTTP:   boolean(values["GRIPI_ALLOW_INSECURE_REMOTE_HTTP"]),
		TrustProxyHeaders:         boolean(values["GRIPI_TRUST_PROXY_HEADERS"]),
		PermittedHosts:            splitCommaSeparated(values["GRIPI_PERMITTED_HOSTS"]),
		ResourceMonitoringEnabled: boolean(values["GRIPI_RESOURCE_MONITORING"]),
		RPCDiagnosticsEnabled:     boolean(values["GRIPI_RPC_DIAGNOSTICS"]),
		RPCIdleTimeout:            time.Duration(idleTimeout) * time.Second,
		RPCIdleSweep:              idleSweep,
		PiCommand:                 piCommand,
	}
	return cfg, nil
}

func readEnvFile(path string) (map[string]string, error) {
	values := make(map[string]string)
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return values, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if len(value) >= 2 && ((value[0] == '\'' && value[len(value)-1] == '\'') || (value[0] == '"' && value[len(value)-1] == '"')) {
			value = value[1 : len(value)-1]
		}
		if _, exists := values[key]; key != "" && !exists {
			values[key] = value
		}
	}
	return values, scanner.Err()
}

func environmentMap(environ []string) map[string]string {
	values := make(map[string]string, len(environ))
	for _, entry := range environ {
		key, value, found := strings.Cut(entry, "=")
		if found {
			values[key] = value
		}
	}
	return values
}

func boolean(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func booleanDefault(values map[string]string, key string, fallback bool) bool {
	value, found := values[key]
	if !found {
		return fallback
	}
	return boolean(value)
}

func nonnegativeInteger(values map[string]string, key string, fallback int64) (int64, error) {
	value := valueOr(values, key, strconv.FormatInt(fallback, 10))
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed < 0 {
		return 0, fmt.Errorf("%s must be a non-negative integer", key)
	}
	return parsed, nil
}

func positiveDurationSeconds(values map[string]string, key string, fallback float64) (time.Duration, error) {
	value := valueOr(values, key, strconv.FormatFloat(fallback, 'f', -1, 64))
	seconds, err := strconv.ParseFloat(value, 64)
	nanoseconds := seconds * float64(time.Second)
	if err != nil || seconds <= 0 || math.IsInf(seconds, 0) || math.IsNaN(seconds) || nanoseconds < 1 || nanoseconds >= float64(1<<63) {
		return 0, fmt.Errorf("%s must be a positive duration representable in nanoseconds", key)
	}
	return time.Duration(nanoseconds), nil
}

func integerInRange(value string, minimum, maximum int) (int, error) {
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < minimum || parsed > maximum {
		return 0, fmt.Errorf("must be an integer between %d and %d", minimum, maximum)
	}
	return parsed, nil
}

func valueOr(values map[string]string, key, fallback string) string {
	if value, found := values[key]; found {
		return value
	}
	return fallback
}

func firstNonempty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func splitCommaSeparated(value string) []string {
	var values []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			values = append(values, item)
		}
	}
	return values
}
