package mirror

import (
	"maps"
	"slices"
)

// List returns the paths of unit that a mirror with opts copies, the unit's
// folders included, as a sync would now.
func List(unit Unit, opts Options) ([]string, error) {
	s := newSender(unit, opts, nil)
	defer s.close()
	seen, err := s.scan()
	if err != nil {
		return nil, err
	}
	if err := s.unreadError(); err != nil {
		return nil, err
	}
	return slices.Sorted(maps.Keys(seen)), nil
}
