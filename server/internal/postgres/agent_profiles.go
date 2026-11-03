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
	"github.com/zetesis-labs/sebastian/server/internal/agentprofile"
)

type agentProfileRow struct {
	ID              uuid.UUID       `bun:"id"`
	Name            string          `bun:"name"`
	Config          json.RawMessage `bun:"config"`
	Source          string          `bun:"source"`
	APIKeySet       bool            `bun:"api_key_set"`
	APIKeyUpdatedAt *time.Time      `bun:"api_key_updated_at"`
	DeviceCount     int             `bun:"device_count"`
	CreatedAt       time.Time       `bun:"created_at"`
	UpdatedAt       time.Time       `bun:"updated_at"`
}

func (r agentProfileRow) toDomain() agentprofile.Profile {
	profile := agentprofile.Profile{
		ID: r.ID, Spec: agentprofile.SpecFromConfig(r.Name, r.Config), Source: agentprofile.Source(r.Source),
		APIKeySet: r.APIKeySet, DeviceCount: r.DeviceCount, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
	if r.APIKeyUpdatedAt != nil {
		profile.APIKeyUpdatedAt = *r.APIKeyUpdatedAt
	}
	return profile
}

func agentProfilesQuery(db bun.IDB) *bun.SelectQuery {
	return db.NewSelect().
		TableExpr("agent_profiles AS p").
		ColumnExpr(`p.id, p.name, p.config, p.source, p.api_key_ciphertext IS NOT NULL AS api_key_set,
			p.api_key_updated_at, p.created_at, p.updated_at,
			(SELECT count(*) FROM devices AS d WHERE d.agent_profile_id = p.id AND d.forgotten_at IS NULL) AS device_count`).
		Where("p.deleted_at IS NULL")
}

func (s *Store) ListAgentProfiles(ctx context.Context) ([]agentprofile.Profile, error) {
	return listAgentProfiles(ctx, s.db)
}

func listAgentProfiles(ctx context.Context, db bun.IDB) ([]agentprofile.Profile, error) {
	var rows []agentProfileRow
	err := agentProfilesQuery(db).
		OrderExpr("p.source = 'kubernetes' DESC, p.created_at, p.name").
		Scan(ctx, &rows)
	if err != nil {
		return nil, fmt.Errorf("list agent profiles: %w", err)
	}
	profiles := make([]agentprofile.Profile, 0, len(rows))
	for _, row := range rows {
		profiles = append(profiles, row.toDomain())
	}
	return profiles, nil
}

func (s *Store) GetAgentProfile(ctx context.Context, id uuid.UUID) (agentprofile.Profile, error) {
	var row agentProfileRow
	err := agentProfilesQuery(s.db).Where("p.id = ?", id).Scan(ctx, &row)
	if errors.Is(err, sql.ErrNoRows) {
		return agentprofile.Profile{}, agentprofile.ErrNotFound
	}
	if err != nil {
		return agentprofile.Profile{}, fmt.Errorf("get agent profile: %w", err)
	}
	return row.toDomain(), nil
}

func (s *Store) CreateAgentProfile(ctx context.Context, spec agentprofile.Spec, source agentprofile.Source) (uuid.UUID, error) {
	return createAgentProfile(ctx, s.db, spec, source, time.Now().UTC())
}

func createAgentProfile(ctx context.Context, db bun.IDB, spec agentprofile.Spec, source agentprofile.Source, now time.Time) (uuid.UUID, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.Nil, err
	}
	_, err = db.NewRaw(`
		INSERT INTO agent_profiles (id, name, agent_name, config, source, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, spec.Name, agentprofile.AgentName, string(spec.Config()), string(source), now, now,
	).Exec(ctx)
	if isUniqueViolation(err) {
		return uuid.Nil, fmt.Errorf("%w: the name %q is taken", agentprofile.ErrNameTaken, spec.Name)
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("create agent profile: %w", err)
	}
	return id, nil
}

func (s *Store) UpdateAgentProfile(ctx context.Context, id uuid.UUID, spec agentprofile.Spec, source agentprofile.Source) error {
	return updateAgentProfile(ctx, s.db, id, spec, source, time.Now().UTC())
}

func updateAgentProfile(ctx context.Context, db bun.IDB, id uuid.UUID, spec agentprofile.Spec, source agentprofile.Source, now time.Time) error {
	result, err := db.NewRaw(`
		UPDATE agent_profiles SET name = ?, config = ?, source = ?, updated_at = ?
		WHERE id = ? AND deleted_at IS NULL`,
		spec.Name, string(spec.Config()), string(source), now, id,
	).Exec(ctx)
	if isUniqueViolation(err) {
		return fmt.Errorf("%w: the name %q is taken", agentprofile.ErrNameTaken, spec.Name)
	}
	if err != nil {
		return fmt.Errorf("update agent profile: %w", err)
	}
	return requireOneRow(result)
}

// DeleteAgentProfile soft-deletes a profile no visible speaker uses and drops
// its stored key. Forgotten units lose the reference, so a re-adoption falls
// back to the default. The profile row is locked first: an assignment running
// at the same time (FOR SHARE in AssignAgentProfile) either lands before the
// in-use check or finds the profile gone.
func (s *Store) DeleteAgentProfile(ctx context.Context, id uuid.UUID) error {
	return s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		return deleteAgentProfile(ctx, tx, id, time.Now().UTC())
	})
}

func deleteAgentProfile(ctx context.Context, tx bun.Tx, id uuid.UUID, now time.Time) error {
	var locked []uuid.UUID
	if err := tx.NewRaw(`SELECT id FROM agent_profiles WHERE id = ? AND deleted_at IS NULL FOR UPDATE`, id).
		Scan(ctx, &locked); err != nil {
		return fmt.Errorf("lock agent profile: %w", err)
	}
	if len(locked) == 0 {
		return agentprofile.ErrNotFound
	}
	var inUse bool
	if err := tx.NewRaw(`SELECT EXISTS (SELECT 1 FROM devices WHERE agent_profile_id = ? AND forgotten_at IS NULL)`, id).
		Scan(ctx, &inUse); err != nil {
		return fmt.Errorf("check agent profile use: %w", err)
	}
	if inUse {
		return agentprofile.ErrInUse
	}
	if _, err := tx.NewRaw(`UPDATE devices SET agent_profile_id = NULL WHERE agent_profile_id = ?`, id).Exec(ctx); err != nil {
		return fmt.Errorf("detach forgotten devices: %w", err)
	}
	result, err := tx.NewRaw(`
		UPDATE agent_profiles
		SET deleted_at = ?, updated_at = ?, api_key_ciphertext = NULL, api_key_updated_at = NULL
		WHERE id = ? AND deleted_at IS NULL`, now, now, id).Exec(ctx)
	if err != nil {
		return fmt.Errorf("delete agent profile: %w", err)
	}
	return requireOneRow(result)
}

func (s *Store) AgentProfileAPIKey(ctx context.Context, id uuid.UUID) ([]byte, error) {
	var sealed []byte
	err := s.db.NewRaw(`SELECT api_key_ciphertext FROM agent_profiles WHERE id = ? AND deleted_at IS NULL`, id).Scan(ctx, &sealed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, agentprofile.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read agent profile api key: %w", err)
	}
	return sealed, nil
}

// SetAgentProfileAPIKey stores a sealed key; nil clears it.
func (s *Store) SetAgentProfileAPIKey(ctx context.Context, id uuid.UUID, sealed []byte) error {
	now := time.Now().UTC()
	var updatedAt *time.Time
	if sealed != nil {
		updatedAt = &now
	}
	result, err := s.db.NewRaw(`
		UPDATE agent_profiles SET api_key_ciphertext = ?, api_key_updated_at = ?, updated_at = ?
		WHERE id = ? AND deleted_at IS NULL`,
		sealed, updatedAt, now, id,
	).Exec(ctx)
	if err != nil {
		return fmt.Errorf("store agent profile api key: %w", err)
	}
	return requireOneRow(result)
}

// ApplyAgentProfilePlan applies the boot-time sync with the profiles file in
// one transaction: either the file wins entirely or nothing changes.
func (s *Store) ApplyAgentProfilePlan(ctx context.Context, plan agentprofile.Plan) error {
	now := time.Now().UTC()
	return s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		for _, change := range plan.Update {
			if err := updateAgentProfile(ctx, tx, change.ID, change.Spec, agentprofile.SourceKubernetes, now); err != nil {
				return err
			}
		}
		for _, spec := range plan.Create {
			if _, err := createAgentProfile(ctx, tx, spec, agentprofile.SourceKubernetes, now); err != nil {
				return err
			}
		}
		for _, id := range plan.Delete {
			if err := deleteAgentProfile(ctx, tx, id, now); err != nil {
				return err
			}
		}
		for _, id := range plan.Release {
			if _, err := tx.NewRaw(`UPDATE agent_profiles SET source = 'panel', updated_at = ? WHERE id = ?`, now, id).Exec(ctx); err != nil {
				return fmt.Errorf("release agent profile: %w", err)
			}
		}
		return nil
	})
}

func (s *Store) AssignAgentProfile(ctx context.Context, deviceID string, profileID uuid.UUID) error {
	return s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		var locked []uuid.UUID
		if err := tx.NewRaw(`SELECT id FROM agent_profiles WHERE id = ? AND deleted_at IS NULL FOR SHARE`, profileID).
			Scan(ctx, &locked); err != nil {
			return fmt.Errorf("lock agent profile: %w", err)
		}
		if len(locked) == 0 {
			return agentprofile.ErrNotFound
		}
		return s.updateDeviceIn(ctx, tx, deviceID, func(q *bun.UpdateQuery) *bun.UpdateQuery {
			return q.Set("agent_profile_id = ?", profileID)
		})
	})
}

func requireOneRow(result sql.Result) error {
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return agentprofile.ErrNotFound
	}
	return nil
}
