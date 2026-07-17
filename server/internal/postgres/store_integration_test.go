package postgres

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"
	"github.com/zetesis-labs/sebastian/server/internal/database"
	"github.com/zetesis-labs/sebastian/server/internal/database/migrations"
	"github.com/zetesis-labs/sebastian/server/internal/outbox"
	"github.com/zetesis-labs/sebastian/server/internal/recording"
	"github.com/zetesis-labs/sebastian/server/internal/session"
)

type recordingPublisher struct {
	events []outbox.Event
	err    error
}

func TestMain(m *testing.M) {
	sourceURL := os.Getenv("DATABASE_URL")
	if sourceURL == "" {
		os.Exit(m.Run())
	}

	parsed, err := url.Parse(sourceURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, "parse DATABASE_URL:", err)
		os.Exit(1)
	}
	databaseName := "sebastian_integration_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	adminURL := *parsed
	adminURL.Path = "/postgres"
	adminDB, err := database.OpenMigration(adminURL.String())
	if err != nil {
		fmt.Fprintln(os.Stderr, "connect integration admin database:", err)
		os.Exit(1)
	}

	ctx := context.Background()
	if _, err := adminDB.ExecContext(ctx, "CREATE DATABASE "+databaseName); err != nil {
		_ = adminDB.Close()
		fmt.Fprintln(os.Stderr, "create integration database:", err)
		os.Exit(1)
	}
	testURL := *parsed
	testURL.Path = "/" + databaseName
	migrationDB, err := database.OpenMigration(testURL.String())
	if err != nil {
		_, _ = adminDB.ExecContext(ctx, "DROP DATABASE "+databaseName+" WITH (FORCE)")
		_ = adminDB.Close()
		fmt.Fprintln(os.Stderr, "connect integration database:", err)
		os.Exit(1)
	}
	if err := migrations.Run(ctx, migrationDB); err != nil {
		_ = migrationDB.Close()
		_, _ = adminDB.ExecContext(ctx, "DROP DATABASE "+databaseName+" WITH (FORCE)")
		_ = adminDB.Close()
		fmt.Fprintln(os.Stderr, "migrate integration database:", err)
		os.Exit(1)
	}
	_ = migrationDB.Close()

	previousURL, hadPreviousURL := os.LookupEnv("DATABASE_URL")
	_ = os.Setenv("DATABASE_URL", testURL.String())
	code := m.Run()
	if hadPreviousURL {
		_ = os.Setenv("DATABASE_URL", previousURL)
	} else {
		_ = os.Unsetenv("DATABASE_URL")
	}
	_, _ = adminDB.ExecContext(ctx, "DROP DATABASE "+databaseName+" WITH (FORCE)")
	_ = adminDB.Close()
	os.Exit(code)
}

