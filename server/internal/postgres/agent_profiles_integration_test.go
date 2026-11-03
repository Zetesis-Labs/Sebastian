package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/zetesis-labs/sebastian/server/internal/agentprofile"
	"github.com/zetesis-labs/sebastian/server/internal/database"
)

func integrationStore(t *testing.T) *Store {
	t.Helper()
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL is not set")
	}
	db, err := database.Open(databaseURL)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewStore(db)
}

func profileByName(t *testing.T, store *Store, name string) (agentprofile.Profile, bool) {
	t.Helper()
	profiles, err := store.ListAgentProfiles(context.Background())
	if err != nil {
		t.Fatalf("ListAgentProfiles: %v", err)
	}
	for _, p := range profiles {
		if p.Name == name {
			return p, true
		}
	}
	return agentprofile.Profile{}, false
}

func TestAgentProfilesFromTheFileThePanelAndTheSpeakers(t *testing.T) {
	store := integrationStore(t)
	ctx := context.Background()
	suffix := uuid.NewString()[:8]
	gemini, phonic, dropped := "k8s-gemini-"+suffix, "k8s-phonic-"+suffix, "k8s-old-"+suffix

	err := store.ApplyAgentProfilePlan(ctx, agentprofile.Plan{Create: []agentprofile.Spec{
		{Name: gemini, Provider: agentprofile.Gemini, Voice: "Puck"},
		{Name: phonic, Provider: agentprofile.Phonic, Voice: "eduardo", Language: "es"},
		{Name: dropped, Provider: agentprofile.OpenAI},
	}})
	if err != nil {
		t.Fatalf("apply create plan: %v", err)
	}
	managed, ok := profileByName(t, store, phonic)
	if !ok || managed.Source != agentprofile.SourceKubernetes || managed.Voice != "eduardo" || managed.Language != "es" {
		t.Fatalf("managed = %+v", managed)
	}

	deviceID := "agents-" + suffix
	if err := store.MarkAdopted(ctx, deviceID, []byte("digest")); err != nil {
		t.Fatalf("MarkAdopted: %v", err)
	}
	if err := store.AssignAgentProfile(ctx, deviceID, managed.ID); err != nil {
		t.Fatalf("AssignAgentProfile: %v", err)
	}
	if err := store.AssignAgentProfile(ctx, deviceID, uuid.New()); !errors.Is(err, agentprofile.ErrNotFound) {
		t.Fatalf("assign unknown profile: %v", err)
	}
	found, err := store.FindDevice(ctx, deviceID)
	if err != nil {
		t.Fatalf("FindDevice: %v", err)
	}
	var cfg map[string]string
	_ = json.Unmarshal(found.AgentConfig, &cfg)
	if found.ProfileID != managed.ID || cfg["provider"] != "phonic" || cfg["voice"] != "eduardo" {
		t.Fatalf("dispatch sees profile %s config %s", found.ProfileID, found.AgentConfig)
	}
	listed, err := store.Get(ctx, deviceID)
	if err != nil || listed.AgentProfileID != managed.ID || listed.AgentProfileName != phonic {
		t.Fatalf("device = %+v, %v", listed, err)
	}
	if managed, _ = profileByName(t, store, phonic); managed.DeviceCount != 1 {
		t.Fatalf("device count = %d", managed.DeviceCount)
	}

	if err := store.DeleteAgentProfile(ctx, managed.ID); !errors.Is(err, agentprofile.ErrInUse) {
		t.Fatalf("delete in use: %v", err)
	}
	droppedProfile, _ := profileByName(t, store, dropped)
	if err := store.ApplyAgentProfilePlan(ctx, agentprofile.Plan{
		Update:  []agentprofile.Change{{ID: managed.ID, Spec: agentprofile.Spec{Name: phonic, Provider: agentprofile.Phonic, Voice: "valeria", Language: "es"}}},
		Delete:  []uuid.UUID{droppedProfile.ID},
		Release: []uuid.UUID{},
	}); err != nil {
		t.Fatalf("apply update plan: %v", err)
	}
	if managed, _ = profileByName(t, store, phonic); managed.Voice != "valeria" {
		t.Fatalf("after update = %+v", managed)
	}
	if _, ok := profileByName(t, store, dropped); ok {
		t.Fatal("dropped profile still listed")
	}
	if _, err := store.CreateAgentProfile(ctx, agentprofile.Spec{Name: dropped, Provider: agentprofile.Gemini}, agentprofile.SourcePanel); err != nil {
		t.Fatalf("a deleted profile's name is reusable: %v", err)
	}
	if _, err := store.CreateAgentProfile(ctx, agentprofile.Spec{Name: gemini, Provider: agentprofile.Gemini}, agentprofile.SourcePanel); !errors.Is(err, agentprofile.ErrNameTaken) {
		t.Fatalf("duplicate name: %v", err)
	}

	if err := store.SetAgentProfileAPIKey(ctx, managed.ID, []byte("sealed")); err != nil {
		t.Fatalf("SetAgentProfileAPIKey: %v", err)
	}
	if sealed, err := store.AgentProfileAPIKey(ctx, managed.ID); err != nil || string(sealed) != "sealed" {
		t.Fatalf("read key = %q, %v", sealed, err)
	}
	if managed, _ = profileByName(t, store, phonic); !managed.APIKeySet || managed.APIKeyUpdatedAt.IsZero() {
		t.Fatalf("key flags = %+v", managed)
	}
	if err := store.SetAgentProfileAPIKey(ctx, managed.ID, nil); err != nil {
		t.Fatalf("clear key: %v", err)
	}
	if managed, _ = profileByName(t, store, phonic); managed.APIKeySet {
		t.Fatal("key still set after clearing")
	}

	if err := store.ApplyAgentProfilePlan(ctx, agentprofile.Plan{Release: []uuid.UUID{managed.ID}}); err != nil {
		t.Fatalf("apply release plan: %v", err)
	}
	if managed, _ = profileByName(t, store, phonic); managed.Source != agentprofile.SourcePanel {
		t.Fatalf("released profile source = %s", managed.Source)
	}
}

func TestDeletingAnAgentDropsItsStoredKey(t *testing.T) {
	store := integrationStore(t)
	ctx := context.Background()
	id, err := store.CreateAgentProfile(ctx, agentprofile.Spec{Name: "borrable-" + uuid.NewString()[:8], Provider: agentprofile.Phonic}, agentprofile.SourcePanel)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := store.SetAgentProfileAPIKey(ctx, id, []byte("sealed")); err != nil {
		t.Fatalf("set key: %v", err)
	}
	if err := store.DeleteAgentProfile(ctx, id); err != nil {
		t.Fatalf("delete: %v", err)
	}
	var cleared bool
	if err := store.db.NewRaw(`SELECT api_key_ciphertext IS NULL AND api_key_updated_at IS NULL FROM agent_profiles WHERE id = ?`, id).Scan(ctx, &cleared); err != nil || !cleared {
		t.Fatalf("key still stored after delete (cleared=%v, err=%v)", cleared, err)
	}
	if err := store.DeleteAgentProfile(ctx, id); !errors.Is(err, agentprofile.ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
	if err := store.AssignAgentProfile(ctx, "esp32-respeaker", id); !errors.Is(err, agentprofile.ErrNotFound) {
		t.Fatalf("assign a deleted profile: %v", err)
	}
}
