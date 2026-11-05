package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/uptrace/bun"
	"github.com/zetesis-labs/sebastian/server/internal/session"
)

// Store deliberately keeps queries close to the domain operations. Bun provides
// parameter binding, typed scans, and transaction helpers without generating an
// application-wide query API for every table.
type Store struct {
	db *bun.DB
}

func NewStore(db *bun.DB) *Store {
	return &Store{db: db}
}

type sessionModel struct {
	bun.BaseModel `bun:"table:sessions"`

	ID             uuid.UUID `bun:",pk"`
	RoomName       string
	ExpiresAt      time.Time
	CreatedAt      time.Time
	AgentProfileID uuid.UUID
	DeviceID       string
}

type domainEventModel struct {
	bun.BaseModel `bun:"table:domain_events"`

	ID            uuid.UUID `bun:",pk"`
	AggregateType string
	AggregateID   string
	EventType     string
	EventVersion  int64
	Payload       json.RawMessage `bun:"type:jsonb"`
	OccurredAt    time.Time
}

type deviceRow struct {
	ID               string          `bun:"id"`
	Identity         string          `bun:"livekit_identity"`
	CredentialDigest []byte          `bun:"credential_digest"`
	PendingDigest    []byte          `bun:"pending_credential_digest"`
	ProfileID        uuid.UUID       `bun:"profile_id"`
	AgentName        string          `bun:"agent_name"`
	AgentConfig      json.RawMessage `bun:"agent_config"`
	MeetingSilence   int             `bun:"meeting_silence_min"`
}

func (s *Store) FindDevice(ctx context.Context, id string) (session.Device, error) {
	var row deviceRow
	err := s.db.NewSelect().
		TableExpr("devices AS d").
		ColumnExpr("d.id, d.livekit_identity, d.credential_digest, d.pending_credential_digest, p.id AS profile_id, p.agent_name, p.config AS agent_config, d.meeting_silence_min").
		Join("JOIN agent_profiles AS p ON p.id = d.agent_profile_id").
		Where("d.id = ?", id).
		Where("d.enabled = TRUE").
		Where("d.forgotten_at IS NULL").
		Scan(ctx, &row)
	if errors.Is(err, sql.ErrNoRows) {
		return session.Device{}, session.ErrUnauthorized
	}
	if err != nil {
		return session.Device{}, fmt.Errorf("query device: %w", err)
	}
	return session.Device{
		ID:                row.ID,
		Identity:          row.Identity,
		CredentialDigest:  row.CredentialDigest,
		PendingDigest:     row.PendingDigest,
		ProfileID:         row.ProfileID,
		AgentName:         row.AgentName,
		AgentConfig:       row.AgentConfig,
		MeetingSilenceMin: row.MeetingSilence,
	}, nil
}

func (s *Store) RecordSession(ctx context.Context, record session.Record) error {
	payload, err := json.Marshal(map[string]any{
		"session_id": record.SessionID,
		"device_id":  record.DeviceID,
		"profile_id": record.ProfileID,
		"room":       record.Room,
		"expires_at": record.ExpiresAt,
	})
	if err != nil {
		return fmt.Errorf("encode event: %w", err)
	}

	return s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if _, err := tx.NewInsert().Model(&sessionModel{
			ID:             record.SessionID,
			DeviceID:       record.DeviceID,
			AgentProfileID: record.ProfileID,
			RoomName:       record.Room,
			ExpiresAt:      record.ExpiresAt,
			CreatedAt:      record.OccurredAt,
		}).Exec(ctx); err != nil {
			return fmt.Errorf("insert session: %w", err)
		}
		return appendEvent(ctx, tx, domainEvent{
			ID: record.EventID, AggregateType: "session", AggregateID: record.SessionID.String(),
			Type: "session.created", Payload: payload, At: record.OccurredAt,
		})
	})
}

// domainEvent is what every state change of interest leaves behind (spec 12
// §12 observability): a row in domain_events, the audit trail of the control
// room, written in the same transaction as the change.
type domainEvent struct {
	ID            uuid.UUID // zero = generated
	AggregateType string
	AggregateID   string
	Type          string
	Payload       json.RawMessage
	At            time.Time
}

func deviceEvent(eventType, id string, payload map[string]any, at time.Time) domainEvent {
	return domainEvent{AggregateType: "device", AggregateID: id, Type: eventType, Payload: mustJSON(payload), At: at}
}

// mustJSON encodes a payload built from plain values: it cannot fail.
func mustJSON(payload map[string]any) json.RawMessage {
	body, _ := json.Marshal(payload)
	return body
}

func appendEvent(ctx context.Context, db bun.IDB, e domainEvent) error {
	if e.ID == uuid.Nil {
		id, err := uuid.NewV7()
		if err != nil {
			return fmt.Errorf("generate event id: %w", err)
		}
		e.ID = id
	}
	if _, err := db.NewInsert().Model(&domainEventModel{
		ID: e.ID, AggregateType: e.AggregateType, AggregateID: e.AggregateID,
		EventType: e.Type, EventVersion: 1, Payload: e.Payload, OccurredAt: e.At,
	}).Exec(ctx); err != nil {
		return fmt.Errorf("insert domain event %s: %w", e.Type, err)
	}
	return nil
}

func (s *Store) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
