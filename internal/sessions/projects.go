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

func (state *GatewayState) RememberProject(cwd string) (func() error, error) {
	state.mu.Lock()
	defer state.mu.Unlock()

	projects := make(map[string]bool)
	if err := readJSONIfExists(state.projectsPath, &projects); err != nil {
		return nil, fmt.Errorf("read projects: %w", err)
	}
	if projects[cwd] {
		state.projectChanges[cwd]++
		return nil, nil
	}
	if projects == nil {
		projects = make(map[string]bool)
	}
	projects[cwd] = true
	if err := writeJSON(state.projectsPath, projects); err != nil {
		return nil, fmt.Errorf("remember project: %w", err)
	}
	state.projectChanges[cwd]++
	revision := state.projectChanges[cwd]
	return func() error {
		state.mu.Lock()
		defer state.mu.Unlock()
		// A later gateway action may have also adopted this directory.
		if state.projectChanges[cwd] != revision {
			return nil
		}
		var current map[string]bool
		if err := readJSON(state.projectsPath, &current); err != nil {
			return fmt.Errorf("read projects for rollback: %w", err)
		}
		delete(current, cwd)
		if err := writeJSON(state.projectsPath, current); err != nil {
			return fmt.Errorf("roll back project: %w", err)
		}
		state.projectChanges[cwd]++
		return nil
	}, nil
}
