package server

import (
	"fmt"
	"testing"
	"time"
)

func TestNotificationPresenceTracksFocusedSessionsByOwnerAndClient(t *testing.T) {
	now := time.Date(2026, 8, 4, 10, 0, 0, 0, time.UTC)
	presence := newNotificationPresence(func() time.Time { return now })

	presence.Update("owner-a", "client-a", "/sessions/one.jsonl", true)
	if !presence.Focused("owner-a", "/sessions/one.jsonl") {
		t.Fatal("focused session was not tracked")
	}
	if presence.Focused("owner-b", "/sessions/one.jsonl") {
		t.Fatal("focus leaked to another owner")
	}

	presence.Update("owner-a", "client-a", "/sessions/two.jsonl", true)
	if presence.Focused("owner-a", "/sessions/one.jsonl") || !presence.Focused("owner-a", "/sessions/two.jsonl") {
		t.Fatal("client session change was not applied")
	}

	presence.Update("owner-a", "client-b", "/sessions/two.jsonl", true)
	presence.Update("owner-a", "client-a", "", false)
	if !presence.Focused("owner-a", "/sessions/two.jsonl") {
		t.Fatal("clearing one client removed another client's focus")
	}
	presence.Update("owner-a", "client-b", "", false)
	if presence.Focused("owner-a", "/sessions/two.jsonl") {
		t.Fatal("cleared session remained focused")
	}
}

func TestNotificationPresenceExpiresAndBoundsClientLeases(t *testing.T) {
	now := time.Date(2026, 8, 4, 10, 0, 0, 0, time.UTC)
	presence := newNotificationPresence(func() time.Time { return now })
	presence.Update("owner", "expiring", "/sessions/old.jsonl", true)

	now = now.Add(notificationPresenceTTL)
	if presence.Focused("owner", "/sessions/old.jsonl") {
		t.Fatal("expired session remained focused")
	}

	for index := 0; index <= notificationPresenceMaxClients; index++ {
		presence.Update("owner", fmt.Sprintf("client-%d", index), fmt.Sprintf("/sessions/%d.jsonl", index), true)
		now = now.Add(time.Millisecond)
	}
	if presence.Focused("owner", "/sessions/0.jsonl") {
		t.Fatal("oldest client was retained past the per-owner limit")
	}
	if !presence.Focused("owner", fmt.Sprintf("/sessions/%d.jsonl", notificationPresenceMaxClients)) {
		t.Fatal("newest client was not retained")
	}
}
