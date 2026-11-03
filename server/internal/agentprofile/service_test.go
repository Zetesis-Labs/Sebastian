package agentprofile

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/zetesis-labs/sebastian/server/internal/secretbox"
)

type fakeStore struct {
	profiles map[uuid.UUID]Profile
	keys     map[uuid.UUID][]byte
	applied  []Plan
}

func newFakeStore(profiles ...Profile) *fakeStore {
	s := &fakeStore{profiles: map[uuid.UUID]Profile{}, keys: map[uuid.UUID][]byte{}}
	for _, p := range profiles {
		s.profiles[p.ID] = p
	}
	return s
}

func (s *fakeStore) ListAgentProfiles(context.Context) ([]Profile, error) {
	out := make([]Profile, 0, len(s.profiles))
	for _, p := range s.profiles {
		p.APIKeySet = s.keys[p.ID] != nil
		out = append(out, p)
	}
	return out, nil
}

func (s *fakeStore) GetAgentProfile(_ context.Context, id uuid.UUID) (Profile, error) {
	p, ok := s.profiles[id]
	if !ok {
		return Profile{}, ErrNotFound
	}
	p.APIKeySet = s.keys[id] != nil
	return p, nil
}

func (s *fakeStore) CreateAgentProfile(_ context.Context, spec Spec, source Source) (uuid.UUID, error) {
	for _, p := range s.profiles {
		if p.Name == spec.Name {
			return uuid.Nil, ErrNameTaken
		}
	}
	id := uuid.New()
	s.profiles[id] = Profile{ID: id, Spec: spec, Source: source}
	return id, nil
}

func (s *fakeStore) UpdateAgentProfile(_ context.Context, id uuid.UUID, spec Spec, source Source) error {
	p, ok := s.profiles[id]
	if !ok {
		return ErrNotFound
	}
	p.Spec, p.Source = spec, source
	s.profiles[id] = p
	return nil
}

func (s *fakeStore) DeleteAgentProfile(_ context.Context, id uuid.UUID) error {
	if s.profiles[id].DeviceCount > 0 {
		return ErrInUse
	}
	delete(s.profiles, id)
	return nil
}

func (s *fakeStore) AgentProfileAPIKey(_ context.Context, id uuid.UUID) ([]byte, error) {
	if _, ok := s.profiles[id]; !ok {
		return nil, ErrNotFound
	}
	return s.keys[id], nil
}

func (s *fakeStore) SetAgentProfileAPIKey(_ context.Context, id uuid.UUID, sealed []byte) error {
	if _, ok := s.profiles[id]; !ok {
		return ErrNotFound
	}
	s.keys[id] = sealed
	return nil
}

func (s *fakeStore) ApplyAgentProfilePlan(_ context.Context, plan Plan) error {
	s.applied = append(s.applied, plan)
	return nil
}