func TestRecordingCatalogueRoundTrip(t *testing.T) {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL is not set")
	}
	ctx := context.Background()
	db, err := database.Open(databaseURL)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := NewStore(db)
	device, err := store.FindDevice(ctx, "esp32-respeaker")
	if err != nil {
		t.Fatalf("find seeded device: %v", err)
	}

	sessionRecord := session.Record{
		SessionID: uuid.New(), DeviceID: device.ID, ProfileID: device.ProfileID,
		Room: "recording-catalogue-" + uuid.NewString(), ExpiresAt: time.Now().UTC().Add(time.Hour),
		EventID: uuid.New(), OccurredAt: time.Now().UTC(),
	}
	if err := store.RecordSession(ctx, sessionRecord); err != nil {
		t.Fatalf("RecordSession: %v", err)
	}
	recordingID, recordingEventID := uuid.New(), uuid.New()
	created, err := store.CreateRecording(ctx, recording.CreateRecord{
		ID: recordingID, EventID: recordingEventID, Now: time.Now().UTC(),
		Input: recording.Registration{
			Room: sessionRecord.Room, Kind: recording.KindModel, FileName: "model.wav",
			ObjectURL:   "https://storage.example/" + recordingID.String() + ".wav",
			ContentType: "audio/wav", ByteSize: 4096, DurationMs: 2500,
			CapturedAt: time.Now().UTC(),
		},
	})
	if err != nil {
		t.Fatalf("CreateRecording: %v", err)
	}
	t.Cleanup(func() {
		cleanupSession(context.Background(), db, sessionRecord.SessionID, recordingEventID, sessionRecord.EventID)
	})

	if created.ID != recordingID || created.Room != sessionRecord.Room {
		t.Fatalf("unexpected created recording: %#v", created)
	}
	got, err := store.GetRecording(ctx, recordingID)
	if err != nil || got.ObjectURL != created.ObjectURL {
		t.Fatalf("GetRecording() = %#v, %v", got, err)
	}
	items, err := store.ListRecordings(ctx, 100)
	if err != nil {
		t.Fatalf("ListRecordings: %v", err)
	}
	found := false
	for _, item := range items {
		found = found || item.ID == recordingID
	}
	if !found {
		t.Fatalf("recording %s was not listed", recordingID)
	}
	summary, err := store.RecordingsSummary(ctx)
	if err != nil || summary.Count < 1 || summary.TotalBytes < 4096 {
		t.Fatalf("RecordingsSummary() = %#v, %v", summary, err)
	}
	if !eventExists(ctx, db, recordingEventID) || !outboxEventExists(ctx, db, recordingEventID) {
		t.Fatal("recording event or outbox event is missing")
	}
}

func (p *recordingPublisher) Publish(_ context.Context, event outbox.Event) error {
	p.events = append(p.events, event)
	return p.err
}

func TestRecordSessionPersistsEventAndOutboxAtomically(t *testing.T) {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL is not set")
	}
	ctx := context.Background()
	db, err := database.Open(databaseURL)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	store := NewStore(db)
	device, err := store.FindDevice(ctx, "esp32-respeaker")
	if err != nil {
		t.Fatalf("find seeded device: %v", err)
	}
	record := session.Record{
		SessionID:  uuid.New(),
		DeviceID:   device.ID,
		ProfileID:  device.ProfileID,
		Room:       "test-" + uuid.NewString(),
		ExpiresAt:  time.Now().UTC().Add(time.Hour),
		EventID:    uuid.New(),
		OccurredAt: time.Now().UTC(),
	}
	if err := store.RecordSession(ctx, record); err != nil {
		t.Fatalf("RecordSession: %v", err)
	}
	t.Cleanup(func() { cleanupSession(context.Background(), db, record.SessionID, record.EventID) })

	if !sessionExists(ctx, db, record.SessionID) || !eventExists(ctx, db, record.EventID) {
		t.Fatal("session or domain event is missing")
	}
	outbox, err := getOutboxEvent(ctx, db, record.EventID)
	if err != nil {
		t.Fatalf("get outbox event: %v", err)
	}
	if outbox.Subject != "evt.sebastian.v1.session.created" || outbox.EventID != record.EventID {
		t.Fatalf("unexpected outbox event: %#v", outbox)
	}
}

