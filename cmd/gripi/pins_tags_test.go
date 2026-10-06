package main

import (
	"encoding/json"
	"strings"
	"testing"

	gateway "github.com/melounvitek/gripi/internal/server"
)

func listedSession(t *testing.T, id string) gateway.LocalSession {
	t.Helper()
	_, stdout, _ := runCLI("list", "--json")
	var listed []gateway.LocalSession
	if err := json.Unmarshal([]byte(stdout), &listed); err != nil {
		t.Fatal(err)
	}
	for _, session := range listed {
		if session.ID == id {
			return session
		}
	}
	t.Fatalf("%s is not listed: %+v", id, listed)
	return gateway.LocalSession{}
}

func TestPinAndUnpinChangeWhatTheListingReports(t *testing.T) {
	alpha, _ := fakePiGateway(t)

	code, stdout, stderr := runCLI("pin", "0a1-a", "--json")
	if session := decodeSession(t, stdout); code != 0 || stderr != "" || session.ID != "0a1-alpha" || !session.Pinned {
		t.Fatalf("gripi pin = %d, stderr %q, session %+v", code, stderr, session)
	}
	if !listedSession(t, "0a1-alpha").Pinned || listedSession(t, "0a1-beta").Pinned {
		t.Fatal("only 0a1-alpha should be pinned")
	}
	// Pinning what is pinned already is not an error.
	if code, _, stderr := runCLI("pin", "0a1-alpha"); code != 0 || stderr != "" {
		t.Fatalf("second gripi pin = %d, stderr %q", code, stderr)
	}

	code, stdout, stderr = runCLI("unpin", alpha)
	if code != 0 || stderr != "" || !strings.Contains(stdout, "0a1-alpha") {
		t.Fatalf("gripi unpin = %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if listedSession(t, "0a1-alpha").Pinned {
		t.Fatal("0a1-alpha is still pinned")
	}
}

func TestPinExplainsWhatItCannotPin(t *testing.T) {
	fakePiGateway(t)
	for _, name := range []string{"pin", "unpin"} {
		for _, usage := range [][]string{{name}, {name, "0a1-a", "0a1-b"}, {name, "0a1-a", "--bogus"}} {
			if code, stdout, stderr := runCLI(usage...); code != 2 || stdout != "" || !strings.Contains(stderr, "gripi help "+name) {
				t.Fatalf("gripi %q = %d, stdout %q, stderr %q", usage, code, stdout, stderr)
			}
		}
		if code, stdout, stderr := runCLI(name, "zzz"); code != 1 || stdout != "" || !strings.Contains(stderr, "gripi list") {
			t.Fatalf("gripi %s on an unknown session = %d, stdout %q, stderr %q", name, code, stdout, stderr)
		}
	}
}
