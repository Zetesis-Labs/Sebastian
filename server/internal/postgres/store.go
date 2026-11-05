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
	"github.com/zetesis-labs/sebastian/server/internal/recording"
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

type recordingModel struct {
	bun.BaseModel `bun:"table:recordings"`

	ID          uuid.UUID `bun:",pk"`
	Kind        string
	FileName    string
	ObjectURL   string
	ContentType string
	ByteSize    int64
	DurationMs  int64
	CapturedAt  time.Time
	CreatedAt   time.Time
	Transcript  *string
	SessionID   uuid.UUID
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

type recordingRow struct {
	ID          uuid.UUID `bun:"id"`
	SessionID   uuid.UUID `bun:"session_id"`
	Room        string    `bun:"room"`
	Kind        string    `bun:"kind"`
	FileName    string    `bun:"file_name"`
	ObjectURL   string    `bun:"object_url"`
	ContentType string    `bun:"content_type"`
	ByteSize    int64     `bun:"byte_size"`
	DurationMs  int64     `bun:"duration_ms"`
	CapturedAt  time.Time `bun:"captured_at"`
	CreatedAt   time.Time `bun:"created_at"`
	Transcript  *string   `bun:"transcript"`
}

func (s *Store) ListRecordings(ctx context.Context, limit int) ([]recording.Recording, error) {
	var rows []recordingRow
	if err := s.recordingsQuery(s.db.NewSelect()).Limit(limit).Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("query recordings: %w", err)
	}
	result := make([]recording.Recording, 0, len(rows))
	for _, row := range rows {
		result = append(result, mapRecording(row))
	}
	return result, nil
}

func (s *Store) GetRecording(ctx context.Context, id uuid.UUID) (recording.Recording, error) {
	var row recordingRow
	if err := s.recordingsQuery(s.db.NewSelect()).Where("r.id = ?", id).Scan(ctx, &row); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return recording.Recording{}, recording.ErrNotFound
		}
		return recording.Recording{}, fmt.Errorf("query recording: %w", err)
	}
	return mapRecording(row), nil
}

func (s *Store) recordingsQuery(query *bun.SelectQuery) *bun.SelectQuery {
	return query.
		TableExpr("recordings AS r").
		ColumnExpr("r.id, r.session_id, s.room_name AS room, r.kind, r.file_name, r.object_url, r.content_type, r.byte_size, r.duration_ms, r.captured_at, r.created_at, r.transcript").
		Join("JOIN sessions AS s ON s.id = r.session_id").
		OrderExpr("r.captured_at DESC, r.id DESC")
}

func (s *Store) CreateRecording(ctx context.Context, record recording.CreateRecord) (recording.Recording, error) {
	var created recordingRow
	err := s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		var sessionRow struct {
			ID       uuid.UUID `bun:"id"`
			RoomName string    `bun:"room_name"`
		}
		if err := tx.NewSelect().
			TableExpr("sessions").
			ColumnExpr("id, room_name").
			Where("room_name = ?", record.Input.Room).
			Scan(ctx, &sessionRow); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return recording.ErrNotFound
			}
			return fmt.Errorf("query recording session: %w", err)
		}
		model := recordingModel{
			ID:          record.ID,
			SessionID:   sessionRow.ID,
			Kind:        string(record.Input.Kind),
			FileName:    record.Input.FileName,
			ObjectURL:   record.Input.ObjectURL,
			ContentType: record.Input.ContentType,
			ByteSize:    record.Input.ByteSize,
			DurationMs:  record.Input.DurationMs,
			CapturedAt:  record.Input.CapturedAt.UTC(),
			CreatedAt:   record.Now,
			Transcript:  record.Input.Transcript,
		}
		if _, err := tx.NewInsert().Model(&model).Exec(ctx); err != nil {
			if isUniqueViolation(err) {
				return recording.ErrConflict
			}
			return fmt.Errorf("insert recording: %w", err)
		}
		payload, err := json.Marshal(map[string]any{
			"recording_id": record.ID,
			"session_id":   sessionRow.ID,
			"room":         sessionRow.RoomName,
			"kind":         record.Input.Kind,
			"object_url":   record.Input.ObjectURL,
			"byte_size":    record.Input.ByteSize,
			"duration_ms":  record.Input.DurationMs,
			"captured_at":  record.Input.CapturedAt.UTC(),
		})
		if err != nil {
			return fmt.Errorf("encode recording event: %w", err)
		}
		if err := appendEvent(ctx, tx, domainEvent{
			ID: record.EventID, AggregateType: "recording", AggregateID: record.ID.String(),
			Type: "recording.registered", Payload: payload, At: record.Now,
		}); err != nil {
			return err
		}
		created = recordingRow{
			ID: record.ID, SessionID: sessionRow.ID, Room: sessionRow.RoomName,
			Kind: model.Kind, FileName: model.FileName, ObjectURL: model.ObjectURL,
			ContentType: model.ContentType, ByteSize: model.ByteSize, DurationMs: model.DurationMs,
			CapturedAt: model.CapturedAt, CreatedAt: model.CreatedAt, Transcript: model.Transcript,
		}
		return nil
	})
	if err != nil {
		return recording.Recording{}, err
	}
	return mapRecording(created), nil
}

func (s *Store) RecordingsSummary(ctx context.Context) (recording.Summary, error) {
	var summary recording.Summary
	var lastCapturedAt sql.NullTime
	if err := s.db.QueryRowContext(ctx, `
		SELECT count(*), coalesce(sum(byte_size), 0), coalesce(sum(duration_ms), 0), max(captured_at)
		FROM recordings
	`).Scan(&summary.Count, &summary.TotalBytes, &summary.TotalDurationMs, &lastCapturedAt); err != nil {
		return recording.Summary{}, fmt.Errorf("aggregate recordings: %w", err)
	}
	if lastCapturedAt.Valid {
		value := lastCapturedAt.Time.UTC()
		summary.LastCapturedAt = &value
	}
	return summary, nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func mapRecording(row recordingRow) recording.Recording {
	return recording.Recording{
		ID: row.ID, SessionID: row.SessionID, Room: row.Room, Kind: recording.Kind(row.Kind),
		FileName: row.FileName, ObjectURL: row.ObjectURL, ContentType: row.ContentType,
		ByteSize: row.ByteSize, DurationMs: row.DurationMs, CapturedAt: row.CapturedAt,
		CreatedAt: row.CreatedAt, Transcript: row.Transcript,
	}
}
