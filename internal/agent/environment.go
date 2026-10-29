package agent

import "strings"

// ScrubbedEnvironment drops the gateway's own settings, which would otherwise
// configure the projects and gateways that an agent's commands run, and the
// session of an agent that started the gateway, which would otherwise be taken
// for the session of an agent that the gateway runs.
func ScrubbedEnvironment(environment []string) []string {
	result := make([]string, 0, len(environment))
	for _, entry := range environment {
		key, _, _ := strings.Cut(entry, "=")
		// gripi commands in a session reach this gateway through the socket, and the browser suite's fake Pi reads GRIPI_E2E_*.
		kept := key == "GRIPI_SOCKET_PATH" || strings.HasPrefix(key, "GRIPI_E2E_")
		session := key == "PI_SESSION_FILE" || key == "PI_SESSION_ID" || key == "HERMES_SESSION_ID"
		if key == "APP_ENV" || session || (strings.HasPrefix(key, "GRIPI_") && !kept) {
			continue
		}
		result = append(result, entry)
	}
	return result
}
