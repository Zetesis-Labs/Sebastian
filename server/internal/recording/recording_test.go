package recording

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

type stubStore struct {
	created CreateRecord
}

func (s *stubStore) ListRecordings(context.Context, int) ([]Recording, error) {
	return nil, nil
}

func (s *stubStore) GetRecording(context.Context, uuid.UUID) (Recording, error) {
	return Recording{}, nil
}

func (s *stubStore) CreateRecording(_ context.Context, record CreateRecord) (Recording, error) {
	s.created = record
	return Recording{ID: record.ID, Room: record.Input.Room}, nil
}

func (s *stubStore) RecordingsSummary(context.Context) (Summary, error) {
	return Summary{}, nil
}

func TestRegisterCreatesTimeOrderedIdentifiers(t *testing.T) {
	store := &stubStore{}
	service := NewService(store)
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }

	created, err := service.Register(context.Background(), Registration{Room: "sebastian-room"})
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	if created.ID == uuid.Nil || store.created.EventID == uuid.Nil || !store.created.Now.Equal(now) {
		t.Fatalf("unexpected create record: %#v", store.created)
	}
	if store.created.ID.Version() != 7 || store.created.EventID.Version() != 7 {
		t.Fatalf("identifiers are not UUIDv7: recording=%s event=%s", store.created.ID, store.created.EventID)
	}
}
