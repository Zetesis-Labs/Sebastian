package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"
	"github.com/zetesis-labs/sebastian/server/internal/meeting"
)

type meetingModel struct {
	bun.BaseModel `bun:"table:meetings"`

	ID              uuid.UUID       `bun:"id,pk"`
	DeviceID        string          `bun:"device_id"`
	SessionID       *uuid.UUID      `bun:"session_id"`
	State           string          `bun:"state"`
	RequestedBy     string          `bun:"requested_by"`
	RequestedAt     time.Time       `bun:"requested_at"`
	StartedAt       *time.Time      `bun:"started_at"`
	EndedAt         *time.Time      `bun:"ended_at"`
	EndReason       *string         `bun:"end_reason"`
	AudioPath       *string         `bun:"audio_path"`
	AudioBytes      int64           `bun:"audio_bytes"`
	LastAudioAt     *time.Time      `bun:"last_audio_at"`
	DurationMs      int64           `bun:"duration_ms"`
	Keep            bool            `bun:"keep"`
	DeletedAt       *time.Time      `bun:"deleted_at"`
	Transcript      json.RawMessage `bun:"transcript,type:jsonb,nullzero"`
	TranscriptError *string         `bun:"transcript_error"`
	Summary         json.RawMessage `bun:"summary,type:jsonb,nullzero"`
	CreatedAt       time.Time       `bun:"created_at"`
	UpdatedAt       time.Time       `bun:"updated_at"`
}

// MeetingStore is the meetings side of the store (meeting.Store).
type MeetingStore struct{ db *bun.DB }

func (s *Store) Meetings() *MeetingStore { return &MeetingStore{db: s.db} }

func meetingToModel(m meeting.Meeting) meetingModel {
	return meetingModel{
		ID: m.ID, DeviceID: m.DeviceID, SessionID: m.SessionID, State: string(m.State),
		RequestedBy: string(m.RequestedBy), RequestedAt: m.RequestedAt,
		StartedAt: optTime(m.StartedAt), EndedAt: optTime(m.EndedAt), EndReason: optStr(string(m.EndReason)),
		AudioPath: optStr(m.AudioPath), AudioBytes: m.AudioBytes, LastAudioAt: optTime(m.LastAudioAt),
		DurationMs: m.DurationMs, Keep: m.Keep, DeletedAt: optTime(m.DeletedAt),
		Transcript: optJSON(m.Transcript), TranscriptError: optStr(m.TranscriptError), Summary: optJSON(m.Summary),
		CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt,
	}
}

func optJSON[T any](v *T) json.RawMessage {
	if v == nil {
		return nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("marshal meeting content: %v", err))
	}
	return raw
}

func (r meetingModel) toDomain() meeting.Meeting {
	m := meeting.Meeting{
		ID: r.ID, DeviceID: r.DeviceID, SessionID: r.SessionID, State: meeting.State(r.State),
		RequestedBy: meeting.Origin(r.RequestedBy), RequestedAt: r.RequestedAt,
		StartedAt: derefTime(r.StartedAt), EndedAt: derefTime(r.EndedAt), EndReason: meeting.EndReason(derefStr(r.EndReason)),
		AudioPath: derefStr(r.AudioPath), AudioBytes: r.AudioBytes, LastAudioAt: derefTime(r.LastAudioAt),
		DurationMs: r.DurationMs, Keep: r.Keep, DeletedAt: derefTime(r.DeletedAt),
		TranscriptError: derefStr(r.TranscriptError),
		CreatedAt:       r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
	if len(r.Transcript) > 0 {
		var t meeting.Transcript
		if json.Unmarshal(r.Transcript, &t) == nil {
			m.Transcript = &t
		}
	}
	if len(r.Summary) > 0 {
		var sum meeting.Summary
		if json.Unmarshal(r.Summary, &sum) == nil {
			m.Summary = &sum
		}
	}
	return m
}

func optTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func optStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func derefTime(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func meetingPayload(m meeting.Meeting) map[string]any {
	return map[string]any{
		"meeting_id": m.ID, "device_id": m.DeviceID, "state": m.State, "requested_by": m.RequestedBy,
		"end_reason": m.EndReason, "duration_ms": m.DurationMs, "audio_bytes": m.AudioBytes,
	}
}

func (s *MeetingStore) Insert(ctx context.Context, m meeting.Meeting) error {
	model := meetingToModel(m)
	err := s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if _, err := tx.NewInsert().Model(&model).Exec(ctx); err != nil {
			return err
		}
		return appendEvent(ctx, tx, domainEvent{AggregateType: "meeting", AggregateID: m.ID.String(), Type: "meeting.requested", Payload: mustJSON(meetingPayload(m)), At: m.CreatedAt})
	})
	if err != nil {
		return fmt.Errorf("insert meeting: %w", err)
	}
	return nil
}

