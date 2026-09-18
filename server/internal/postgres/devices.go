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
	"github.com/zetesis-labs/sebastian/server/internal/device"
)

type adminDeviceRow struct {
	ID                    string     `bun:"id"`
	DisplayName           string     `bun:"display_name"`
	Enabled               bool       `bun:"enabled"`
	DesiredProfile        *string    `bun:"desired_device_profile"`
	ReportedProfile       *string    `bun:"reported_device_profile"`
	ProfileReportedAt     *time.Time `bun:"profile_reported_at"`
	Adopted               bool       `bun:"adopted"`
	AdoptedAt             *time.Time `bun:"adopted_at"`
	ReportedFirmware      *string    `bun:"reported_firmware"`
	ReportedConfigVersion *string    `bun:"reported_config_version"`
	DesiredConfigVersion  *string    `bun:"desired_config_version"`
	LastEvent             *string    `bun:"last_event"`
	LastEventAt           *time.Time `bun:"last_event_at"`
}

const adminDeviceColumns = `id, display_name, enabled, desired_device_profile, reported_device_profile,
	profile_reported_at, credential_digest IS NOT NULL AS adopted, adopted_at, reported_firmware,
	reported_config_version, desired_config_version, last_event, last_event_at`

func (r adminDeviceRow) toDomain() device.Device {
	item := device.Device{ID: r.ID, DisplayName: r.DisplayName, Enabled: r.Enabled, Adopted: r.Adopted}
	if r.DesiredProfile != nil {
		item.DesiredProfile = *r.DesiredProfile
	}
	if r.ReportedProfile != nil {
		item.ReportedProfile = *r.ReportedProfile
	}
	if r.ProfileReportedAt != nil {
		item.ProfileReportedAt = *r.ProfileReportedAt
	}
	if r.AdoptedAt != nil {
		item.AdoptedAt = *r.AdoptedAt
	}
	if r.ReportedFirmware != nil {
		item.ReportedFirmware = *r.ReportedFirmware
	}
	if r.ReportedConfigVersion != nil {
		item.ReportedConfig = *r.ReportedConfigVersion
	}
	if r.DesiredConfigVersion != nil {
		item.DesiredConfig = *r.DesiredConfigVersion
	}
	if r.LastEvent != nil {
		item.LastEvent = *r.LastEvent
	}
	if r.LastEventAt != nil {
		item.LastEventAt = *r.LastEventAt
	}
	return item
}

// TouchPoll upserts the device row on every reconciliation poll: first contact
// auto-registers the unit (no agent profile — it cannot open LiveKit sessions
// until adopted or assigned one), later polls refresh what it reports. A
// forgotten unit that polls again is simply re-registered. The event the unit
// reports (RF-36/42) replaces the stored one only when it carries one; its
// timestamp is the first poll that carried that value.
func (s *Store) TouchPoll(ctx context.Context, poll device.Poll) (device.PollResult, error) {
	var row struct {
		DesiredProfile string `bun:"desired_profile"`
		DesiredConfig  string `bun:"desired_config"`
	}
	now := time.Now().UTC()
	err := s.db.NewRaw(`
		INSERT INTO devices (id, display_name, livekit_identity, enabled, created_at, updated_at,
		                     reported_device_profile, profile_reported_at, reported_config_version, reported_firmware,
		                     last_event, last_event_at)
		VALUES (?, ?, ?, TRUE, ?, ?, NULLIF(?, ''), ?, NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), ?)
		ON CONFLICT (id) DO UPDATE SET
			reported_device_profile = EXCLUDED.reported_device_profile,
			profile_reported_at = EXCLUDED.profile_reported_at,
			reported_config_version = EXCLUDED.reported_config_version,
			reported_firmware = COALESCE(EXCLUDED.reported_firmware, devices.reported_firmware),
			last_event = COALESCE(EXCLUDED.last_event, devices.last_event),
			last_event_at = CASE
				WHEN EXCLUDED.last_event IS NULL OR EXCLUDED.last_event IS NOT DISTINCT FROM devices.last_event
				THEN devices.last_event_at ELSE EXCLUDED.last_event_at END,
			forgotten_at = NULL,
			updated_at = EXCLUDED.updated_at
		RETURNING COALESCE(desired_device_profile, '') AS desired_profile,
		          COALESCE(desired_config_version, '') AS desired_config`,
		poll.ID, poll.ID, poll.ID, now, now, poll.Reported, now, poll.ConfigVersion, poll.Firmware, poll.Event, now,
	).Scan(ctx, &row)
	if err != nil {
		return device.PollResult{}, fmt.Errorf("touch device poll: %w", err)
	}
	return device.PollResult{DesiredProfile: row.DesiredProfile, DesiredConfigVersion: row.DesiredConfig}, nil
}

