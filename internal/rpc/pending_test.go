package rpc

import "testing"

func TestPendingSessionCurrentResolvesRemapsAndPendingDestinationsAtomically(t *testing.T) {
	registry := NewPendingSessionRegistry(nil)
	registry.Remember("/first", "/project")
	if path, cwd, pending := registry.Current("/first"); path != "/first" || cwd != "/project" || !pending {
		t.Fatalf("initial current = %q, %q, %t", path, cwd, pending)
	}

	registry.Remap("/first", "/second")
	if path, cwd, pending := registry.Current("/first"); path != "/second" || cwd != "" || pending {
		t.Fatalf("resolved current = %q, %q, %t", path, cwd, pending)
	}

	registry.Remember("/second", "/project")
	if path, cwd, pending := registry.Current("/first"); path != "/second" || cwd != "/project" || !pending {
		t.Fatalf("remapped pending current = %q, %q, %t", path, cwd, pending)
	}
}
