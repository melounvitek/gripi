package worker

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/melounvitek/gripi/internal/state"
	"github.com/melounvitek/gripi/internal/worker/reconcile"
)

// Placement is a session that runs on a worker, on a private copy of its repository there: the session's move. Its
// paths are main's.
type Placement struct {
	// Session is the session's key.
	Session string `json:"session"`
	// Move names the move on the worker and in main's state folder.
	Move   string `json:"move"`
	Worker string `json:"worker"`
	// Cwd is the session's folder, Repository the common Git folder of its repository, Roots the worktrees that the
	// copy holds, the session's own first, and Private the folders directly in main's home folder that hold them,
	// which the move's processes see from the copy.
	Cwd        string   `json:"cwd"`
	Repository string   `json:"repository"`
	Roots      []string `json:"roots"`
	Private    []string `json:"private"`
	// Stage is how far a return has got: releasing once it has asked the worker to release the move, which takes no
	// new sessions from then on, and released once main's copies have the move's last changes, which the return then
	// brings to main's folders.
	Stage string `json:"stage,omitempty"`
}

const (
	releasing = "releasing"
	released  = "released"
)

func (placement Placement) move() Move {
	return Move{ID: placement.Move, Repository: placement.Repository, Roots: placement.Roots, Private: placement.Private}
}

// Notice says what a session's return kept as main had it, though the worker changed it too, with the worker's
// versions in the recovery folder Recovery and under the refs of Result, until the user dismisses it.
type Notice struct {
	Session string `json:"session"`
	Worker  string `json:"worker"`
	// Repository is the main checkout of the session's repository.
	Repository string           `json:"repository"`
	Recovery   string           `json:"recovery"`
	Result     reconcile.Result `json:"result"`
	Time       time.Time        `json:"time"`
}

// staleMove is a move whose copy a worker still has, though main no longer needs it.
type staleMove struct {
	Worker string `json:"worker"`
	Move   string `json:"move"`
}

// Placements is the gateway's record of the sessions on workers, of the moves that began, of what their returns kept
// aside, and of the copies that workers still have of moves that ended, kept in a state file.
type Placements struct {
	file *state.File
	mu   sync.Mutex
	data record
}

type record struct {
	Moves []Placement `json:"moves"`
	// Begun are moves whose sessions aren't on their workers yet.
	Begun   []Placement `json:"begun,omitempty"`
	Notices []Notice    `json:"notices,omitempty"`
	Stale   []staleMove `json:"stale,omitempty"`
}

// ErrPreviousPlacements is why the placements of the previous version of Gripi don't load: only that version can bring
// their repositories back.
var ErrPreviousPlacements = errors.New("the previous version of Gripi moved repositories to workers")

// LoadPlacements reads the placements from the state file at path, which may not exist yet. The previous version of
// Gripi kept whole repositories on workers there, as a list: an empty one is no placement, which the next change
// replaces, but any other fails with ErrPreviousPlacements and stays as it is.
func LoadPlacements(path string) (*Placements, error) {
	placements := &Placements{file: state.NewFile(path)}
	contents, found, err := placements.file.Read()
	if err != nil || !found {
		return placements, err
	}
	var previous []struct {
		Worker string
		Unit   struct{ Main string }
	}
	if json.Unmarshal(contents, &previous) == nil {
		if len(previous) == 0 {
			return placements, nil
		}
		var repositories []string
		for _, placement := range previous {
			repositories = append(repositories, placement.Unit.Main+" on "+placement.Worker)
		}
		return placements, fmt.Errorf("%w, as %s says: %s; bring them back with that version ('gripi worker return <folder>') before upgrading",
			ErrPreviousPlacements, path, strings.Join(repositories, ", "))
	}
	if err := json.Unmarshal(contents, &placements.data); err != nil {
		return placements, fmt.Errorf("parse %s: %w", path, err)
	}
	return placements, nil
}

// Find returns the placement of the session whose key is key.
func (placements *Placements) Find(key string) (Placement, bool) {
	placements.mu.Lock()
	defer placements.mu.Unlock()
	i := slices.IndexFunc(placements.data.Moves, func(placement Placement) bool { return placement.Session == key })
	if i < 0 {
		return Placement{}, false
	}
	return placements.data.Moves[i], true
}

// All returns every placement.
func (placements *Placements) All() []Placement {
	placements.mu.Lock()
	defer placements.mu.Unlock()
	return slices.Clone(placements.data.Moves)
}

