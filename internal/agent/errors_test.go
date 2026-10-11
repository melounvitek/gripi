package agent

import "testing"

func TestStopResultClassifiesInterruptAndTerminalFailures(t *testing.T) {
	stopping, err := StopResultFor(ErrInterruptPending)
	if err != nil || !stopping.Stopping || stopping.Forced {
		t.Fatalf("stopping = %#v, %v", stopping, err)
	}
	forced, err := StopResultFor(&RequestTimeoutError{Command: "abort"})
	if err != nil || !forced.Forced || forced.Stopping {
		t.Fatalf("forced = %#v, %v", forced, err)
	}
}
