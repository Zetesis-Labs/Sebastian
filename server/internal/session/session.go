package session

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

var (
	ErrUnauthorized = errors.New("invalid device credentials")
	ErrUnavailable  = errors.New("session service unavailable")
)

type Device struct {
	ID               string
	Identity         string
	CredentialDigest []byte
	PendingDigest    []byte // regenerated secret not yet confirmed by the device
	ProfileID        uuid.UUID
	AgentName        string
	AgentConfig      json.RawMessage
}

// Kind is what the session is for: a conversation with the agent, or a
// meeting recording (docs/implementation/14 §3.2) — the agent reads it from
// the dispatch metadata.
type Kind string

const (
	KindConversation Kind = "conversation"
	KindMeeting      Kind = "meeting"
)

type Credentials struct {
	DeviceID  string
	Secret    string
	Kind      Kind
	MeetingID uuid.UUID // with KindMeeting
}

type Created struct {
	ID        uuid.UUID
	Room      string
	ServerURL string
	Token     string
	ExpiresAt time.Time
}

type Record struct {
	SessionID  uuid.UUID
	DeviceID   string
	ProfileID  uuid.UUID
	Room       string
	ExpiresAt  time.Time
	EventID    uuid.UUID
	OccurredAt time.Time
}

type Store interface {
	FindDevice(context.Context, string) (Device, error)
	RecordSession(context.Context, Record) error
	// ConfirmSecret promotes a regenerated secret the first time the device uses it.
	ConfirmSecret(ctx context.Context, id string, digest []byte) error
}

type LiveKit interface {
	Dispatch(context.Context, string, string, []byte) error
	MintToken(string, string, time.Duration) (string, error)
}

type Service struct {
	store      Store
	livekit    LiveKit
	serverURL  string
	roomPrefix string
	tokenTTL   time.Duration
	now        func() time.Time
}

func NewService(store Store, livekit LiveKit, serverURL, roomPrefix string, tokenTTL time.Duration) *Service {
	return &Service{
		store:      store,
		livekit:    livekit,
		serverURL:  serverURL,
		roomPrefix: roomPrefix,
		tokenTTL:   tokenTTL,
		now:        time.Now,
	}
}

func (s *Service) Create(ctx context.Context, credentials Credentials) (Created, error) {
	device, err := s.store.FindDevice(ctx, credentials.DeviceID)
	if err != nil {
		if errors.Is(err, ErrUnauthorized) {
			return Created{}, ErrUnauthorized
		}
		return Created{}, fmt.Errorf("find device: %w", ErrUnavailable)
	}
	digest := DigestSecret(credentials.Secret)
	switch {
	case len(device.CredentialDigest) > 0 && subtle.ConstantTimeCompare(digest, device.CredentialDigest) == 1:
	case len(device.PendingDigest) > 0 && subtle.ConstantTimeCompare(digest, device.PendingDigest) == 1:
		if err := s.store.ConfirmSecret(ctx, device.ID, digest); err != nil {
			return Created{}, fmt.Errorf("confirm secret: %w: %w", ErrUnavailable, err)
		}
	default:
		return Created{}, ErrUnauthorized
	}

	sessionID, err := uuid.NewV7()
	if err != nil {
		return Created{}, fmt.Errorf("generate session id: %w", ErrUnavailable)
	}
	eventID, err := uuid.NewV7()
	if err != nil {
		return Created{}, fmt.Errorf("generate event id: %w", ErrUnavailable)
	}
	room := fmt.Sprintf("%s-%s", s.roomPrefix, sessionID.String()[:8])
	now := s.now().UTC()
	expiresAt := now.Add(s.tokenTTL)
	// Persist the session BEFORE any LiveKit side effect. If the record fails we
	// return here having dispatched nothing — the alternative (record last) leaves
	// a dispatched agent with no session row: a zombie in the room.
	if err := s.store.RecordSession(ctx, Record{
		SessionID:  sessionID,
		DeviceID:   device.ID,
		ProfileID:  device.ProfileID,
		Room:       room,
		ExpiresAt:  expiresAt,
		EventID:    eventID,
		OccurredAt: now,
	}); err != nil {
		// Keep the store error in the chain: swallowing it turned every storage
		// fault into an opaque 503 with nothing to debug from.
		return Created{}, fmt.Errorf("record session: %w: %w", ErrUnavailable, err)
	}

	metadata, err := json.Marshal(dispatchMetadata(device, credentials))
	if err != nil {
		return Created{}, fmt.Errorf("encode dispatch metadata: %w", ErrUnavailable)
	}
	// Mint before dispatch, and dispatch LAST: minting is a local, near-infallible
	// op; dispatch is the irreversible side effect. Doing it last means a failure in
	// record/marshal/mint leaves no agent in the room — closing the residual zombie
	// window that record-first alone leaves open (mint failing after dispatch).
	token, err := s.livekit.MintToken(room, device.Identity, s.tokenTTL)
	if err != nil {
		return Created{}, fmt.Errorf("mint token: %w", ErrUnavailable)
	}
	if err := s.livekit.Dispatch(ctx, room, device.AgentName, metadata); err != nil {
		return Created{}, fmt.Errorf("dispatch agent: %w", ErrUnavailable)
	}

	return Created{ID: sessionID, Room: room, ServerURL: s.serverURL, Token: token, ExpiresAt: expiresAt}, nil
}

// dispatchMetadata is what the agent learns about the job: the unit, its
// profile and, for a meeting, the mode and the meeting to feed (design 14 §3.2).
func dispatchMetadata(device Device, credentials Credentials) map[string]any {
	out := map[string]any{"device_id": device.ID, "profile_id": device.ProfileID, "config": device.AgentConfig}
	if credentials.Kind == KindMeeting {
		out["mode"] = "meeting"
		if credentials.MeetingID != uuid.Nil {
			out["meeting_id"] = credentials.MeetingID
		}
	}
	return out
}
