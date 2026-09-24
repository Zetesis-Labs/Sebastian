package session

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

type fakeStore struct {
	confirmed []byte
	device    Device
	record    Record
	err       error
	recordErr error
}

func (f *fakeStore) FindDevice(context.Context, string) (Device, error) {
	return f.device, f.err
}

func (f *fakeStore) RecordSession(_ context.Context, record Record) error {
	f.record = record
	return f.recordErr
}

func (f *fakeStore) ConfirmSecret(_ context.Context, _ string, digest []byte) error {
	f.confirmed = digest
	return nil
}

type fakeLiveKit struct {
	dispatched bool
	metadata   []byte
	err        error
	mintErr    error
}

func (f *fakeLiveKit) Dispatch(_ context.Context, _, _ string, metadata []byte) error {
	f.dispatched = true
	f.metadata = metadata
	return f.err
}

func (f *fakeLiveKit) MintToken(_, _ string, _ time.Duration) (string, error) {
	return "jwt", f.mintErr
}

func TestCreateDispatchesAndRecordsOutboxEvent(t *testing.T) {
	profileID := uuid.MustParse("018f08d8-3f5d-7d5d-bd61-9b2ba12b58b8")
	store := &fakeStore{device: Device{
		ID: "esp32-respeaker", Identity: "esp32-respeaker",
		CredentialDigest: DigestSecret("correct"), ProfileID: profileID,
		AgentName: "sebastian", AgentConfig: json.RawMessage(`{"language":"es"}`),
	}}
	livekit := &fakeLiveKit{}
	service := NewService(store, livekit, "ws://livekit:7880", "sebastian", time.Hour)
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

// A unit that opens a meeting right after a conversation creates two sessions
// seconds apart; UUIDv7 ids share their first 8 hex digits for ~65 s, and a
// room named from that prefix collided on sessions_room_name_key (503).
func TestCreateGivesBackToBackSessionsDistinctRooms(t *testing.T) {
	store := &fakeStore{device: Device{
		ID: "e072a1f96ef0", Identity: "e072a1f96ef0",
		CredentialDigest: DigestSecret("correct"), ProfileID: uuid.New(), AgentName: "sebastian",
	}}
	service := NewService(store, &fakeLiveKit{}, "ws://livekit:7880", "sebastian", time.Hour)
	credentials := Credentials{DeviceID: "e072a1f96ef0", Secret: "correct"}

	first, err := service.Create(context.Background(), credentials)
	if err != nil {
		t.Fatalf("first Create() error = %v", err)
	}
	second, err := service.Create(context.Background(), credentials)
	if err != nil {
		t.Fatalf("second Create() error = %v", err)
	}
	if first.Room == second.Room {
		t.Fatalf("back-to-back sessions share room %q", first.Room)
	}
	if !strings.HasPrefix(second.Room, "sebastian-") {
		t.Fatalf("room %q lost the prefix the control plane filters on", second.Room)
	}
}

func TestCreateRejectsInvalidSecretBeforeDispatch(t *testing.T) {
	store := &fakeStore{device: Device{CredentialDigest: DigestSecret("correct")}}
	livekit := &fakeLiveKit{}
	service := NewService(store, livekit, "ws://livekit:7880", "sebastian", time.Hour)

	_, err := service.Create(context.Background(), Credentials{DeviceID: "esp32-respeaker", Secret: "wrong"})
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("Create() error = %v, want ErrUnauthorized", err)
	}
	if livekit.dispatched {
		t.Fatal("agent dispatched for invalid credentials")
	}
}

func TestCreateDoesNotDispatchWhenRecordFails(t *testing.T) {
	store := &fakeStore{
		device: Device{
			ID: "esp32-respeaker", Identity: "esp32-respeaker",
			CredentialDigest: DigestSecret("correct"), ProfileID: uuid.New(),
			AgentName: "sebastian",
		},
		recordErr: errors.New("db down"),
	}
	livekit := &fakeLiveKit{}
	service := NewService(store, livekit, "ws://livekit:7880", "sebastian", time.Hour)

	_, err := service.Create(context.Background(), Credentials{DeviceID: "esp32-respeaker", Secret: "correct"})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Create() error = %v, want ErrUnavailable", err)
	}
	if livekit.dispatched {
		t.Fatal("agent dispatched although session was not recorded")
	}
}

func TestCreateDoesNotDispatchWhenMintFails(t *testing.T) {
	store := &fakeStore{device: Device{
		ID: "esp32-respeaker", Identity: "esp32-respeaker",
		CredentialDigest: DigestSecret("correct"), ProfileID: uuid.New(),
		AgentName: "sebastian",
	}}
	livekit := &fakeLiveKit{mintErr: errors.New("bad signing key")}
	service := NewService(store, livekit, "ws://livekit:7880", "sebastian", time.Hour)

	_, err := service.Create(context.Background(), Credentials{DeviceID: "esp32-respeaker", Secret: "correct"})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Create() error = %v, want ErrUnavailable", err)
	}
	if livekit.dispatched {
		t.Fatal("agent dispatched although token mint failed")
	}
}

func TestCreateAcceptsThePendingSecretAndConfirmsIt(t *testing.T) {
	store := &fakeStore{device: Device{ID: "68ee", Identity: "68ee", CredentialDigest: DigestSecret("old"), PendingDigest: DigestSecret("new"), ProfileID: uuid.New()}}
	livekit := &fakeLiveKit{}
	service := NewService(store, livekit, "ws://lk", "sebastian", time.Hour)

	if _, err := service.Create(context.Background(), Credentials{DeviceID: "68ee", Secret: "new"}); err != nil {
		t.Fatalf("pending secret must open a session: %v", err)
	}
	if string(store.confirmed) != string(DigestSecret("new")) {
		t.Fatal("first use of the pending secret must confirm it")
	}
	if _, err := service.Create(context.Background(), Credentials{DeviceID: "68ee", Secret: "stranger"}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("expected ErrUnauthorized, got %v", err)
	}
}

// Design 14 §3.2: a meeting session dispatches the agent in meeting mode.
func TestDispatchMetadataCarriesTheMeetingMode(t *testing.T) {
	dev := Device{ID: "68ee", ProfileID: uuid.New(), AgentConfig: json.RawMessage(`{"language":"es"}`)}
	plain := dispatchMetadata(dev, Credentials{DeviceID: "68ee"})
	if _, has := plain["mode"]; has || plain["device_id"] != "68ee" {
		t.Fatalf("a conversation carries no mode: %v", plain)
	}
	id := uuid.New()
	dev.MeetingSilenceMin = 15
	meeting := dispatchMetadata(dev, Credentials{DeviceID: "68ee", Kind: KindMeeting, MeetingID: id})
	if meeting["mode"] != "meeting" || meeting["meeting_id"] != id || meeting["silence_s"] != 900 {
		t.Fatalf("meeting metadata: %v", meeting)
	}
}