// Begin notes that a move began, before it leaves anything to clean up.
func (placements *Placements) Begin(placement Placement) error {
	return placements.change(func(data *record) error {
		data.Begun = append(data.Begun, placement)
		return nil
	})
}

// Begun returns the moves that began, but whose sessions aren't on their workers.
func (placements *Placements) Begun() []Placement {
	placements.mu.Lock()
	defer placements.mu.Unlock()
	return slices.Clone(placements.data.Begun)
}

// Add records a placement, the end of a move that began, unless its session is placed already.
func (placements *Placements) Add(placement Placement) error {
	return placements.change(func(data *record) error {
		if slices.ContainsFunc(data.Moves, func(existing Placement) bool { return existing.Session == placement.Session }) {
			return refuse("the session is on a worker already")
		}
		data.Begun = slices.DeleteFunc(data.Begun, func(begun Placement) bool { return begun.Move == placement.Move })
		data.Moves = append(data.Moves, placement)
		return nil
	})
}

// SetStage records how far the return of a session has got.
func (placements *Placements) SetStage(key, stage string) error {
	return placements.change(func(data *record) error {
		i := slices.IndexFunc(data.Moves, func(placement Placement) bool { return placement.Session == key })
		if i < 0 {
			return fmt.Errorf("session %s is on no worker", key)
		}
		data.Moves[i].Stage = stage
		return nil
	})
}

// Finish forgets the placement of a session that has returned, with its notice if it has one, and notes that the
// worker's copy of its move can go.
func (placements *Placements) Finish(key string, notice *Notice) error {
	return placements.change(func(data *record) error {
		i := slices.IndexFunc(data.Moves, func(placement Placement) bool { return placement.Session == key })
		if i < 0 {
			return fmt.Errorf("session %s is on no worker", key)
		}
		data.Stale = append(data.Stale, staleMove{Worker: data.Moves[i].Worker, Move: data.Moves[i].Move})
		data.Moves = slices.Delete(data.Moves, i, i+1)
		if notice != nil {
			data.Notices = append(data.Notices, *notice)
		}
		return nil
	})
}

// Abandon forgets a move that failed, and notes that its worker may have a copy of it, which can go.
func (placements *Placements) Abandon(placement Placement) error {
	return placements.change(func(data *record) error {
		data.Begun = slices.DeleteFunc(data.Begun, func(begun Placement) bool { return begun.Move == placement.Move })
		data.Stale = append(data.Stale, staleMove{Worker: placement.Worker, Move: placement.Move})
		return nil
	})
}

// Stale returns the moves whose copies the worker of a name still has, though main no longer needs them.
func (placements *Placements) Stale(worker string) []string {
	placements.mu.Lock()
	defer placements.mu.Unlock()
	var moves []string
	for _, stale := range placements.data.Stale {
		if stale.Worker == worker {
			moves = append(moves, stale.Move)
		}
	}
	return moves
}

// Deleted notes that the worker of a name deleted its copy of a move.
func (placements *Placements) Deleted(worker, move string) error {
	return placements.change(func(data *record) error {
		data.Stale = slices.DeleteFunc(data.Stale, func(stale staleMove) bool { return stale == staleMove{Worker: worker, Move: move} })
		return nil
	})
}

// Notices returns the notices of the returns of the session whose key is key, oldest first.
func (placements *Placements) Notices(key string) []Notice {
	placements.mu.Lock()
	defer placements.mu.Unlock()
	var notices []Notice
	for _, notice := range placements.data.Notices {
		if notice.Session == key {
			notices = append(notices, notice)
		}
	}
	return notices
}

// Dismiss forgets the notice of the return whose recovery folder is recovery.
func (placements *Placements) Dismiss(recovery string) error {
	return placements.change(func(data *record) error {
		data.Notices = slices.DeleteFunc(data.Notices, func(notice Notice) bool { return notice.Recovery == recovery })
		return nil
	})
}

// change changes the record and saves it, unless change fails.
func (placements *Placements) change(change func(*record) error) error {
	placements.mu.Lock()
	defer placements.mu.Unlock()
	data := placements.data
	data.Moves, data.Begun, data.Notices, data.Stale = slices.Clone(data.Moves), slices.Clone(data.Begun), slices.Clone(data.Notices), slices.Clone(data.Stale)
	if err := change(&data); err != nil {
		return err
	}
	contents, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	if err := placements.file.Write(append(contents, '\n')); err != nil {
		return err
	}
	placements.data = data
	return nil
}
