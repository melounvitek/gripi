package auth

import "testing"

func TestOlderPi(t *testing.T) {
	for version, older := range map[string]bool{
		"0.99.0": false, "0.99.2": false, "1.0.0": false, "1.4.0\n": false, "10.0.0": false,
		"0.87.1": true, "0.9.0": true, "0.81.1": true, "": true, "unknown": true,
	} {
		if got := OlderPi(version); got != older {
			t.Errorf("OlderPi(%q) = %v, want %v", version, got, older)
		}
	}
}
