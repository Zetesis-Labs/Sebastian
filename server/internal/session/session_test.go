package session

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

type fakeStore struct {
	device Device
	record Record
	err    error
}

func (f *fakeStore) FindDevice(context.Context, string) (Device, error) {
	return f.device, f.err
}

func (f *fakeStore) RecordSession(_ context.Context, record Record) error {
	f.record = record
	return f.err
}

type fakeLiveKit struct {
	dispatched bool
	metadata   []byte
	err        error
}

func (f *fakeLiveKit) Dispatch(_ context.Context, _, _ string, metadata []byte) error {
	f.dispatched = true
	f.metadata = metadata
	return f.err
}

func (f *fakeLiveKit) MintToken(_, _ string, _ time.Duration) (string, error) {
	return "jwt", f.err
}

func TestCreateDispatchesAndRecordsOutboxEvent(t *testing.T) {
	profileID := uuid.MustParse("018f08d8-3f5d-7d5d-bd61-9b2ba12b58b8")
	store := &fakeStore{device: Device{
		ID: "esp32-respeaker", Identity: "esp32-respeaker",
		CredentialDigest: DigestSecret("correct"), ProfileID: profileID,
		AgentName: "sebastian", AgentConfig: json.RawMessage(`{"language":"es"}`),
	}}
	livekit := &fakeLiveKit{}
	service := NewService(store, livekit, "ws://livekit:7880", "sebastian", time.Hour, "esp32-respeaker")
	service.now = func() time.Time { return time.Date(2026, 7, 14, 10, 0, 0, 0, time.UTC) }

	created, err := service.Create(context.Background(), Credentials{DeviceID: "esp32-respeaker", Secret: "correct"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if !livekit.dispatched {
		t.Fatal("agent was not dispatched")
	}
	if created.Token != "jwt" || created.ExpiresAt.Sub(service.now()) != time.Hour {
		t.Fatalf("created = %#v", created)
	}
	if store.record.SessionID != created.ID || store.record.EventID == uuid.Nil {
		t.Fatalf("record = %#v", store.record)
	}
	if !json.Valid(livekit.metadata) {
		t.Fatalf("metadata is not JSON: %s", livekit.metadata)
	}
}

func TestCreateRejectsInvalidSecretBeforeDispatch(t *testing.T) {
	store := &fakeStore{device: Device{CredentialDigest: DigestSecret("correct")}}
	livekit := &fakeLiveKit{}
	service := NewService(store, livekit, "ws://livekit:7880", "sebastian", time.Hour, "esp32-respeaker")

	_, err := service.Create(context.Background(), Credentials{DeviceID: "esp32-respeaker", Secret: "wrong"})
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("Create() error = %v, want ErrUnauthorized", err)
	}
	if livekit.dispatched {
		t.Fatal("agent dispatched for invalid credentials")
	}
}
