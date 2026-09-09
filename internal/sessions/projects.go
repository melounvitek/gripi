package sessions

import (
	"errors"
	"fmt"
	"os"
)

// ProjectCWDs preserves existing projects on first startup. Later discoveries
// stay out of the project list until Gripi explicitly remembers their directory.
func (state *GatewayState) ProjectCWDs(existing []*Session) (map[string]bool, error) {
	state.mu.Lock()
	defer state.mu.Unlock()

	var projects map[string]bool
	err := readJSON(state.projectsPath, &projects)
	if errors.Is(err, os.ErrNotExist) {
		projects = make(map[string]bool)
		for _, session := range existing {
			projects[session.CWD] = true
		}
		if err := writeJSON(state.projectsPath, projects); err != nil {
			return nil, fmt.Errorf("initialize projects: %w", err)
		}
	} else if err != nil {
		return nil, fmt.Errorf("read projects: %w", err)
	}
	return projects, nil
}

func (state *GatewayState) RememberProject(cwd string) error {
	state.mu.Lock()
	defer state.mu.Unlock()

	projects := make(map[string]bool)
	if err := readJSONIfExists(state.projectsPath, &projects); err != nil {
		return fmt.Errorf("read projects: %w", err)
	}
	if projects[cwd] {
		return nil
	}
	if projects == nil {
		projects = make(map[string]bool)
	}
	projects[cwd] = true
	if err := writeJSON(state.projectsPath, projects); err != nil {
		return fmt.Errorf("remember project: %w", err)
	}
	return nil
}
