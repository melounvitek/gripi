package internal_test

import (
	"os/exec"
	"slices"
	"strings"
	"testing"
)

const module = "github.com/melounvitek/gripi/"

// Only production imports are checked: tests may still build real Pi clients.
func TestProductionImportsKeepPiBehindTheAgentAdapter(t *testing.T) {
	command := exec.Command("go", "list", "-f", "{{.ImportPath}}{{range .Imports}} {{.}}{{end}}", module+"...")
	var stderr strings.Builder
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, stderr.String())
	}
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		packages := strings.Fields(line)
		from := strings.TrimPrefix(packages[0], module)
		for _, imported := range packages[1:] {
			if to := strings.TrimPrefix(imported, module); forbiddenImport(from, to) {
				t.Errorf("%s must not import %s", from, to)
			}
		}
	}
}

// shared lists the gateway's utility packages, which any layer may use.
var shared = []string{"internal/keyedlock", "internal/state"}

func forbiddenImport(from, to string) bool {
	if !within(to, "internal") || slices.Contains(shared, to) || from == "cmd/gripi" {
		return false
	}
	switch {
	case within(from, "internal/agent/pi"):
		// The adapter uses the core and itself, never the gateway.
		return to != "internal/agent" && !within(to, "internal/agent/pi")
	case within(from, "internal/agent"):
		// The core knows neither its adapters nor the gateway.
		return true
	default:
		// The gateway reaches agents only through the core.
		return within(to, "internal/agent/pi")
	}
}

func TestForbiddenImports(t *testing.T) {
	for _, edge := range []struct {
		from, to  string
		forbidden bool
	}{
		{"internal/server", "internal/agent/pi", true},
		{"internal/server", "internal/agent/pi/rpc", true},
		{"internal/sessions", "internal/agent/pi/sessionfile", true},
		{"internal/access", "internal/agent/pi/settings", true},
		{"internal/agent", "internal/agent/pi", true},
		{"internal/agent", "internal/sessions", true},
		{"internal/agent", "internal/access", true},
		{"internal/agent/pi", "internal/sessions", true},
		{"internal/agent/pi/rpc", "internal/server", true},
		{"cmd/gripi", "internal/agent/pi", false},
		{"internal/server", "internal/agent", false},
		{"internal/sessions", "internal/agent", false},
		{"internal/agent", "context", false},
		{"internal/agent/pi", "internal/agent/pi/rpc", false},
		{"internal/agent/pi/sessionfile", "internal/agent", false},
		{"internal/agent/pi/sessionfile", "internal/keyedlock", false},
	} {
		if got := forbiddenImport(edge.from, edge.to); got != edge.forbidden {
			t.Errorf("forbiddenImport(%q, %q) = %v, want %v", edge.from, edge.to, got, edge.forbidden)
		}
	}
}

func within(path, root string) bool {
	return path == root || strings.HasPrefix(path, root+"/")
}
