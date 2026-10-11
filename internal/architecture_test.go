package internal_test

import (
	"os/exec"
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

func forbiddenImport(from, to string) bool {
	if within(to, "internal/agent/pi") {
		return from != "cmd/gripi" && !within(from, "internal/agent/pi")
	}
	return from == "internal/agent" && (to == "internal/server" || to == "internal/sessions")
}

func within(path, root string) bool {
	return path == root || strings.HasPrefix(path, root+"/")
}
