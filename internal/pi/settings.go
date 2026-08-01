package pi

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

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
	data, err := os.ReadFile(filepath.Join(resolver.AgentDir, "trust.json"))
	if err == nil {
		if json.Unmarshal(data, &decisions) != nil || decisions == nil {
			return false
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return false
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
	data, err := os.ReadFile(path)
	if err != nil {
		return settingsFile{}, false
	}
	var settings settingsFile
	if json.Unmarshal(data, &settings) != nil {
		return settingsFile{}, false
	}
	return settings, true
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
