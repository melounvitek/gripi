package worker

import (
	"os"
	"slices"
	"strings"

	"github.com/melounvitek/gripi/internal/agent"
)

// Variables that describe this machine rather than what Pi needs, which the worker has its own of: the user and
// their shell, the terminal, the desktop session, agents for SSH and GPG, the systemd service that runs Gripi, shell
// state of mise, Pi's own paths here and Gripi's socket.
var (
	machineVariables = []string{
		"HOME", "PATH", "USER", "LOGNAME", "USERNAME", "SHELL", "MAIL", "PWD", "OLDPWD", "SHLVL", "_", "HOSTNAME", "TMPDIR",
		"TERM", "COLORTERM", "COLUMNS", "LINES", "TMUX", "TMUX_PANE", "STY", "WINDOWID", "VTE_VERSION",
		"DISPLAY", "WAYLAND_DISPLAY", "XAUTHORITY", "XMODIFIERS", "DESKTOP_SESSION", "GDMSESSION", "SESSION_MANAGER",
		"GPG_AGENT_INFO", "GPG_TTY",
		"INVOCATION_ID", "JOURNAL_STREAM", "MANAGERPID", "MANAGERPIDFDID", "NOTIFY_SOCKET", "SYSTEMD_EXEC_PID",
		"LISTEN_PID", "LISTEN_FDS", "LISTEN_FDNAMES", "WATCHDOG_PID", "WATCHDOG_USEC", "MEMORY_PRESSURE_WATCH", "MEMORY_PRESSURE_WRITE",
		"MISE_SHELL", "PI_CODING_AGENT_DIR", "PI_PACKAGE_DIR", "GRIPI_SOCKET_PATH",
	}
	machinePrefixes = []string{
		"XDG_", "SSH_", "DBUS_", "TERM_", "TERMINFO", "KITTY_", "WEZTERM_", "ALACRITTY_", "KONSOLE_", "GHOSTTY_", "ITERM_",
		"GNOME_", "KDE_", "QT_", "GTK_", "__MISE_",
	}
)

// Environment returns the environment of a session's Pi on the worker, for Session.Env: what Pi gets here, which is
// Gripi's scrubbed environment with the session's variables on top, except what describes this machine, plus the
// worker's PATH behind the install folder's bin, and main's agent folder.
func (installation *Installation) Environment(variables []string) []string {
	var environment []string
	for _, variable := range append(agent.ScrubbedEnvironment(os.Environ()), variables...) {
		name, _, _ := strings.Cut(variable, "=")
		machine := slices.Contains(machineVariables, name) || slices.ContainsFunc(machinePrefixes, func(prefix string) bool { return strings.HasPrefix(name, prefix) })
		if !machine {
			environment = append(environment, variable)
		}
	}
	return append(environment, "PATH="+installation.dir+"/bin:"+installation.path, "PI_CODING_AGENT_DIR="+installation.agentDir)
}