func TestProcessPendingPublishesAndSchedulesRetries(t *testing.T) {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL is not set")
	}
	ctx := context.Background()
	db, err := database.Open(databaseURL)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	store := NewStore(db)
	device, err := store.FindDevice(ctx, "esp32-respeaker")
	if err != nil {
		t.Fatalf("find seeded device: %v", err)
	}
	record := session.Record{
		SessionID:  uuid.New(),
		DeviceID:   device.ID,
		ProfileID:  device.ProfileID,
		Room:       "outbox-test-" + uuid.NewString(),
		ExpiresAt:  time.Now().UTC().Add(time.Hour),
		EventID:    uuid.New(),
		OccurredAt: time.Now().UTC().Add(-100 * 365 * 24 * time.Hour),
	}
	if err := store.RecordSession(ctx, record); err != nil {
		t.Fatalf("RecordSession: %v", err)
	}
	t.Cleanup(func() { cleanupSession(context.Background(), db, record.SessionID, record.EventID) })

	failing := &recordingPublisher{err: errors.New("nats unavailable")}
	now := time.Now().UTC()
	result, err := store.ProcessPending(ctx, now, 1, failing)
	if err != nil {
		t.Fatalf("ProcessPending(failure): %v", err)
	}
	if result.Failed != 1 || len(failing.events) != 1 || failing.events[0].ID != record.EventID {
		t.Fatalf("unexpected failed batch: result=%#v events=%#v", result, failing.events)
	}
	entity, err := getOutboxEvent(ctx, db, record.EventID)
	if err != nil {
		t.Fatalf("get failed outbox event: %v", err)
	}
	if entity.Attempts != 1 || entity.LastError == nil || !entity.NextAttemptAt.After(now) {
		t.Fatalf("retry was not scheduled: %#v", entity)
	}

	if _, err := db.ExecContext(ctx, "UPDATE outbox_events SET next_attempt_at = ? WHERE event_id = ?", now, record.EventID); err != nil {
		t.Fatalf("make event eligible: %v", err)
	}
	successful := &recordingPublisher{}
	result, err = store.ProcessPending(ctx, now, 1, successful)
	if err != nil {
		t.Fatalf("ProcessPending(success): %v", err)
	}
	if result.Published != 1 || len(successful.events) != 1 {
		t.Fatalf("unexpected successful batch: result=%#v events=%#v", result, successful.events)
	}
	entity, err = getOutboxEvent(ctx, db, record.EventID)
	if err != nil {
		t.Fatalf("get published outbox event: %v", err)
	}
	if entity.PublishedAt == nil || entity.Attempts != 2 || entity.LastError != nil {
		t.Fatalf("event was not marked published: %#v", entity)
	}
}

type outboxEventState struct {
	Subject       string
	EventID       uuid.UUID
	Attempts      int64
	LastError     *string
	NextAttemptAt time.Time
	PublishedAt   *time.Time
}

func cleanupSession(ctx context.Context, db *bun.DB, sessionID uuid.UUID, eventIDs ...uuid.UUID) {
	for _, eventID := range eventIDs {
		_, _ = db.ExecContext(ctx, "DELETE FROM outbox_events WHERE event_id = ?", eventID)
		_, _ = db.ExecContext(ctx, "DELETE FROM domain_events WHERE id = ?", eventID)
	}
	_, _ = db.ExecContext(ctx, "DELETE FROM recordings WHERE session_id = ?", sessionID)
	_, _ = db.ExecContext(ctx, "DELETE FROM sessions WHERE id = ?", sessionID)
}

func sessionExists(ctx context.Context, db *bun.DB, id uuid.UUID) bool {
	return rowExists(ctx, db, "SELECT EXISTS (SELECT 1 FROM sessions WHERE id = ?)", id)
}

func eventExists(ctx context.Context, db *bun.DB, id uuid.UUID) bool {
	return rowExists(ctx, db, "SELECT EXISTS (SELECT 1 FROM domain_events WHERE id = ?)", id)
}

func outboxEventExists(ctx context.Context, db *bun.DB, id uuid.UUID) bool {
	return rowExists(ctx, db, "SELECT EXISTS (SELECT 1 FROM outbox_events WHERE event_id = ?)", id)
}

func rowExists(ctx context.Context, db *bun.DB, query string, id uuid.UUID) bool {
	var exists bool
	return db.QueryRowContext(ctx, query, id).Scan(&exists) == nil && exists
}

func getOutboxEvent(ctx context.Context, db *bun.DB, eventID uuid.UUID) (outboxEventState, error) {
	var event outboxEventState
	err := db.QueryRowContext(ctx, `
		SELECT subject, event_id, attempts, last_error, next_attempt_at, published_at
		FROM outbox_events
		WHERE event_id = ?
	`, eventID).Scan(
		&event.Subject,
		&event.EventID,
		&event.Attempts,
		&event.LastError,
		&event.NextAttemptAt,
		&event.PublishedAt,
	)
	return event, err
}
