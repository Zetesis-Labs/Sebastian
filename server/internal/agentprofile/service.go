package agentprofile

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"
)

type Store interface {
	ListAgentProfiles(ctx context.Context) ([]Profile, error)
	GetAgentProfile(ctx context.Context, id uuid.UUID) (Profile, error)
	CreateAgentProfile(ctx context.Context, spec Spec, source Source) (uuid.UUID, error)
	UpdateAgentProfile(ctx context.Context, id uuid.UUID, spec Spec, source Source) error
	DeleteAgentProfile(ctx context.Context, id uuid.UUID) error
	AgentProfileAPIKey(ctx context.Context, id uuid.UUID) ([]byte, error)
	SetAgentProfileAPIKey(ctx context.Context, id uuid.UUID, sealed []byte) error
	ApplyAgentProfilePlan(ctx context.Context, plan Plan) error
}

type Sealer interface {
	Seal(plaintext, associated []byte) []byte
	Open(sealed, associated []byte) ([]byte, error)
}

type Service struct {
	store  Store
	sealer Sealer
	logger *slog.Logger
}

// NewService: a nil sealer means the server has no SEBASTIAN_SECRETS_KEY, so
// API keys can be neither stored nor read and the agent uses its environment.
func NewService(store Store, sealer Sealer, logger *slog.Logger) *Service {
	return &Service{store: store, sealer: sealer, logger: logger}
}

// Credentials is what the agent needs beyond the dispatch metadata. An empty
// APIKey means "use your environment key"; an empty Provider, "your
// environment provider" (legacy profiles only, which never hold a key).
type Credentials struct {
	Provider Provider
	APIKey   string
}

func (s *Service) SecretsKeyConfigured() bool { return s.sealer != nil }

func (s *Service) List(ctx context.Context) ([]Profile, error) {
	return s.store.ListAgentProfiles(ctx)
}

func (s *Service) Sync(ctx context.Context, manifest []Spec) (Plan, error) {
	existing, err := s.store.ListAgentProfiles(ctx)
	if err != nil {
		return Plan{}, err
	}
	plan := PlanSync(existing, manifest)
	if plan.Empty() {
		return plan, nil
	}
	if err := s.store.ApplyAgentProfilePlan(ctx, plan); err != nil {
		return Plan{}, fmt.Errorf("apply agent profiles file: %w", err)
	}
	if len(plan.Release) > 0 {
		s.logger.WarnContext(ctx, "agent profiles left the file while speakers use them; the panel owns them now",
			"profile_ids", plan.Release)
	}
	return plan, nil
}

func (s *Service) Create(ctx context.Context, spec Spec) (Profile, error) {
	spec = spec.Normalize()
	if err := spec.Validate(); err != nil {
		return Profile{}, err
	}
	id, err := s.store.CreateAgentProfile(ctx, spec, SourcePanel)
	if err != nil {
		return Profile{}, err
	}
	return s.store.GetAgentProfile(ctx, id)
}

func (s *Service) Update(ctx context.Context, id uuid.UUID, spec Spec) (Profile, error) {
	spec = spec.Normalize()
	if err := spec.Validate(); err != nil {
		return Profile{}, err
	}
	if err := s.ownedByPanel(ctx, id); err != nil {
		return Profile{}, err
	}
	if err := s.store.UpdateAgentProfile(ctx, id, spec, SourcePanel); err != nil {
		return Profile{}, err
	}
	return s.store.GetAgentProfile(ctx, id)
}

func (s *Service) Delete(ctx context.Context, id uuid.UUID) error {
	if err := s.ownedByPanel(ctx, id); err != nil {
		return err
	}
	return s.store.DeleteAgentProfile(ctx, id)
}

func (s *Service) SetAPIKey(ctx context.Context, id uuid.UUID, key string) error {
	if s.sealer == nil {
		return ErrNoSecretsKey
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return fmt.Errorf("%w: empty API key", ErrInvalid)
	}
	profile, err := s.store.GetAgentProfile(ctx, id)
	if err != nil {
		return err
	}
	if profile.Provider == "" {
		return ErrNoProvider
	}
	return s.store.SetAgentProfileAPIKey(ctx, id, s.sealer.Seal([]byte(key), id[:]))
}

func (s *Service) ClearAPIKey(ctx context.Context, id uuid.UUID) error {
	return s.store.SetAgentProfileAPIKey(ctx, id, nil)
}

func (s *Service) Credentials(ctx context.Context, id uuid.UUID) (Credentials, error) {
	profile, err := s.store.GetAgentProfile(ctx, id)
	if err != nil {
		return Credentials{}, err
	}
	creds := Credentials{Provider: profile.Provider}
	if !profile.APIKeySet {
		return creds, nil
	}
	if s.sealer == nil {
		return Credentials{}, fmt.Errorf("profile %s has a stored API key but %w", id, ErrNoSecretsKey)
	}
	sealed, err := s.store.AgentProfileAPIKey(ctx, id)
	if err != nil {
		return Credentials{}, err
	}
	key, err := s.sealer.Open(sealed, id[:])
	if err != nil {
		return Credentials{}, fmt.Errorf("open the API key of profile %s: %w", id, err)
	}
	creds.APIKey = string(key)
	return creds, nil
}

func (s *Service) ownedByPanel(ctx context.Context, id uuid.UUID) error {
	profile, err := s.store.GetAgentProfile(ctx, id)
	if err != nil {
		return err
	}
	if profile.Source == SourceKubernetes {
		return ErrManaged
	}
	return nil
}