func (s *Store) List(ctx context.Context) ([]device.Device, error) {
	var rows []adminDeviceRow
	err := s.db.NewSelect().
		TableExpr("devices").
		ColumnExpr(adminDeviceColumns).
		Where("forgotten_at IS NULL").
		OrderExpr("id").
		Scan(ctx, &rows)
	if err != nil {
		return nil, fmt.Errorf("list devices: %w", err)
	}
	devices := make([]device.Device, 0, len(rows))
	for _, row := range rows {
		devices = append(devices, row.toDomain())
	}
	return devices, nil
}

func (s *Store) Get(ctx context.Context, id string) (device.Device, error) {
	var row adminDeviceRow
	err := s.db.NewSelect().
		TableExpr("devices").
		ColumnExpr(adminDeviceColumns).
		Where("id = ?", id).
		Where("forgotten_at IS NULL").
		Scan(ctx, &row)
	if errors.Is(err, sql.ErrNoRows) {
		return device.Device{}, device.ErrNotFound
	}
	if err != nil {
		return device.Device{}, fmt.Errorf("get device: %w", err)
	}
	return row.toDomain(), nil
}

func (s *Store) Sessions(ctx context.Context, id string) ([]device.Session, error) {
	var rows []struct {
		ID             uuid.UUID `bun:"id"`
		Room           string    `bun:"room_name"`
		CreatedAt      time.Time `bun:"created_at"`
		ExpiresAt      time.Time `bun:"expires_at"`
		RecordingCount int64     `bun:"recording_count"`
	}
	err := s.db.NewRaw(`
		SELECT s.id, s.room_name, s.created_at, s.expires_at,
		       (SELECT COUNT(*) FROM recordings r WHERE r.session_id = s.id) AS recording_count
		FROM sessions s
		WHERE s.device_id = ?
		ORDER BY s.created_at DESC
		LIMIT 50`, id).Scan(ctx, &rows)
	if err != nil {
		return nil, fmt.Errorf("device sessions: %w", err)
	}
	out := make([]device.Session, 0, len(rows))
	for _, r := range rows {
		out = append(out, device.Session{ID: r.ID, Room: r.Room, CreatedAt: r.CreatedAt, ExpiresAt: r.ExpiresAt, RecordingCount: r.RecordingCount})
	}
	return out, nil
}

func (s *Store) updateDevice(ctx context.Context, id string, apply func(*bun.UpdateQuery) *bun.UpdateQuery) error {
	return s.updateDeviceIn(ctx, s.db, id, apply)
}

func (s *Store) updateDeviceIn(ctx context.Context, db bun.IDB, id string, apply func(*bun.UpdateQuery) *bun.UpdateQuery) error {
	q := db.NewUpdate().Table("devices").Set("updated_at = ?", time.Now().UTC()).Where("id = ?", id).Where("forgotten_at IS NULL")
	result, err := apply(q).Exec(ctx)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return device.ErrNotFound
	}
	return nil
}

