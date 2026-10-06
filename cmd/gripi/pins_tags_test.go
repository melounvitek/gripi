package main

import (
	"encoding/json"
	"fmt"
	"reflect"
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

func TestTagAndUntagChangeTheTagsOfASession(t *testing.T) {
	alpha, _ := fakePiGateway(t)

	code, stdout, stderr := runCLI("tag", "0a1-a", "Urgent", "needs review", "backend", "--json")
	// Tags come back as the gateway stores them: lowercased and sorted.
	if session := decodeSession(t, stdout); code != 0 || stderr != "" || !reflect.DeepEqual(session.Tags, []string{"backend", "needs review", "urgent"}) {
		t.Fatalf("gripi tag = %d, stderr %q, session %+v", code, stderr, session)
	}
	if code, stdout, stderr := runCLI("tag", "0a1-b", "backend"); code != 0 || stderr != "" || !strings.Contains(stdout, "backend") {
		t.Fatalf("gripi tag = %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if tags := listedSession(t, "0a1-alpha").Tags; !reflect.DeepEqual(tags, []string{"backend", "needs review", "urgent"}) {
		t.Fatalf("listed tags = %q", tags)
	}

	code, stdout, stderr = runCLI("tags", "--json")
	if code != 0 || stderr != "" || strings.Join(strings.Fields(stdout), "") != `[{"name":"backend","count":2},{"name":"needsreview","count":1},{"name":"urgent","count":1}]` {
		t.Fatalf("gripi tags --json = %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	code, stdout, stderr = runCLI("tags", "0a1-b")
	if lines := strings.Split(strings.TrimSpace(stdout), "\n"); code != 0 || stderr != "" || len(lines) != 2 || strings.Join(strings.Fields(stdout), " ") != "TAG SESSIONS backend 2" {
		t.Fatalf("gripi tags for one session = %d, stdout %q, stderr %q", code, stdout, stderr)
	}

	// Removing a tag that the session does not have is not an error.
	code, stdout, stderr = runCLI("untag", alpha, "backend", "missing", "needs review", "--json")
	if session := decodeSession(t, stdout); code != 0 || stderr != "" || !reflect.DeepEqual(session.Tags, []string{"urgent"}) {
		t.Fatalf("gripi untag = %d, stderr %q, session %+v", code, stderr, session)
	}
	if code, stdout, _ := runCLI("untag", "0a1-b", "backend", "--json"); code != 0 || !strings.Contains(stdout, `"tags": []`) {
		t.Fatalf("a session without tags needs an empty list, not null: %d %q", code, stdout)
	}
	if _, stdout, _ := runCLI("tags"); strings.Join(strings.Fields(stdout), " ") != "TAG SESSIONS urgent 1" {
		t.Fatalf("gripi tags after untagging = %q", stdout)
	}
	if code, stdout, _ := runCLI("tags", "0a1-b", "--json"); code != 0 || strings.TrimSpace(stdout) != "[]" {
		t.Fatalf("gripi tags for a session without tags = %d %q", code, stdout)
	}
}

func TestTagCommandsExplainWhatTheyCannotDo(t *testing.T) {
	fakePiGateway(t)
	for _, name := range []string{"tag", "untag"} {
		// One bad tag must not leave the good one before it applied.
		for _, usage := range [][]string{{name}, {name, "0a1-a"}, {name, "", "tag"}, {name, "0a1-a", "fine", " "}, {name, "0a1-a", "fine", strings.Repeat("x", 65)}, {name, "0a1-a", "tag", "--bogus"}} {
			if code, stdout, stderr := runCLI(usage...); code != 2 || stdout != "" || !strings.Contains(stderr, "gripi help "+name) {
				t.Fatalf("gripi %q = %d, stdout %q, stderr %q", usage, code, stdout, stderr)
			}
		}
		if code, stdout, stderr := runCLI(name, "zzz", "tag"); code != 1 || stdout != "" || !strings.Contains(stderr, "gripi list") {
			t.Fatalf("gripi %s on an unknown session = %d, stdout %q, stderr %q", name, code, stdout, stderr)
		}
	}
	if tags := listedSession(t, "0a1-alpha").Tags; len(tags) != 0 {
		t.Fatalf("refused commands left tags behind: %q", tags)
	}
	for _, usage := range [][]string{{"tags", "0a1-a", "0a1-b"}, {"tags", ""}, {"tags", "--bogus"}} {
		if code, stdout, stderr := runCLI(usage...); code != 2 || stdout != "" || !strings.Contains(stderr, "gripi help tags") {
			t.Fatalf("gripi %q = %d, stdout %q, stderr %q", usage, code, stdout, stderr)
		}
	}
	if code, stdout, stderr := runCLI("tags", "zzz"); code != 1 || stdout != "" || !strings.Contains(stderr, "gripi list") {
		t.Fatalf("gripi tags for an unknown session = %d, stdout %q, stderr %q", code, stdout, stderr)
	}

	tooMany := []string{"tag", "0a1-a"}
	for index := range 33 {
		tooMany = append(tooMany, fmt.Sprintf("tag-%02d", index))
	}
	if code, stdout, stderr := runCLI(tooMany...); code != 1 || stdout != "" || !strings.Contains(stderr, "at most 32 tags") {
		t.Fatalf("gripi tag past the limit = %d, stdout %q, stderr %q", code, stdout, stderr)
	}
}
