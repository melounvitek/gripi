package agent

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestMaintenanceStopsPromptlyAndContinuesAfterFailure(t *testing.T) {
	calls := make(chan int, 3)
	count := 0
	maintenance, err := NewMaintenance(time.Millisecond, func(context.Context) error {
		count++
		calls <- count
		if count == 1 {
			return errors.New("first")
		}
		return nil
	}, func(error) {})
	if err != nil {
		t.Fatal(err)
	}
	if !maintenance.Start(context.Background()) || maintenance.Start(context.Background()) {
		t.Fatal("maintenance start lifecycle")
	}
	deadline := time.After(time.Second)
	for {
		select {
		case call := <-calls:
			if call >= 2 {
				goto complete
			}
		case <-deadline:
			t.Fatal("maintenance did not continue")
		}
	}
complete:
	started := time.Now()
	if !maintenance.Stop() {
		t.Fatal("maintenance did not stop")
	}
	if time.Since(started) > 100*time.Millisecond {
		t.Fatal("maintenance stop waited for interval")
	}
	if maintenance.Stop() {
		t.Fatal("duplicate stop succeeded")
	}
}
