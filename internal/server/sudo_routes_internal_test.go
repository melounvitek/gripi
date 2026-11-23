package server

import (
	"context"
	"net/http"
	"testing"
	"time"
)

// An answer to sudo's asking again that comes as the run ends neither reaches askpass nor stays remembered.
func TestSudoRunTakesNoAnswerThatComesAsItEnds(t *testing.T) {
	var requests sudoRequests
	run := &sudoRun{ctx: context.Background(), request: &sudoRequest{view: sudoRequestView{Session: "s"}}, delivered: true}
	given := make(chan []byte, 1)
	go func() { given <- requests.password(context.Background(), run) }()
	var views []sudoRequestView
	for deadline := time.Now().Add(5 * time.Second); len(views) == 0; time.Sleep(time.Millisecond) {
		if views = requests.of("s"); time.Now().After(deadline) {
			t.Fatal("sudo asking again shows no request")
		}
	}

	requests.end(run, true)
	if status := requests.approve(views[0].ID, func(string) bool { return true }, []byte("late"), true); status != http.StatusOK {
		t.Fatalf("answer = %d", status)
	}
	if password := <-given; password != nil {
		t.Fatalf("askpass got %q", password)
	}
	if len(requests.remembered) != 0 {
		t.Fatalf("Gripi remembers %v", requests.remembered)
	}
}
