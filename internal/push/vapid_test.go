package push

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	webpush "github.com/SherClockHolmes/webpush-go"
)

func TestVAPIDIdentityGeneratesLazilyAndStaysStable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vapid-identity.json")
	identity := NewVAPIDIdentity(path)

	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("identity exists before first use: %v", err)
	}

	const workers = 40
	start := make(chan struct{})
	keys := make(chan VAPIDKeys, workers)
	errors := make(chan error, workers)
	var wait sync.WaitGroup
	for range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			value, err := NewVAPIDIdentity(path).Keys()
			if err != nil {
				errors <- err
				return
			}
			keys <- value
		}()
	}
	close(start)
	wait.Wait()
	close(keys)
	close(errors)

	var first VAPIDKeys
	for err := range errors {
		t.Fatal(err)
	}
	for value := range keys {
		if first == (VAPIDKeys{}) {
			first = value
		}
		if value != first {
			t.Fatalf("concurrent identities differ: %#v and %#v", first, value)
		}
	}
	if first == (VAPIDKeys{}) {
		t.Fatal("no VAPID identity was generated")
	}
	if value, err := identity.Keys(); err != nil || value != first {
		t.Fatalf("cached identity = %#v, %v; want %#v", value, err, first)
	}

	other := NewVAPIDIdentity(path)
	if value, err := other.Keys(); err != nil || value != first {
		t.Fatalf("persisted identity = %#v, %v; want %#v", value, err, first)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("identity mode = %o", info.Mode().Perm())
	}
}

func TestVAPIDIdentityFailsClosedForMalformedState(t *testing.T) {
	private, public, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		t.Fatal(err)
	}
	_, otherPublic, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name     string
		contents []byte
	}{
		{name: "malformed JSON", contents: []byte(`{"private_key":`)},
		{name: "malformed private", contents: encodedVAPIDIdentity(t, VAPIDKeys{PrivateKey: "not-a-vapid-key", PublicKey: public})},
		{name: "malformed public", contents: encodedVAPIDIdentity(t, VAPIDKeys{PrivateKey: private, PublicKey: "not-a-vapid-key"})},
		{name: "mismatched pair", contents: encodedVAPIDIdentity(t, VAPIDKeys{PrivateKey: private, PublicKey: otherPublic})},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "identity.json")
			if err := os.WriteFile(path, tt.contents, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := NewVAPIDIdentity(path).Keys(); err == nil {
				t.Fatal("malformed identity loaded")
			}
			persisted, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(persisted) != string(tt.contents) {
				t.Fatalf("malformed state changed to %q", persisted)
			}
		})
	}
}

func encodedVAPIDIdentity(t *testing.T, keys VAPIDKeys) []byte {
	t.Helper()
	contents, err := json.Marshal(keys)
	if err != nil {
		t.Fatal(err)
	}
	return contents
}
