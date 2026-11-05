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
	"github.com/zetesis-labs/sebastian/server/internal/device"
	"github.com/zetesis-labs/sebastian/server/internal/meeting"
	"github.com/zetesis-labs/sebastian/server/internal/recording"
	"github.com/zetesis-labs/sebastian/server/internal/session"
)

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
	if !eventExists(ctx, db, recordingEventID) {
		t.Fatal("recording event is missing")
	}
}

func TestRecordSessionPersistsTheSessionAndItsEvent(t *testing.T) {
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
	var eventType string
	if err := db.QueryRowContext(ctx, "SELECT event_type FROM domain_events WHERE id = ?", record.EventID).Scan(&eventType); err != nil || eventType != "session.created" {
		t.Fatalf("domain event = %q, %v", eventType, err)
	}
}

func cleanupSession(ctx context.Context, db *bun.DB, sessionID uuid.UUID, eventIDs ...uuid.UUID) {
	for _, eventID := range eventIDs {
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

func rowExists(ctx context.Context, db *bun.DB, query string, id uuid.UUID) bool {
	var exists bool
	return db.QueryRowContext(ctx, query, id).Scan(&exists) == nil && exists
}

// Spec 12 §12 observability: adoption, forget, secret regeneration and a new
// desired config each leave a domain event; RF-36/42: the event
// a unit reports in its poll is kept with the time it was first seen.
func TestDeviceLifecycleLeavesDomainEventsAndKeepsTheLastEvent(t *testing.T) {
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
	id := "test" + strings.ReplaceAll(uuid.NewString(), "-", "")[:8]
	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, `DELETE FROM domain_events WHERE aggregate_type = 'device' AND aggregate_id = ?`, id)
		_, _ = db.ExecContext(ctx, `DELETE FROM devices WHERE id = ?`, id)
	})

	if err := store.MarkAdopted(ctx, id, []byte("digest")); err != nil {
		t.Fatalf("MarkAdopted: %v", err)
	}
	if _, err := store.TouchPoll(ctx, device.Poll{ID: id, Event: "adopt-denied:10.0.0.77"}); err != nil {
		t.Fatalf("TouchPoll: %v", err)
	}
	first, err := store.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if first.LastEvent != "adopt-denied:10.0.0.77" || first.LastEventAt.IsZero() {
		t.Fatalf("the poll event was not stored: %+v", first)
	}
	time.Sleep(10 * time.Millisecond)
	if _, err := store.TouchPoll(ctx, device.Poll{ID: id, Event: "adopt-denied:10.0.0.77"}); err != nil {
		t.Fatalf("TouchPoll: %v", err)
	}
	if _, err := store.TouchPoll(ctx, device.Poll{ID: id}); err != nil {
		t.Fatalf("TouchPoll: %v", err)
	}
	again, _ := store.Get(ctx, id)
	if again.LastEvent != first.LastEvent || !again.LastEventAt.Equal(first.LastEventAt) {
		t.Fatalf("repeating or omitting the event must keep the first sighting: %+v vs %+v", again, first)
	}
	if _, err := store.TouchPoll(ctx, device.Poll{ID: id, Event: "cfg-rejected:wifi"}); err != nil {
		t.Fatalf("TouchPoll: %v", err)
	}
	changed, _ := store.Get(ctx, id)
	if changed.LastEvent != "cfg-rejected:wifi" || !changed.LastEventAt.After(first.LastEventAt) {
		t.Fatalf("a new event must replace the old one with a new time: %+v", changed)
	}

	if err := store.SetDesiredConfig(ctx, id, []byte(`{"schema":"sebastian.config.v1"}`), "v1"); err != nil {
		t.Fatalf("SetDesiredConfig: %v", err)
	}
	if err := store.SetPendingSecret(ctx, id, []byte("pending")); err != nil {
		t.Fatalf("SetPendingSecret: %v", err)
	}
	if err := store.Forget(ctx, id); err != nil {
		t.Fatalf("Forget: %v", err)
	}
	var events []string
	if err := db.NewRaw(`
		SELECT event_type FROM domain_events
		WHERE aggregate_type = 'device' AND aggregate_id = ? ORDER BY occurred_at, event_type`, id).Scan(ctx, &events); err != nil {
		t.Fatalf("list events: %v", err)
	}
	want := "device.adopted,device.config_desired,device.secret_regenerated,device.forgotten"
	if strings.Join(events, ",") != want {
		t.Fatalf("domain events = %v", events)
	}
}

