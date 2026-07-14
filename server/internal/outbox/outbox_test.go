package outbox

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"
)

type stubStore struct {
	result BatchResult
	now    time.Time
	limit  int
}

func (s *stubStore) ProcessPending(_ context.Context, now time.Time, limit int, _ Publisher) (BatchResult, error) {
	s.now = now
	s.limit = limit
	return s.result, nil
}

type stubPublisher struct{}

func (stubPublisher) Publish(context.Context, Event) error { return nil }

func TestRunnerProcessesOneBatch(t *testing.T) {
	store := &stubStore{result: BatchResult{Attempted: 2, Published: 2}}
	runner := NewRunner(store, stubPublisher{}, slog.New(slog.NewTextHandler(io.Discard, nil)), 25, time.Second)
	now := time.Date(2026, 7, 14, 10, 0, 0, 0, time.UTC)
	runner.now = func() time.Time { return now }

	result, err := runner.ProcessOnce(context.Background())
	if err != nil {
		t.Fatalf("ProcessOnce() error = %v", err)
	}
	if result.Published != 2 || store.limit != 25 || !store.now.Equal(now) {
		t.Fatalf("unexpected processing result=%#v limit=%d now=%s", result, store.limit, store.now)
	}
}

func TestRetryDelayIsBounded(t *testing.T) {
	if got := RetryDelay(1); got != time.Second {
		t.Fatalf("RetryDelay(1) = %s, want 1s", got)
	}
	if got := RetryDelay(100); got != time.Minute {
		t.Fatalf("RetryDelay(100) = %s, want 1m", got)
	}
}