func (s *Store) SetDesiredProfile(ctx context.Context, id, name string) error {
	err := s.updateDevice(ctx, id, func(q *bun.UpdateQuery) *bun.UpdateQuery {
		return q.Set("desired_device_profile = NULLIF(?, '')", name)
	})
	if err != nil && !errors.Is(err, device.ErrNotFound) {
		return fmt.Errorf("set desired profile: %w", err)
	}
	return err
}

func (s *Store) DesiredConfig(ctx context.Context, id string) (json.RawMessage, string, error) {
	var row struct {
		Config  json.RawMessage `bun:"desired_config"`
		Version *string         `bun:"desired_config_version"`
	}
	err := s.db.NewSelect().TableExpr("devices").ColumnExpr("desired_config, desired_config_version").
		Where("id = ?", id).Where("forgotten_at IS NULL").Scan(ctx, &row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", device.ErrNotFound
	}
	if err != nil {
		return nil, "", fmt.Errorf("desired config: %w", err)
	}
	version := ""
	if row.Version != nil {
		version = *row.Version
	}
	if len(row.Config) == 0 || string(row.Config) == "null" {
		return nil, version, nil
	}
	return row.Config, version, nil
}

func (s *Store) SetDesiredConfig(ctx context.Context, id string, config json.RawMessage, version string) error {
	err := s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if err := s.updateDeviceIn(ctx, tx, id, func(q *bun.UpdateQuery) *bun.UpdateQuery {
			return q.Set("desired_config = ?::jsonb", string(config)).Set("desired_config_version = ?", version)
		}); err != nil {
			return err
		}
		return appendEvent(ctx, tx, deviceEvent("device.config_desired", id, map[string]any{"device_id": id, "version": version}, time.Now().UTC()))
	})
	if err != nil && !errors.Is(err, device.ErrNotFound) {
		return fmt.Errorf("set desired config: %w", err)
	}
	return err
}

func (s *Store) ClearDesiredConfig(ctx context.Context, id string) error {
	err := s.updateDevice(ctx, id, func(q *bun.UpdateQuery) *bun.UpdateQuery {
		return q.Set("desired_config = NULL").Set("desired_config_version = NULL")
	})
	if err != nil && !errors.Is(err, device.ErrNotFound) {
		return fmt.Errorf("clear desired config: %w", err)
	}
	return err
}

func (s *Store) RunningConfig(ctx context.Context, id string) (json.RawMessage, time.Time, error) {
	var row struct {
		Config json.RawMessage `bun:"running_config"`
		At     *time.Time      `bun:"running_config_at"`
	}
	err := s.db.NewSelect().TableExpr("devices").ColumnExpr("running_config, running_config_at").
		Where("id = ?", id).Where("forgotten_at IS NULL").Scan(ctx, &row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, time.Time{}, device.ErrNotFound
	}
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("running config: %w", err)
	}
	if row.At == nil {
		return nil, time.Time{}, nil
	}
	return row.Config, *row.At, nil
}

func (s *Store) SetRunningConfig(ctx context.Context, id string, config json.RawMessage, at time.Time) error {
	err := s.updateDevice(ctx, id, func(q *bun.UpdateQuery) *bun.UpdateQuery {
		return q.Set("running_config = ?", string(config)).Set("running_config_at = ?", at)
	})
	if err != nil && !errors.Is(err, device.ErrNotFound) {
		return fmt.Errorf("set running config: %w", err)
	}
	return err
}

func (s *Store) CredentialDigests(ctx context.Context, id string) ([]byte, []byte, error) {
	var row struct {
		Current []byte `bun:"credential_digest"`
		Pending []byte `bun:"pending_credential_digest"`
	}
	err := s.db.NewSelect().TableExpr("devices").ColumnExpr("credential_digest, pending_credential_digest").
		Where("id = ?", id).Where("forgotten_at IS NULL").Scan(ctx, &row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, device.ErrNotFound
	}
	if err != nil {
		return nil, nil, fmt.Errorf("credential digests: %w", err)
	}
	return row.Current, row.Pending, nil
}