func box(t *testing.T) *secretbox.Box {
	t.Helper()
	b, err := secretbox.New(bytes.Repeat([]byte{1}, secretbox.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestCreateNormalizesValidatesAndOwnsByPanel(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store, nil, quiet())
	created, err := svc.Create(context.Background(), Spec{Name: " pruebas ", Provider: "PHONIC", Voice: "eduardo"})
	if err != nil {
		t.Fatal(err)
	}
	if created.Name != "pruebas" || created.Provider != Phonic || created.Source != SourcePanel {
		t.Fatalf("created = %+v", created)
	}
	if _, err := svc.Create(context.Background(), Spec{Name: "x", Provider: "nope"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid provider: %v", err)
	}
}

func TestKubernetesProfilesAreReadOnly(t *testing.T) {
	managed := Profile{ID: uuid.New(), Spec: Spec{Name: "gemini", Provider: Gemini}, Source: SourceKubernetes}
	svc := NewService(newFakeStore(managed), nil, quiet())
	if _, err := svc.Update(context.Background(), managed.ID, Spec{Name: "otro", Provider: Gemini}); !errors.Is(err, ErrManaged) {
		t.Fatalf("update: %v", err)
	}
	if err := svc.Delete(context.Background(), managed.ID); !errors.Is(err, ErrManaged) {
		t.Fatalf("delete: %v", err)
	}
}

func TestUpdatePanelProfile(t *testing.T) {
	own := Profile{ID: uuid.New(), Spec: Spec{Name: "pruebas", Provider: Gemini}, Source: SourcePanel}
	svc := NewService(newFakeStore(own), nil, quiet())
	updated, err := svc.Update(context.Background(), own.ID, Spec{Name: "pruebas", Provider: Phonic, Language: "ES"})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Provider != Phonic || updated.Language != "es" || updated.Source != SourcePanel {
		t.Fatalf("updated = %+v", updated)
	}
}

func TestAPIKeyNeedsTheSecretsKey(t *testing.T) {
	own := Profile{ID: uuid.New(), Spec: Spec{Name: "pruebas", Provider: Phonic}, Source: SourcePanel}
	svc := NewService(newFakeStore(own), nil, quiet())
	if err := svc.SetAPIKey(context.Background(), own.ID, "ph_secret_123"); !errors.Is(err, ErrNoSecretsKey) {
		t.Fatalf("set without key: %v", err)
	}
}

func TestAPIKeyIsSealedPerProfileAndHandedToTheAgent(t *testing.T) {
	managed := Profile{ID: uuid.New(), Spec: Spec{Name: "phonic", Provider: Phonic}, Source: SourceKubernetes}
	other := Profile{ID: uuid.New(), Spec: Spec{Name: "gemini", Provider: Gemini}, Source: SourceKubernetes}
	store := newFakeStore(managed, other)
	svc := NewService(store, box(t), quiet())
	ctx := context.Background()

	if err := svc.SetAPIKey(ctx, managed.ID, "  ph_secret_123  "); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(store.keys[managed.ID], []byte("ph_secret_123")) {
		t.Fatal("key stored in clear")
	}
	creds, err := svc.Credentials(ctx, managed.ID)
	if err != nil || creds.APIKey != "ph_secret_123" || creds.Provider != Phonic {
		t.Fatalf("credentials = %+v, %v", creds, err)
	}

	store.keys[other.ID] = store.keys[managed.ID]
	if _, err := svc.Credentials(ctx, other.ID); err == nil {
		t.Fatal("a key copied to another profile opened")
	}

	if err := svc.ClearAPIKey(ctx, managed.ID); err != nil {
		t.Fatal(err)
	}
	creds, err = svc.Credentials(ctx, managed.ID)
	if err != nil || creds.APIKey != "" {
		t.Fatalf("after clear = %+v, %v", creds, err)
	}
}

func TestAKeyNeedsAnExplicitProvider(t *testing.T) {
	legacy := Profile{ID: uuid.New(), Spec: Spec{Name: "default"}, Source: SourcePanel}
	store := newFakeStore(legacy)
	svc := NewService(store, box(t), quiet())
	if err := svc.SetAPIKey(context.Background(), legacy.ID, "AIza-secret-123"); !errors.Is(err, ErrNoProvider) {
		t.Fatalf("set key on a profile without provider: %v", err)
	}
	if store.keys[legacy.ID] != nil {
		t.Fatal("stored a key nobody knows which provider it is for")
	}
}

func TestCredentialsWithoutAKeyFallBackToTheEnvironment(t *testing.T) {
	own := Profile{ID: uuid.New(), Spec: Spec{Name: "pruebas", Provider: OpenAI}, Source: SourcePanel}
	svc := NewService(newFakeStore(own), nil, quiet())
	creds, err := svc.Credentials(context.Background(), own.ID)
	if err != nil || creds.APIKey != "" || creds.Provider != OpenAI {
		t.Fatalf("credentials = %+v, %v", creds, err)
	}
}

func TestSyncAppliesThePlanOnlyWhenSomethingChanges(t *testing.T) {
	managed := Profile{ID: uuid.New(), Spec: Spec{Name: "gemini", Provider: Gemini}, Source: SourceKubernetes}
	store := newFakeStore(managed)
	svc := NewService(store, nil, quiet())
	if _, err := svc.Sync(context.Background(), []Spec{{Name: "gemini", Provider: Gemini}}); err != nil {
		t.Fatal(err)
	}
	if len(store.applied) != 0 {
		t.Fatalf("applied an empty plan: %+v", store.applied)
	}
	plan, err := svc.Sync(context.Background(), []Spec{{Name: "gemini", Provider: Gemini}, {Name: "phonic", Provider: Phonic}})
	if err != nil || len(plan.Create) != 1 || len(store.applied) != 1 {
		t.Fatalf("plan = %+v, applied = %d, err = %v", plan, len(store.applied), err)
	}
}
