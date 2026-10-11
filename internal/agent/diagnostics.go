package agent

import (
	"encoding/json"
	"io"
	"sync"
	"time"
)

type Diagnostics struct {
	Enabled bool
	Writer  io.Writer
	Clock   func() time.Time
	mu      sync.Mutex
}

func (diagnostics *Diagnostics) Log(event string, fields map[string]any) {
	if diagnostics == nil || !diagnostics.Enabled || diagnostics.Writer == nil {
		return
	}
	clock := diagnostics.Clock
	if clock == nil {
		clock = time.Now
	}
	payload := map[string]any{"component": "pi_rpc", "event": event, "timestamp": clock().UTC().Format("2006-01-02T15:04:05.000Z07:00")}
	for key, value := range fields {
		payload[key] = value
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return
	}
	diagnostics.mu.Lock()
	defer diagnostics.mu.Unlock()
	_, _ = diagnostics.Writer.Write(append(encoded, '\n'))
}
