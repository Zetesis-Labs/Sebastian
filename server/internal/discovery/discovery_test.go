package discovery

import (
	"context"
	"io"
	"log/slog"
	"runtime"
	"testing"
	"time"
)

// dnssd stops its socket readers only on context.Canceled; a browse cycle
// that expired by deadline leaked two spinning goroutines per cycle.
func TestBrowseCyclesDoNotLeakGoroutines(t *testing.T) {
	const cycles = 4
	before := runtime.NumGoroutine()
	m := NewMDNS(slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		m.runCycles(ctx, 100*time.Millisecond, cycles)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("browse cycles did not finish")
	}
	cancel()
	time.Sleep(200 * time.Millisecond)
	if after := runtime.NumGoroutine(); after > before+1 {
		t.Fatalf("goroutines before=%d after=%d: browse cycles leak readers", before, after)
	}
}