// MarkAdopted upserts the unit as adopted by this control room. The agent
// profile — required to open LiveKit sessions — defaults to the oldest
// profile unless already assigned.
func (s *Store) MarkAdopted(ctx context.Context, id string, digest []byte) error {
	now := time.Now().UTC()
	err := s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if _, err := tx.NewRaw(`
		INSERT INTO devices (id, display_name, livekit_identity, enabled, created_at, updated_at,
		                     credential_digest, adopted_at, agent_profile_id)
		VALUES (?, ?, ?, TRUE, ?, ?, ?, ?,
		        (SELECT id FROM agent_profiles ORDER BY created_at LIMIT 1))
		ON CONFLICT (id) DO UPDATE SET
			credential_digest = EXCLUDED.credential_digest,
			pending_credential_digest = NULL,
			adopted_at = COALESCE(devices.adopted_at, EXCLUDED.adopted_at),
			agent_profile_id = COALESCE(devices.agent_profile_id, EXCLUDED.agent_profile_id),
			forgotten_at = NULL,
			enabled = TRUE,
			updated_at = EXCLUDED.updated_at`,
			id, id, id, now, now, digest, now,
		).Exec(ctx); err != nil {
			return err
		}
		return appendEvent(ctx, tx, deviceEvent("device.adopted", id, map[string]any{"device_id": id}, now))
	})
	if err != nil {
		return fmt.Errorf("mark adopted: %w", err)
	}
	return nil
}

func (s *Store) SetPendingSecret(ctx context.Context, id string, digest []byte) error {
	err := s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if err := s.updateDeviceIn(ctx, tx, id, func(q *bun.UpdateQuery) *bun.UpdateQuery {
			return q.Set("pending_credential_digest = ?", digest)
		}); err != nil {
			return err
		}
		return appendEvent(ctx, tx, deviceEvent("device.secret_regenerated", id, map[string]any{"device_id": id}, time.Now().UTC()))
	})
	if err != nil && !errors.Is(err, device.ErrNotFound) {
		return fmt.Errorf("set pending secret: %w", err)
	}
	return err
}

// ConfirmSecret promotes the pending secret once the device authenticates with it.
func (s *Store) ConfirmSecret(ctx context.Context, id string, digest []byte) error {
	err := s.updateDevice(ctx, id, func(q *bun.UpdateQuery) *bun.UpdateQuery {
		return q.Set("credential_digest = ?", digest).Set("pending_credential_digest = NULL")
	})
	if err != nil && !errors.Is(err, device.ErrNotFound) {
		return fmt.Errorf("confirm secret: %w", err)
	}
	return err
}

// Forget hides the unit from the inventory and drops its credentials; its
// sessions and recordings stay (history).
func (s *Store) Forget(ctx context.Context, id string) error {
	now := time.Now().UTC()
	err := s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if err := s.updateDeviceIn(ctx, tx, id, func(q *bun.UpdateQuery) *bun.UpdateQuery {
			return q.Set("forgotten_at = ?", now).
				Set("credential_digest = NULL").Set("pending_credential_digest = NULL").
				Set("desired_config = NULL").Set("desired_config_version = NULL").
				Set("desired_device_profile = NULL").Set("adopted_at = NULL")
		}); err != nil {
			return err
		}
		return appendEvent(ctx, tx, deviceEvent("device.forgotten", id, map[string]any{"device_id": id}, now))
	})
	if err != nil && !errors.Is(err, device.ErrNotFound) {
		return fmt.Errorf("forget device: %w", err)
	}
	return err
}

func (s *Store) Rename(ctx context.Context, id, name string) error {
	err := s.updateDevice(ctx, id, func(q *bun.UpdateQuery) *bun.UpdateQuery {
		return q.Set("display_name = ?", name)
	})
	if err != nil && !errors.Is(err, device.ErrNotFound) {
		return fmt.Errorf("rename device: %w", err)
	}
	return err
}