// T-A7: a meeting's life leaves its domain events atomically, the
// active-per-unit lookup honours RM-05, and delete keeps the trace (RM-46).
func TestMeetingLifecycleLeavesDomainEventsAndKeepsTheTrace(t *testing.T) {
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
	deviceID := "mtg" + strings.ReplaceAll(uuid.NewString(), "-", "")[:8]
	if err := store.MarkAdopted(ctx, deviceID, []byte("digest")); err != nil {
		t.Fatalf("MarkAdopted: %v", err)
	}
	meetings := store.Meetings()
	now := time.Now().UTC().Truncate(time.Microsecond)
	m := meeting.Meeting{ID: uuid.Must(uuid.NewV7()), DeviceID: deviceID, State: meeting.StateRequested, RequestedBy: meeting.OriginDashboard, RequestedAt: now, CreatedAt: now, UpdatedAt: now}
	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, `DELETE FROM domain_events WHERE aggregate_type IN ('meeting','device') AND aggregate_id IN (?, ?)`, m.ID.String(), deviceID)
		_, _ = db.ExecContext(ctx, `DELETE FROM meetings WHERE id = ?`, m.ID)
		_, _ = db.ExecContext(ctx, `DELETE FROM devices WHERE id = ?`, deviceID)
	})
	if err := meetings.Insert(ctx, m); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if _, active, err := meetings.Active(ctx, deviceID); err != nil || !active {
		t.Fatalf("a requested meeting blocks the unit (RM-05): %v %v", active, err)
	}
	m.State, m.StartedAt, m.LastAudioAt, m.UpdatedAt = meeting.StateRecording, now.Add(2*time.Second), now.Add(2*time.Second), now.Add(2*time.Second)
	if err := meetings.Update(ctx, m, "meeting.started"); err != nil {
		t.Fatalf("Update: %v", err)
	}
	m.AudioBytes, m.AudioPath = 4096, m.ID.String()+".ogg"
	if err := meetings.Update(ctx, m, ""); err != nil {
		t.Fatalf("Update without event: %v", err)
	}
	got, err := meetings.Get(ctx, m.ID)
	if err != nil || got.State != meeting.StateRecording || got.AudioBytes != 4096 || !got.StartedAt.Equal(m.StartedAt) {
		t.Fatalf("Get: %+v %v", got, err)
	}
	inProgress, _ := meetings.InProgress(ctx)
	if len(inProgress) == 0 || inProgress[len(inProgress)-1].ID != m.ID {
		t.Fatalf("InProgress must list it: %v", inProgress)
	}
	m.State, m.EndReason, m.EndedAt, m.DurationMs, m.UpdatedAt = meeting.StateCut, meeting.EndDeviceLost, now.Add(time.Minute), 58_000, now.Add(time.Minute)
	if err := meetings.Update(ctx, m, "meeting.ended"); err != nil {
		t.Fatalf("Update: %v", err)
	}
	// Block D: transcript, digest and the failure reason round-trip as JSON.
	tr := meeting.Transcript{Language: "es", Diarized: true, Segments: []meeting.Segment{{Start: 0, End: 1.5, Speaker: "Hablante 1", Text: "Hola"}}, Speakers: map[string]string{"Hablante 1": "Ana"}}
	tr.Text = tr.PlainText()
	m.Transcript, m.Summary, m.TranscriptError = &tr, &meeting.Summary{Text: "resumen", Agreements: []string{}, Actions: []string{"x"}, Model: "mini", GeneratedAt: now}, "was: timeout"
	if err := meetings.Update(ctx, m, "meeting.transcribed"); err != nil {
		t.Fatalf("Update transcript: %v", err)
	}
	got, err = meetings.Get(ctx, m.ID)
	if err != nil || got.Transcript == nil || got.Transcript.Segments[0].Speaker != "Hablante 1" || got.Transcript.Speakers["Hablante 1"] != "Ana" || got.Summary == nil || got.Summary.Actions[0] != "x" || got.TranscriptError != "was: timeout" {
		t.Fatalf("transcript round-trip: %+v %+v %v", got.Transcript, got.Summary, err)
	}
	if found, _ := meetings.List(ctx, meeting.Filter{DeviceID: deviceID, Query: "hola", Limit: 5}); len(found) != 1 {
		t.Fatalf("search in the transcript (RM-42): %d", len(found))
	}
	if found, _ := meetings.List(ctx, meeting.Filter{DeviceID: deviceID, Query: "nada", Limit: 5}); len(found) != 0 {
		t.Fatalf("search miss: %d", len(found))
	}
	if expired, _ := meetings.EndedBefore(ctx, now.Add(2*time.Minute), 10); len(expired) != 1 || expired[0].ID != m.ID {
		t.Fatalf("EndedBefore: %+v", expired)
	}
	if _, active, _ := meetings.Active(ctx, deviceID); active {
		t.Fatal("a cut meeting frees the unit")
	}
	listed, err := meetings.List(ctx, meeting.Filter{DeviceID: deviceID, Limit: 10})
	if err != nil || len(listed) != 1 || listed[0].EndReason != meeting.EndDeviceLost {
		t.Fatalf("List: %+v %v", listed, err)
	}
	if err := meetings.Delete(ctx, m.ID, now.Add(2*time.Minute)); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	trace, _ := meetings.Get(ctx, m.ID)
	if trace.DeletedAt.IsZero() || trace.AudioPath != "" || trace.DurationMs != 58_000 {
		t.Fatalf("the trace stays without content (RM-46): %+v", trace)
	}
	if err := meetings.Delete(ctx, uuid.New(), now); !errors.Is(err, meeting.ErrNotFound) {
		t.Fatalf("deleting an unknown meeting: %v", err)
	}
	var events []string
	if err := db.NewRaw(`
		SELECT event_type FROM domain_events
		WHERE aggregate_type = 'meeting' AND aggregate_id = ? ORDER BY occurred_at, event_type`, m.ID.String()).Scan(ctx, &events); err != nil {
		t.Fatalf("list events: %v", err)
	}
	want := "meeting.requested,meeting.started,meeting.ended,meeting.transcribed,meeting.deleted"
	if strings.Join(events, ",") != want {
		t.Fatalf("domain events = %v", events)
	}
	if err := meetings.Drop(ctx, m.ID); err != nil {
		t.Fatalf("Drop: %v", err)
	}
	if _, err := meetings.Get(ctx, m.ID); !errors.Is(err, meeting.ErrNotFound) {
		t.Fatal("dropped meetings are gone")
	}
}

