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
	"github.com/zetesis-labs/sebastian/server/internal/outbox"
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

type outboxEventModel struct {
	bun.BaseModel `bun:"table:outbox_events"`

	ID            uuid.UUID `bun:",pk"`
	Subject       string
	Payload       json.RawMessage `bun:"type:jsonb"`
	CreatedAt     time.Time
	PublishedAt   *time.Time
	Attempts      int64
	LastError     *string
	EventID       uuid.UUID
	NextAttemptAt time.Time
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
	ProfileID        uuid.UUID       `bun:"profile_id"`
	AgentName        string          `bun:"agent_name"`
	AgentConfig      json.RawMessage `bun:"agent_config"`
}

func (s *Store) FindDevice(ctx context.Context, id string) (session.Device, error) {
	var row deviceRow
	err := s.db.NewSelect().
		TableExpr("devices AS d").
		ColumnExpr("d.id, d.livekit_identity, d.credential_digest, p.id AS profile_id, p.agent_name, p.config AS agent_config").
		Join("JOIN agent_profiles AS p ON p.id = d.agent_profile_id").
		Where("d.id = ?", id).
		Where("d.enabled = TRUE").
		Scan(ctx, &row)
	if errors.Is(err, sql.ErrNoRows) {
		return session.Device{}, session.ErrUnauthorized
	}
	if err != nil {
		return session.Device{}, fmt.Errorf("query device: %w", err)
	}
	return session.Device{
		ID:               row.ID,
		Identity:         row.Identity,
		CredentialDigest: row.CredentialDigest,
		ProfileID:        row.ProfileID,
		AgentName:        row.AgentName,
		AgentConfig:      row.AgentConfig,
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
		if _, err := tx.NewInsert().Model(&domainEventModel{
			ID:            record.EventID,
			AggregateType: "session",
			AggregateID:   record.SessionID.String(),
			EventType:     "session.created",
			EventVersion:  1,
			Payload:       payload,
			OccurredAt:    record.OccurredAt,
		}).Exec(ctx); err != nil {
			return fmt.Errorf("insert domain event: %w", err)
		}
		if _, err := tx.NewInsert().Model(&outboxEventModel{
			ID:            record.EventID,
			EventID:       record.EventID,
			Subject:       "evt.sebastian.v1.session.created",
			Payload:       payload,
			CreatedAt:     record.OccurredAt,
			NextAttemptAt: record.OccurredAt,
		}).Exec(ctx); err != nil {
			return fmt.Errorf("insert outbox event: %w", err)
		}
		return nil
	})
}

func (s *Store) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

type claimedOutboxEvent struct {
	ID            uuid.UUID       `bun:"id"`
	EventID       uuid.UUID       `bun:"event_id"`
	Subject       string          `bun:"subject"`
	Payload       json.RawMessage `bun:"payload"`
	Attempts      int64           `bun:"attempts"`
	EventType     string          `bun:"event_type"`
	AggregateType string          `bun:"aggregate_type"`
	AggregateID   string          `bun:"aggregate_id"`
	OccurredAt    time.Time       `bun:"occurred_at"`
}

func (s *Store) ProcessPending(
	ctx context.Context,
	now time.Time,
	limit int,
	publisher outbox.Publisher,
) (outbox.BatchResult, error) {
	var result outbox.BatchResult
	err := s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		var claimed []claimedOutboxEvent
		if err := tx.NewSelect().
			TableExpr("outbox_events AS o").
			ColumnExpr("o.id, o.event_id, o.subject, o.payload, o.attempts, d.event_type, d.aggregate_type, d.aggregate_id, d.occurred_at").
			Join("JOIN domain_events AS d ON d.id = o.event_id").
			Where("o.published_at IS NULL").
			Where("o.next_attempt_at <= ?", now).
			OrderExpr("o.created_at ASC").
			Limit(limit).
			For("UPDATE SKIP LOCKED").
			Scan(ctx, &claimed); err != nil {
			return fmt.Errorf("claim outbox events: %w", err)
		}

		result.Attempted = len(claimed)
		for _, item := range claimed {
			event := outbox.Event{
				ID:            item.EventID,
				Type:          item.EventType,
				AggregateType: item.AggregateType,
				AggregateID:   item.AggregateID,
				Subject:       item.Subject,
				OccurredAt:    item.OccurredAt,
				Data:          item.Payload,
			}
			attempts := item.Attempts + 1
			if err := publisher.Publish(ctx, event); err != nil {
				if _, updateErr := tx.NewUpdate().
					TableExpr("outbox_events").
					Set("attempts = ?", attempts).
					Set("last_error = ?", truncateError(err)).
					Set("next_attempt_at = ?", now.Add(outbox.RetryDelay(int(attempts)))).
					Where("id = ?", item.ID).
					Exec(ctx); updateErr != nil {
					return fmt.Errorf("record outbox failure %s: %w", item.ID, updateErr)
				}
				result.Failed++
				continue
			}
			if _, err := tx.NewUpdate().
				TableExpr("outbox_events").
				Set("attempts = ?", attempts).
				Set("published_at = ?", now).
				Set("last_error = NULL").
				Where("id = ?", item.ID).
				Exec(ctx); err != nil {
				return fmt.Errorf("mark outbox event %s published: %w", item.ID, err)
			}
			result.Published++
		}
		return nil
	})
	if err != nil {
		return result, err
	}
	return result, nil
}

func truncateError(err error) string {
	const maxRunes = 2048
	message := []rune(err.Error())
	if len(message) > maxRunes {
		message = message[:maxRunes]
	}
	return string(message)
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
		if _, err := tx.NewInsert().Model(&domainEventModel{
			ID:            record.EventID,
			AggregateType: "recording",
			AggregateID:   record.ID.String(),
			EventType:     "recording.registered",
			EventVersion:  1,
			Payload:       payload,
			OccurredAt:    record.Now,
		}).Exec(ctx); err != nil {
			return fmt.Errorf("insert recording event: %w", err)
		}
		if _, err := tx.NewInsert().Model(&outboxEventModel{
			ID:            record.EventID,
			EventID:       record.EventID,
			Subject:       "evt.sebastian.v1.recording.registered",
			Payload:       payload,
			CreatedAt:     record.Now,
			NextAttemptAt: record.Now,
		}).Exec(ctx); err != nil {
			return fmt.Errorf("insert recording outbox event: %w", err)
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
