// Package environment stores the variables each user wants Pi to run with.
package environment

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/melounvitek/gripi/internal/state"
)

const (
	maxValueBytes = 16 << 10
	maxVariables  = 100
)

var namePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// InvalidError is a problem with what the user entered. Its text is shown to the user.
type InvalidError struct{ Message string }

func (err *InvalidError) Error() string { return err.Message }

type Variable struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type storeState struct {
	Users map[string][]Variable `json:"users"`
}

// Store keeps the variables of every user in one state file. The user ID is empty in single-user mode.
type Store struct {
	path string
	file *state.File
	mu   sync.Mutex
	// Kept in memory only: no Pi process outlives the gateway.
	changedAt map[string]time.Time
}

func NewStore(path string) *Store {
	return &Store{path: path, file: state.NewFile(path), changedAt: make(map[string]time.Time)}
}

func (store *Store) Variables(userID string) ([]Variable, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	value, err := store.read()
	return value.Users[userID], err
}

// ChangedAt returns when the user's variables last changed, or the zero time if they have not since the gateway started.
func (store *Store) ChangedAt(userID string) time.Time {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.changedAt[userID]
}

// Save replaces the value of a saved name in place and appends a new name. A previousName renames in place.
func (store *Store) Save(userID, previousName string, variable Variable) ([]Variable, error) {
	return store.update(userID, func(variables []Variable) ([]Variable, error) {
		if previousName != "" && previousName != variable.Name {
			if indexOf(variables, variable.Name) >= 0 {
				return nil, &InvalidError{variable.Name + " is already set."}
			}
			if index := indexOf(variables, previousName); index >= 0 {
				variables[index].Name = variable.Name
			}
		}
		return save(variables, variable)
	})
}

// SaveBlock saves the NAME=value lines of text, or none of them, and returns how many it saved.
func (store *Store) SaveBlock(userID, text string) ([]Variable, int, error) {
	saved := 0
	variables, err := store.update(userID, func(variables []Variable) ([]Variable, error) {
		seen := make(map[string]bool)
		for index, line := range strings.Split(text, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			name, value, found := strings.Cut(line, "=")
			name, value = strings.TrimSpace(name), strings.TrimSpace(value)
			if len(value) >= 2 && (value[0] == '\'' || value[0] == '"') && value[len(value)-1] == value[0] {
				value = value[1 : len(value)-1]
			}
			var err error
			switch {
			case !found:
				err = &InvalidError{"expected NAME=value."}
			case seen[name]:
				err = &InvalidError{name + " is set twice."}
			default:
				variables, err = save(variables, Variable{Name: name, Value: value})
			}
			if err != nil {
				return nil, &InvalidError{fmt.Sprintf("Line %d: %s", index+1, err)}
			}
			seen[name] = true
			saved++
		}
		return variables, nil
	})
	return variables, saved, err
}

func (store *Store) Delete(userID, name string) ([]Variable, error) {
	return store.update(userID, func(variables []Variable) ([]Variable, error) {
		return slices.DeleteFunc(variables, func(variable Variable) bool { return variable.Name == name }), nil
	})
}

func save(variables []Variable, variable Variable) ([]Variable, error) {
	switch {
	case !namePattern.MatchString(variable.Name):
		return nil, &InvalidError{"“" + variable.Name + "” is not a valid name. Use letters, digits and _."}
	// These move Pi's or Gripi's directories.
	case variable.Name == "HOME" || strings.HasPrefix(variable.Name, "GRIPI_") || strings.HasPrefix(variable.Name, "PI_CODING_AGENT_"):
		return nil, &InvalidError{"“" + variable.Name + "” is reserved for Gripi and Pi."}
	case variable.Value == "":
		return nil, &InvalidError{variable.Name + " has no value."}
	case strings.ContainsRune(variable.Value, 0):
		return nil, &InvalidError{variable.Name + " has an invalid value."}
	case len(variable.Value) > maxValueBytes:
		return nil, &InvalidError{variable.Name + " is too long."}
	}
	if index := indexOf(variables, variable.Name); index >= 0 {
		variables[index] = variable
		return variables, nil
	}
	if len(variables) >= maxVariables {
		return nil, &InvalidError{fmt.Sprintf("At most %d variables can be saved.", maxVariables)}
	}
	return append(variables, variable), nil
}

func indexOf(variables []Variable, name string) int {
	return slices.IndexFunc(variables, func(variable Variable) bool { return variable.Name == name })
}

func (store *Store) update(userID string, change func([]Variable) ([]Variable, error)) ([]Variable, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	value, err := store.read()
	if err != nil {
		return nil, err
	}
	variables, err := change(value.Users[userID])
	if err != nil {
		return nil, err
	}
	if value.Users == nil {
		value.Users = make(map[string][]Variable)
	}
	value.Users[userID] = variables
	contents, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := store.file.Write(append(contents, '\n')); err != nil {
		return nil, err
	}
	store.changedAt[userID] = time.Now()
	return variables, nil
}

func (store *Store) read() (storeState, error) {
	var value storeState
	contents, found, err := store.file.Read()
	if err != nil || !found {
		return value, err
	}
	if err := json.Unmarshal(contents, &value); err != nil {
		return storeState{}, fmt.Errorf("parse %s: %w", store.path, err)
	}
	return value, nil
}
