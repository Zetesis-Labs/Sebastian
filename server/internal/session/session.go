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
	ProfileID        uuid.UUID
	AgentName        string
	AgentConfig      json.RawMessage
}

type Credentials struct {
	DeviceID string
	Secret   string
	Legacy   bool
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
	legacyID   string
	now        func() time.Time
}

func NewService(store Store, livekit LiveKit, serverURL, roomPrefix string, tokenTTL time.Duration, legacyID string) *Service {
	return &Service{
		store:      store,
		livekit:    livekit,
		serverURL:  serverURL,
		roomPrefix: roomPrefix,
		tokenTTL:   tokenTTL,
		legacyID:   legacyID,
		now:        time.Now,
	}
}

func (s *Service) Create(ctx context.Context, credentials Credentials) (Created, error) {
	deviceID := credentials.DeviceID
	if credentials.Legacy {
		deviceID = s.legacyID
	}
	device, err := s.store.FindDevice(ctx, deviceID)
	if err != nil {
		if errors.Is(err, ErrUnauthorized) {
			return Created{}, ErrUnauthorized
		}
		return Created{}, fmt.Errorf("find device: %w", ErrUnavailable)
	}
	if !credentials.Legacy {
		digest := DigestSecret(credentials.Secret)
		if len(device.CredentialDigest) == 0 || subtle.ConstantTimeCompare(digest, device.CredentialDigest) != 1 {
			return Created{}, ErrUnauthorized
		}
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
	metadata, err := json.Marshal(struct {
		DeviceID  string          `json:"device_id"`
		ProfileID uuid.UUID       `json:"profile_id"`
		Config    json.RawMessage `json:"config"`
	}{DeviceID: device.ID, ProfileID: device.ProfileID, Config: device.AgentConfig})
	if err != nil {
		return Created{}, fmt.Errorf("encode dispatch metadata: %w", ErrUnavailable)
	}
	if err := s.livekit.Dispatch(ctx, room, device.AgentName, metadata); err != nil {
		return Created{}, fmt.Errorf("dispatch agent: %w", ErrUnavailable)
	}
	token, err := s.livekit.MintToken(room, device.Identity, s.tokenTTL)
	if err != nil {
		return Created{}, fmt.Errorf("mint token: %w", ErrUnavailable)
	}

	now := s.now().UTC()
	expiresAt := now.Add(s.tokenTTL)
	if err := s.store.RecordSession(ctx, Record{
		SessionID:  sessionID,
		DeviceID:   device.ID,
		ProfileID:  device.ProfileID,
		Room:       room,
		ExpiresAt:  expiresAt,
		EventID:    eventID,
		OccurredAt: now,
	}); err != nil {
		return Created{}, fmt.Errorf("record session: %w", ErrUnavailable)
	}

	return Created{ID: sessionID, Room: room, ServerURL: s.serverURL, Token: token, ExpiresAt: expiresAt}, nil
}
