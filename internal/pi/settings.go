package pi

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
)

const maxSettingsBytes = 1 << 20

type DisplaySettings struct {
	HideThinkingBlock bool
}

type SettingsResolver struct {
	AgentDir            string
	AutoApproveProjects bool
}

type settingsFile struct {
	HideThinkingBlock   *bool  `json:"hideThinkingBlock"`
	DefaultProjectTrust string `json:"defaultProjectTrust"`
}

func (resolver SettingsResolver) DisplaySettings(cwd string) DisplaySettings {
	global, _ := readSettings(filepath.Join(resolver.AgentDir, "settings.json"))
	result := DisplaySettings{}
	if global.HideThinkingBlock != nil {
		result.HideThinkingBlock = *global.HideThinkingBlock
	}

	if !resolver.projectTrusted(cwd, global.DefaultProjectTrust) {
		return result
	}

	project, ok := readSettings(filepath.Join(absolutePath(cwd), ".pi", "settings.json"))
	if ok && project.HideThinkingBlock != nil {
		result.HideThinkingBlock = *project.HideThinkingBlock
	}
	return result
}

func (resolver SettingsResolver) projectTrusted(cwd, defaultTrust string) bool {
	if resolver.AutoApproveProjects {
		return true
	}

	decisions := make(map[string]*bool)
	if data, ok := readBounded(filepath.Join(resolver.AgentDir, "trust.json")); ok {
		_ = json.Unmarshal(data, &decisions)
	}
	for current := canonicalPath(cwd); ; current = filepath.Dir(current) {
		if decision := decisions[current]; decision != nil {
			return *decision
		}
		if parent := filepath.Dir(current); parent == current {
			break
		}
	}
	return defaultTrust == "always"
}

func readSettings(path string) (settingsFile, bool) {
	data, ok := readBounded(path)
	if !ok {
		return settingsFile{}, false
	}
	var settings settingsFile
	if json.Unmarshal(data, &settings) != nil {
		return settingsFile{}, false
	}
	return settings, true
}

func readBounded(path string) ([]byte, bool) {
	file, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, maxSettingsBytes+1))
	if err != nil || len(data) > maxSettingsBytes {
		return nil, false
	}
	return data, true
}

func absolutePath(path string) string {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	return absolute
}

func canonicalPath(path string) string {
	absolute := absolutePath(path)
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return absolute
	}
	return resolved
}
