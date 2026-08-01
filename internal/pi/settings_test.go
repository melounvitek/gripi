package pi

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDisplaySettingsUseGlobalAndApprovedProjectOverrides(t *testing.T) {
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	project := filepath.Join(root, "project")
	writeJSON(t, filepath.Join(agentDir, "settings.json"), `{"hideThinkingBlock":true}`)
	writeJSON(t, filepath.Join(project, ".pi", "settings.json"), `{"hideThinkingBlock":false}`)

	global := (SettingsResolver{AgentDir: agentDir}).DisplaySettings(filepath.Join(root, "other"))
	if !global.HideThinkingBlock {
		t.Fatalf("global settings = %#v", global)
	}

	projectOverride := (SettingsResolver{AgentDir: agentDir, AutoApproveProjects: true}).DisplaySettings(project)
	if projectOverride.HideThinkingBlock {
		t.Fatalf("project settings = %#v", projectOverride)
	}
}

func TestDisplaySettingsUseNativeProjectTrustFallbacks(t *testing.T) {
	tests := []struct {
		name        string
		global      string
		trust       string
		autoApprove bool
		wantHidden  bool
	}{
		{"automatic approval", `{}`, `{}`, true, true},
		{"saved parent trust", `{}`, `{"PROJECT_PARENT":true}`, false, true},
		{"saved project rejection overrides parent trust", `{}`, `{"PROJECT_PARENT":true,"PROJECT":false}`, false, false},
		{"default always", `{"defaultProjectTrust":"always"}`, `{}`, false, true},
		{"default ask is untrusted in RPC", `{"defaultProjectTrust":"ask"}`, `{}`, false, false},
		{"default never", `{"defaultProjectTrust":"never"}`, `{}`, false, false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			agentDir := filepath.Join(root, "agent")
			projectParent := filepath.Join(root, "projects")
			project := filepath.Join(projectParent, "app")
			writeJSON(t, filepath.Join(agentDir, "settings.json"), test.global)
			trust := replaceTrustPaths(test.trust, projectParent, project)
			writeJSON(t, filepath.Join(agentDir, "trust.json"), trust)
			writeJSON(t, filepath.Join(project, ".pi", "settings.json"), `{"hideThinkingBlock":true}`)

			settings := (SettingsResolver{AgentDir: agentDir, AutoApproveProjects: test.autoApprove}).DisplaySettings(project)
			if settings.HideThinkingBlock != test.wantHidden {
				t.Fatalf("settings = %#v, want hidden %t", settings, test.wantHidden)
			}
		})
	}
}

func TestDisplaySettingsIgnoreMissingMalformedAndInvalidFiles(t *testing.T) {
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	project := filepath.Join(root, "project")
	writeJSON(t, filepath.Join(agentDir, "settings.json"), `{"hideThinkingBlock":`)
	writeJSON(t, filepath.Join(agentDir, "trust.json"), `[]`)
	writeJSON(t, filepath.Join(project, ".pi", "settings.json"), `{"hideThinkingBlock":"yes"}`)

	settings := (SettingsResolver{AgentDir: agentDir}).DisplaySettings(project)
	if settings.HideThinkingBlock {
		t.Fatalf("settings = %#v", settings)
	}
}

func writeJSON(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
}

func replaceTrustPaths(value, parent, project string) string {
	value = strings.ReplaceAll(value, "PROJECT_PARENT", filepath.ToSlash(parent))
	return strings.ReplaceAll(value, "PROJECT", filepath.ToSlash(project))
}
