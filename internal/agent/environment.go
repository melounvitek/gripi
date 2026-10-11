package agent

import "strings"

// ScrubbedEnvironment drops the gateway's own settings, which would otherwise
// configure the projects and gateways that an agent's commands run.
func ScrubbedEnvironment(environment []string) []string {
	result := make([]string, 0, len(environment))
	for _, entry := range environment {
		key, _, _ := strings.Cut(entry, "=")
		// gripi commands in a session reach this gateway through the socket, and the browser suite's fake Pi reads GRIPI_E2E_*.
		kept := key == "GRIPI_SOCKET_PATH" || strings.HasPrefix(key, "GRIPI_E2E_")
		if key == "APP_ENV" || (strings.HasPrefix(key, "GRIPI_") && !kept) {
			continue
		}
		result = append(result, entry)
	}
	return result
}