// Readopt: an authenticated poll leaves where the unit is; a poll that carries
// no address (unauthenticated) keeps the last one.
func TestThePollKeepsTheLastAddressOfTheUnit(t *testing.T) {
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
	id := "test" + strings.ReplaceAll(uuid.NewString(), "-", "")[:8]
	t.Cleanup(func() { _, _ = db.ExecContext(ctx, `DELETE FROM devices WHERE id = ?`, id) })

	if _, err := store.TouchPoll(ctx, device.Poll{ID: id, IP: "10.0.0.193"}); err != nil {
		t.Fatalf("TouchPoll: %v", err)
	}
	if _, err := store.TouchPoll(ctx, device.Poll{ID: id}); err != nil {
		t.Fatalf("TouchPoll: %v", err)
	}
	got, err := store.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.LastIP != "10.0.0.193" {
		t.Fatalf("last ip = %q", got.LastIP)
	}
	if _, err := store.TouchPoll(ctx, device.Poll{ID: id, IP: "10.0.0.201"}); err != nil {
		t.Fatalf("TouchPoll: %v", err)
	}
	if moved, _ := store.Get(ctx, id); moved.LastIP != "10.0.0.201" {
		t.Fatalf("a new address must replace the old one: %q", moved.LastIP)
	}
	if err := store.Forget(ctx, id); err != nil {
		t.Fatalf("Forget: %v", err)
	}
	if _, err := store.TouchPoll(ctx, device.Poll{ID: id}); err != nil {
		t.Fatalf("TouchPoll: %v", err)
	}
	if back, _ := store.Get(ctx, id); back.LastIP != "" {
		t.Fatalf("a forgotten unit must not keep an address its lease may have lost: %q", back.LastIP)
	}
}

func TestReadoptingAUnitWaitsAgainForItsFirstPoll(t *testing.T) {
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
	id := "test" + strings.ReplaceAll(uuid.NewString(), "-", "")[:8]
	t.Cleanup(func() { _, _ = db.ExecContext(ctx, `DELETE FROM devices WHERE id = ?`, id) })

	if err := store.MarkAdopted(ctx, id, []byte("digest")); err != nil {
		t.Fatalf("MarkAdopted: %v", err)
	}
	if _, err := store.TouchPoll(ctx, device.Poll{ID: id}); err != nil {
		t.Fatalf("TouchPoll: %v", err)
	}
	if err := store.MarkAdopted(ctx, id, []byte("digest-2")); err != nil {
		t.Fatalf("MarkAdopted again: %v", err)
	}
	got, err := store.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.AdoptedAt.After(got.ProfileReportedAt) {
		t.Fatalf("readopted at %v, last poll %v: the new adoption must come after the old poll", got.AdoptedAt, got.ProfileReportedAt)
	}
}
