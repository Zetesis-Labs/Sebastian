package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"
)

type Event struct {
	ID            uuid.UUID
	Type          string
	AggregateType string
	AggregateID   string
	Subject       string
	OccurredAt    time.Time
	Data          json.RawMessage
}

type BatchResult struct {
	Attempted int
	Published int
	Failed    int
}

type Publisher interface {
	Publish(context.Context, Event) error
}

type Store interface {
	ProcessPending(context.Context, time.Time, int, Publisher) (BatchResult, error)
}

type Runner struct {
	store        Store
	publisher    Publisher
	logger       *slog.Logger
	batchSize    int
	pollInterval time.Duration
	now          func() time.Time
}

func NewRunner(store Store, publisher Publisher, logger *slog.Logger, batchSize int, pollInterval time.Duration) *Runner {
	return &Runner{
		store:        store,
		publisher:    publisher,
		logger:       logger,
		batchSize:    batchSize,
		pollInterval: pollInterval,
		now:          time.Now,
	}
}

func (r *Runner) Run(ctx context.Context) error {
	timer := time.NewTimer(0)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
		}

		result, err := r.ProcessOnce(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			r.logger.ErrorContext(ctx, "process outbox batch", "error", err)
		}
		if ctx.Err() != nil {
			return nil
		}
		if result.Published > 0 || result.Failed > 0 {
			r.logger.InfoContext(ctx, "outbox batch processed",
				"attempted", result.Attempted,
				"published", result.Published,
				"failed", result.Failed,
			)
		}

		delay := r.pollInterval
		if err == nil && result.Attempted == r.batchSize && result.Failed == 0 {
			delay = 0
		}
		timer.Reset(delay)
	}
}

func (r *Runner) ProcessOnce(ctx context.Context) (BatchResult, error) {
	return r.store.ProcessPending(ctx, r.now().UTC(), r.batchSize, r.publisher)
}

func RetryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := time.Second << min(attempt-1, 6)
	return min(delay, time.Minute)
}
