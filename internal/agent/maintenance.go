package agent

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

type Maintenance struct {
	interval time.Duration
	cleanup  func(context.Context) error
	onError  func(error)
	mu       sync.Mutex
	cancel   context.CancelFunc
	done     chan struct{}
}

func NewMaintenance(interval time.Duration, cleanup func(context.Context) error, onError func(error)) (*Maintenance, error) {
	if interval <= 0 {
		return nil, fmt.Errorf("interval must be positive")
	}
	return &Maintenance{interval: interval, cleanup: cleanup, onError: onError}, nil
}

func (maintenance *Maintenance) Start(parent context.Context) bool {
	maintenance.mu.Lock()
	defer maintenance.mu.Unlock()
	if maintenance.cancel != nil {
		return false
	}
	ctx, cancel := context.WithCancel(parent)
	maintenance.cancel = cancel
	maintenance.done = make(chan struct{})
	go maintenance.run(ctx, maintenance.done)
	return true
}

func (maintenance *Maintenance) Stop() bool {
	maintenance.mu.Lock()
	if maintenance.cancel == nil {
		maintenance.mu.Unlock()
		return false
	}
	cancel, done := maintenance.cancel, maintenance.done
	maintenance.mu.Unlock()
	cancel()
	<-done
	maintenance.mu.Lock()
	if maintenance.done == done {
		maintenance.cancel = nil
		maintenance.done = nil
	}
	maintenance.mu.Unlock()
	return true
}

func (maintenance *Maintenance) run(ctx context.Context, done chan struct{}) {
	defer close(done)
	ticker := time.NewTicker(maintenance.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := maintenance.sweep(ctx); err != nil && maintenance.onError != nil {
				maintenance.report(err)
			}
		}
	}
}

func (maintenance *Maintenance) sweep(ctx context.Context) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("idle client maintenance panicked: %v", recovered)
		}
	}()
	return maintenance.cleanup(ctx)
}

func (maintenance *Maintenance) report(err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			_, _ = fmt.Fprintf(os.Stderr, "Idle client maintenance error reporting failed: %v\n", recovered)
		}
	}()
	maintenance.onError(err)
}

func LogMaintenanceError(writer io.Writer) func(error) {
	return func(err error) { _, _ = fmt.Fprintf(writer, "Idle Pi cleanup failed: %v\n", err) }
}
