package agent

import (
	"testing"
	"time"
)

func TestPendingSessionCurrentResolvesRemapsAndPendingDestinationsAtomically(t *testing.T) {
	registry := NewPendingSessionRegistry(nil)
	registry.Remember("/first", "/project", "")
	if path, cwd, pending := registry.Current("/first"); path != "/first" || cwd != "/project" || !pending {
		t.Fatalf("initial current = %q, %q, %t", path, cwd, pending)
	}

	registry.Remap("/first", "/second")
	if path, cwd, pending := registry.Current("/first"); path != "/second" || cwd != "" || pending {
		t.Fatalf("resolved current = %q, %q, %t", path, cwd, pending)
	}

	registry.Remember("/second", "/project", "")
	if path, cwd, pending := registry.Current("/first"); path != "/second" || cwd != "/project" || !pending {
		t.Fatalf("remapped pending current = %q, %q, %t", path, cwd, pending)
	}
}

func TestPendingSessionRegistryPreservesCreationTime(t *testing.T) {
	now := time.Unix(1000, 0)
	registry := NewPendingSessionRegistry(func() time.Time { return now })
	registry.Remember("/pending", "/one", "")
	now = now.Add(time.Hour)
	registry.Remember("/pending", "/two", "")
	entries := registry.Entries()
	if len(entries) != 1 || entries[0].CWD != "/two" || !entries[0].CreatedAt.Equal(time.Unix(1000, 0)) {
		t.Fatalf("entries = %#v", entries)
	}
	registry.Forget("/pending")
	if _, ok := registry.CWD("/pending"); ok {
		t.Fatal("pending entry remained")
	}
	registry.Remember("/pending", "/three", "")
	if entries := registry.Entries(); len(entries) != 1 || entries[0].CWD != "/three" {
		t.Fatalf("re-remembered entries = %#v", entries)
	}
}
