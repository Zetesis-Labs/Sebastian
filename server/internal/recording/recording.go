package recording

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

var (
	ErrNotFound    = errors.New("recording or session not found")
	ErrConflict    = errors.New("recording already registered")
	ErrUnavailable = errors.New("recording service unavailable")
)

type Kind string

const (
	KindModel      Kind = "model"
	KindMicrophone Kind = "microphone"
	KindAgent      Kind = "agent"
	KindComposite  Kind = "composite"
)

type Recording struct {
	ID          uuid.UUID
	SessionID   uuid.UUID
	Room        string
	Kind        Kind
	FileName    string
	ObjectURL   string
	ContentType string
	ByteSize    int64
	DurationMs  int64
	CapturedAt  time.Time
	CreatedAt   time.Time
	Transcript  *string
}

type Registration struct {
	Room        string
	Kind        Kind
	FileName    string
	ObjectURL   string
	ContentType string
	ByteSize    int64
	DurationMs  int64
	CapturedAt  time.Time
	Transcript  *string
}

type Summary struct {
	Count           int64
	TotalBytes      int64
	TotalDurationMs int64
	LastCapturedAt  *time.Time
}

type CreateRecord struct {
	ID      uuid.UUID
	EventID uuid.UUID
	Input   Registration
	Now     time.Time
}

type Store interface {
	ListRecordings(context.Context, int) ([]Recording, error)
	GetRecording(context.Context, uuid.UUID) (Recording, error)
	CreateRecording(context.Context, CreateRecord) (Recording, error)
	RecordingsSummary(context.Context) (Summary, error)
}

type Service struct {
	store Store
	now   func() time.Time
}

func NewService(store Store) *Service {
	return &Service{store: store, now: time.Now}
}

func (s *Service) List(ctx context.Context, limit int) ([]Recording, error) {
	recordings, err := s.store.ListRecordings(ctx, limit)
	if err != nil {
		return nil, fmt.Errorf("list recordings: %w", ErrUnavailable)
	}
	return recordings, nil
}

func (s *Service) Get(ctx context.Context, id uuid.UUID) (Recording, error) {
	recording, err := s.store.GetRecording(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return Recording{}, ErrNotFound
	}
	if err != nil {
		return Recording{}, fmt.Errorf("get recording: %w", ErrUnavailable)
	}
	return recording, nil
}

func (s *Service) Register(ctx context.Context, input Registration) (Recording, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return Recording{}, fmt.Errorf("generate recording id: %w", ErrUnavailable)
	}
	eventID, err := uuid.NewV7()
	if err != nil {
		return Recording{}, fmt.Errorf("generate recording event id: %w", ErrUnavailable)
	}
	recording, err := s.store.CreateRecording(ctx, CreateRecord{
		ID: id, EventID: eventID, Input: input, Now: s.now().UTC(),
	})
	if errors.Is(err, ErrNotFound) || errors.Is(err, ErrConflict) {
		return Recording{}, err
	}
	if err != nil {
		return Recording{}, fmt.Errorf("register recording: %w", ErrUnavailable)
	}
	return recording, nil
}

func (s *Service) Summary(ctx context.Context) (Summary, error) {
	summary, err := s.store.RecordingsSummary(ctx)
	if err != nil {
		return Summary{}, fmt.Errorf("summarize recordings: %w", ErrUnavailable)
	}
	return summary, nil
}
