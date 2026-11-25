package server

import (
	"context"
	"testing"
	"time"
)

// The desktop app reads a poll's JSON only up to 8 MiB, so a poll carries at most 2 MiB of agent messages and the
// others wait for the next one.
func TestPCLinkPollCarriesAtMost2MiBOfAgentMessages(t *testing.T) {
	poll := PCLinkPoll
	PCLinkPoll = 10 * time.Millisecond
	t.Cleanup(func() { PCLinkPoll = poll })
	links := &pcLinks{}
	if _, err := links.poll(context.Background(), "app", true); err != nil {
		t.Fatal(err)
	}
	links.mu.Lock()
	for range 9 {
		links.current.send(pcMessage{Type: "ssh", Message: make([]byte, pcMessageLimit)})
	}
	links.mu.Unlock()

	for _, want := range []int{8, 1} {
		messages, err := links.poll(context.Background(), "app", false)
		if err != nil || len(messages) != want {
			t.Fatalf("poll = %d messages (%v), want %d", len(messages), err, want)
		}
	}
}
