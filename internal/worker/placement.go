package worker

import (
	"encoding/json"
	"fmt"
	"slices"
	"sync"

	"github.com/melounvitek/gripi/internal/state"
	"github.com/melounvitek/gripi/internal/worker/mirror"
)

// Placement is a repository unit on a worker: the Pi of every session whose folder is in the unit runs there, and
// main keeps a mirror of the unit.
type Placement struct {
	Worker string      `json:"worker"`
	Unit   mirror.Unit `json:"unit"`
}

// Placements is the gateway's record of the units on workers, kept in a state file.
type Placements struct {
	file *state.File
	mu   sync.Mutex
	list []Placement
}

// LoadPlacements reads the placements from the state file at path, which may not exist yet.
func LoadPlacements(path string) (*Placements, error) {
	placements := &Placements{file: state.NewFile(path)}
	contents, found, err := placements.file.Read()
	if err == nil && found {
		if err = json.Unmarshal(contents, &placements.list); err != nil {
			err = fmt.Errorf("parse %s: %w", path, err)
		}
	}
	return placements, err
}

// Find returns the placement whose unit contains path.
func (placements *Placements) Find(path string) (Placement, bool) {
	placements.mu.Lock()
	defer placements.mu.Unlock()
	for _, placement := range placements.list {
		if placement.Unit.Contains(path) {
			return placement, true
		}
	}
	return Placement{}, false
}

// All returns every placement.
func (placements *Placements) All() []Placement {
	placements.mu.Lock()
	defer placements.mu.Unlock()
	return slices.Clone(placements.list)
}

// Add records a placement, unless its unit overlaps one placed already.
func (placements *Placements) Add(placement Placement) error {
	placements.mu.Lock()
	defer placements.mu.Unlock()
	for _, existing := range placements.list {
		if existing.Unit.Contains(placement.Unit.Main) || placement.Unit.Contains(existing.Unit.Main) {
			return fmt.Errorf("%s is on worker %s already", existing.Unit.Main, existing.Worker)
		}
	}
	return placements.save(append(slices.Clone(placements.list), placement))
}

// Remove forgets the placement of the unit whose main folder is main.
func (placements *Placements) Remove(main string) error {
	placements.mu.Lock()
	defer placements.mu.Unlock()
	return placements.save(slices.DeleteFunc(slices.Clone(placements.list), func(placement Placement) bool { return placement.Unit.Main == main }))
}

func (placements *Placements) save(list []Placement) error {
	contents, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	if err := placements.file.Write(append(contents, '\n')); err != nil {
		return err
	}
	placements.list = list
	return nil
}
