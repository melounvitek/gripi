package agent

import (
	"slices"
	"testing"
)

func TestScrubbedEnvironmentRemovesGatewaySettingsAndPreservesRuntimeVariables(t *testing.T) {
	environment := ScrubbedEnvironment([]string{"PATH=/bin", "GRIPI_ADMIN_PASSWORD=secret", "APP_ENV=production", "GRIPI_PORT=4567", "TOOL_RUNTIME_PATH=kept", "GRIPI_SOCKET_PATH=/kept.sock", "GRIPI_E2E_SESSIONS_ROOT=kept"})
	expected := []string{"PATH=/bin", "TOOL_RUNTIME_PATH=kept", "GRIPI_SOCKET_PATH=/kept.sock", "GRIPI_E2E_SESSIONS_ROOT=kept"}
	if !slices.Equal(environment, expected) {
		t.Fatalf("environment = %q, want %q", environment, expected)
	}
}
