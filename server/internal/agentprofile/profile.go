// Package agentprofile holds the agents a speaker can talk through: which
// realtime provider, model, voice and language the agent uses for a session.
//
// Profiles have one owner each. Those declared in the agent profiles file
// (a Kubernetes ConfigMap) are owned by Kubernetes and read-only in the panel;
// the panel owns the ones it creates. The provider API key is the only thing
// the panel may set on any profile.
package agentprofile

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

type Provider string

const (
	Gemini Provider = "gemini"
	OpenAI Provider = "openai"
	Phonic Provider = "phonic"
)

// AgentName is the LiveKit agent every profile dispatches.
const AgentName = "sebastian"

type Source string

const (
	SourceKubernetes Source = "kubernetes"
	SourcePanel      Source = "panel"
)

var (
	ErrInvalid      = errors.New("invalid agent profile")
	ErrNotFound     = errors.New("agent profile not found")
	ErrConflict     = errors.New("agent profile conflict")
	ErrNameTaken    = fmt.Errorf("%w: name taken", ErrConflict)
	ErrManaged      = fmt.Errorf("%w: managed by Kubernetes", ErrConflict)
	ErrInUse        = fmt.Errorf("%w: speakers still use it", ErrConflict)
	ErrNoSecretsKey = errors.New("SEBASTIAN_SECRETS_KEY is not configured")
	// ErrNoProvider: a legacy profile (config {}) talks through the agent's
	// environment provider, which the server does not know, so a key stored
	// for it could be handed to the wrong provider.
	ErrNoProvider = fmt.Errorf("%w: choose a provider before storing a key", ErrConflict)
)

// Spec is what defines a profile, wherever it is declared.
type Spec struct {
	Name     string   `json:"name"`
	Provider Provider `json:"provider"`
	Model    string   `json:"model,omitempty"`
	Voice    string   `json:"voice,omitempty"`
	Language string   `json:"language,omitempty"`
}

type Profile struct {
	ID uuid.UUID
	Spec
	Source          Source
	APIKeySet       bool
	APIKeyUpdatedAt time.Time
	DeviceCount     int
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

var language = regexp.MustCompile(`^[a-z]{2,3}(-[a-z0-9]{2,8})?$`)

func (s Spec) Normalize() Spec {
	return Spec{
		Name:     strings.TrimSpace(s.Name),
		Provider: Provider(strings.ToLower(strings.TrimSpace(string(s.Provider)))),
		Model:    strings.TrimSpace(s.Model),
		Voice:    strings.TrimSpace(s.Voice),
		Language: strings.ToLower(strings.TrimSpace(s.Language)),
	}
}

func (s Spec) Validate() error {
	s = s.Normalize()
	switch {
	case s.Name == "" || utf8.RuneCountInString(s.Name) > 64:
		return fmt.Errorf("%w: name must have 1 to 64 characters", ErrInvalid)
	case s.Provider != Gemini && s.Provider != OpenAI && s.Provider != Phonic:
		return fmt.Errorf("%w: provider %q is not gemini, openai or phonic", ErrInvalid, s.Provider)
	case utf8.RuneCountInString(s.Model) > 128:
		return fmt.Errorf("%w: model is longer than 128 characters", ErrInvalid)
	case utf8.RuneCountInString(s.Voice) > 64:
		return fmt.Errorf("%w: voice is longer than 64 characters", ErrInvalid)
	case s.Language != "" && !language.MatchString(s.Language):
		return fmt.Errorf("%w: language %q is not an ISO 639 code", ErrInvalid, s.Language)
	}
	return nil
}

type config struct {
	Provider Provider `json:"provider,omitempty"`
	Model    string   `json:"model,omitempty"`
	Voice    string   `json:"voice,omitempty"`
	Language string   `json:"language,omitempty"`
}

// Config is the document stored in agent_profiles.config and handed to the
// agent in the dispatch metadata.
func (s Spec) Config() json.RawMessage {
	raw, _ := json.Marshal(config{Provider: s.Provider, Model: s.Model, Voice: s.Voice, Language: s.Language})
	return raw
}

func SpecFromConfig(name string, raw json.RawMessage) Spec {
	var c config
	_ = json.Unmarshal(raw, &c)
	return Spec{Name: name, Provider: c.Provider, Model: c.Model, Voice: c.Voice, Language: c.Language}.Normalize()
}

// ParseManifest reads the agent profiles file. API keys never go in it: an
// unknown field is an error, not something silently ignored.
func ParseManifest(data []byte) ([]Spec, error) {
	var doc struct {
		Profiles *[]Spec `json:"profiles"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&doc); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if doc.Profiles == nil {
		return nil, fmt.Errorf(`%w: missing "profiles"`, ErrInvalid)
	}
	specs := make([]Spec, 0, len(*doc.Profiles))
	seen := map[string]bool{}
	for _, raw := range *doc.Profiles {
		spec := raw.Normalize()
		if err := spec.Validate(); err != nil {
			return nil, fmt.Errorf("profile %q: %w", spec.Name, err)
		}
		if seen[spec.Name] {
			return nil, fmt.Errorf("%w: profile %q is declared twice", ErrInvalid, spec.Name)
		}
		seen[spec.Name] = true
		specs = append(specs, spec)
	}
	return specs, nil
}

type Change struct {
	ID   uuid.UUID
	Spec Spec
}

// Plan brings the stored profiles in line with the file. A profile the file
// names becomes Kubernetes-owned even if the panel created it; one the file no
// longer names is deleted, or handed to the panel while speakers still use it.
type Plan struct {
	Create  []Spec
	Update  []Change
	Delete  []uuid.UUID
	Release []uuid.UUID
}

func (p Plan) Empty() bool {
	return len(p.Create) == 0 && len(p.Update) == 0 && len(p.Delete) == 0 && len(p.Release) == 0
}

func PlanSync(existing []Profile, manifest []Spec) Plan {
	byName := make(map[string]Profile, len(existing))
	for _, profile := range existing {
		byName[profile.Name] = profile
	}
	declared := make(map[string]bool, len(manifest))
	var plan Plan
	for _, spec := range manifest {
		declared[spec.Name] = true
		current, ok := byName[spec.Name]
		switch {
		case !ok:
			plan.Create = append(plan.Create, spec)
		case current.Spec != spec || current.Source != SourceKubernetes:
			plan.Update = append(plan.Update, Change{ID: current.ID, Spec: spec})
		}
	}
	for _, profile := range existing {
		if profile.Source != SourceKubernetes || declared[profile.Name] {
			continue
		}
		if profile.DeviceCount > 0 {
			plan.Release = append(plan.Release, profile.ID)
		} else {
			plan.Delete = append(plan.Delete, profile.ID)
		}
	}
	return plan
}
