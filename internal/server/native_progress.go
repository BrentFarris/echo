package server

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"
)

// Slow setup and lock waits may produce no process output. Report their phase
// through the live tool stream, leaving the command's returned bytes unchanged.
func nativeCommandProgress(ctx context.Context, label string, interval time.Duration, emit func(string)) (update func(string), stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	var phase atomic.Value
	phase.Store("waiting for repository")
	started := time.Now()
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				emit(fmt.Sprintf("[Echo] %s: %s (%s elapsed).\n", label, phase.Load().(string), time.Since(started).Round(time.Second)))
			}
		}
	}()
	return func(value string) { phase.Store(value) }, func() { cancel(); <-done }
}
