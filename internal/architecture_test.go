package internal_test

import (
	"maps"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

const module = "github.com/melounvitek/gripi/"

type importEdge struct{ from, to string }

// allowedImports lists the violations that remain until Pi is fully behind the agent adapter.
// The coordinator removes entries as steps land, and the list must end empty.
// Entries that are no longer needed don't fail the test.
var allowedImports = map[importEdge]bool{
	{"internal/sessions", "internal/agent/pi/rpc"}: true,
}

// Only production imports are checked: tests may still build real Pi clients.
func TestProductionImportsKeepPiBehindTheAgentAdapter(t *testing.T) {
	command := exec.Command("go", "list", "-f", "{{.ImportPath}}{{range .Imports}} {{.}}{{end}}", module+"...")
	var stderr strings.Builder
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, stderr.String())
	}
	unused := maps.Clone(allowedImports)
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		packages := strings.Fields(line)
		from := strings.TrimPrefix(packages[0], module)
		for _, imported := range packages[1:] {
			edge := importEdge{from, strings.TrimPrefix(imported, module)}
			if !forbiddenImport(edge) {
				continue
			}
			delete(unused, edge)
			if !allowedImports[edge] {
				t.Errorf("%s must not import %s", edge.from, edge.to)
			}
		}
	}
	for edge := range unused {
		t.Logf("allowed import %s -> %s is no longer used", edge.from, edge.to)
	}
}

func forbiddenImport(edge importEdge) bool {
	if within(edge.to, "internal/agent/pi") {
		return edge.from != "cmd/gripi" && !within(edge.from, "internal/agent/pi")
	}
	switch edge.from {
	case "internal/agent":
		return slices.Contains([]string{"internal/server", "internal/sessions", "internal/rpc", "internal/pi"}, edge.to)
	case "internal/server", "internal/sessions", "internal/access":
		// internal/server may import internal/sessions until the Pi session reader moves into the adapter.
		return edge.to == "internal/rpc" || edge.to == "internal/pi"
	}
	return false
}

func within(path, root string) bool {
	return path == root || strings.HasPrefix(path, root+"/")
}