func (s *MeetingStore) Update(ctx context.Context, m meeting.Meeting, event string) error {
	model := meetingToModel(m)
	err := s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if _, err := tx.NewUpdate().Model(&model).WherePK().Exec(ctx); err != nil {
			return err
		}
		if event == "" {
			return nil
		}
		return appendEvent(ctx, tx, domainEvent{AggregateType: "meeting", AggregateID: m.ID.String(), Type: event, Payload: mustJSON(meetingPayload(m)), At: m.UpdatedAt})
	})
	if err != nil {
		return fmt.Errorf("update meeting: %w", err)
	}
	return nil
}

func (s *MeetingStore) Get(ctx context.Context, id uuid.UUID) (meeting.Meeting, error) {
	var row meetingModel
	err := s.db.NewSelect().Model(&row).Where("id = ?", id).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return meeting.Meeting{}, meeting.ErrNotFound
	}
	if err != nil {
		return meeting.Meeting{}, fmt.Errorf("get meeting: %w", err)
	}
	return row.toDomain(), nil
}

func (s *MeetingStore) Active(ctx context.Context, deviceID string) (meeting.Meeting, bool, error) {
	var row meetingModel
	err := s.db.NewSelect().Model(&row).
		Where("device_id = ?", deviceID).
		Where("state IN (?)", bun.In([]string{"requested", "recording", "closing"})).
		OrderExpr("requested_at DESC").Limit(1).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return meeting.Meeting{}, false, nil
	}
	if err != nil {
		return meeting.Meeting{}, false, fmt.Errorf("active meeting: %w", err)
	}
	return row.toDomain(), true, nil
}

func (s *MeetingStore) InProgress(ctx context.Context) ([]meeting.Meeting, error) {
	var rows []meetingModel
	err := s.db.NewSelect().Model(&rows).
		Where("state IN (?)", bun.In([]string{"requested", "recording", "closing"})).
		OrderExpr("requested_at").Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("meetings in progress: %w", err)
	}
	return meetingsToDomain(rows), nil
}

func (s *MeetingStore) List(ctx context.Context, f meeting.Filter) ([]meeting.Meeting, error) {
	q := s.db.NewSelect().Model((*meetingModel)(nil)).OrderExpr("requested_at DESC").Limit(f.Limit)
	if f.DeviceID != "" {
		q = q.Where("device_id = ?", f.DeviceID)
	}
	if f.State != "" {
		q = q.Where("state = ?", string(f.State))
	}
	if f.Query != "" {
		like := "%" + f.Query + "%"
		q = q.WhereGroup(" AND ", func(q *bun.SelectQuery) *bun.SelectQuery {
			return q.Where("transcript->>'text' ILIKE ?", like).WhereOr("summary->>'text' ILIKE ?", like)
		})
	}
	var rows []meetingModel
	if err := q.Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("list meetings: %w", err)
	}
	return meetingsToDomain(rows), nil
}

func (s *MeetingStore) EndedBefore(ctx context.Context, before time.Time, limit int) ([]meeting.Meeting, error) {
	var rows []meetingModel
	err := s.db.NewSelect().Model(&rows).
		Where("state IN (?)", bun.In([]string{"ready", "no_transcript", "cut"})).
		Where("keep = false").Where("deleted_at IS NULL").
		Where("coalesce(ended_at, requested_at) < ?", before).
		OrderExpr("coalesce(ended_at, requested_at)").Limit(limit).Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("meetings ended before: %w", err)
	}
	return meetingsToDomain(rows), nil
}

func (s *MeetingStore) Drop(ctx context.Context, id uuid.UUID) error {
	if _, err := s.db.NewDelete().Model((*meetingModel)(nil)).Where("id = ?", id).Exec(ctx); err != nil {
		return fmt.Errorf("drop meeting: %w", err)
	}
	return nil
}

func (s *MeetingStore) Delete(ctx context.Context, id uuid.UUID, at time.Time) error {
	err := s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		res, err := tx.NewUpdate().Model((*meetingModel)(nil)).
			Set("deleted_at = ?", at).Set("audio_path = NULL").Set("transcript = NULL").
			Set("summary = NULL").Set("transcript_error = NULL").Set("updated_at = ?", at).
			Where("id = ?", id).Exec(ctx)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return meeting.ErrNotFound
		}
		return appendEvent(ctx, tx, domainEvent{AggregateType: "meeting", AggregateID: id.String(), Type: "meeting.deleted", Payload: mustJSON(map[string]any{"meeting_id": id}), At: at})
	})
	if err != nil && !errors.Is(err, meeting.ErrNotFound) {
		return fmt.Errorf("delete meeting: %w", err)
	}
	return err
}

func meetingsToDomain(rows []meetingModel) []meeting.Meeting {
	out := make([]meeting.Meeting, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.toDomain())
	}
	return out
}
