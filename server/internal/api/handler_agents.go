package api

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/google/uuid"
	"github.com/zetesis-labs/sebastian/server/internal/agentprofile"
)

type AgentProfileService interface {
	List(ctx context.Context) ([]agentprofile.Profile, error)
	Create(ctx context.Context, spec agentprofile.Spec) (agentprofile.Profile, error)
	Update(ctx context.Context, id uuid.UUID, spec agentprofile.Spec) (agentprofile.Profile, error)
	Delete(ctx context.Context, id uuid.UUID) error
	SetAPIKey(ctx context.Context, id uuid.UUID, key string) error
	ClearAPIKey(ctx context.Context, id uuid.UUID) error
	Credentials(ctx context.Context, id uuid.UUID) (agentprofile.Credentials, error)
	SecretsKeyConfigured() bool
}

func (h *Server) WithAgentProfiles(agents AgentProfileService) *Server {
	h.agents = agents
	return h
}

func (h *Server) ListAgentProfiles(ctx context.Context, _ ListAgentProfilesRequestObject) (ListAgentProfilesResponseObject, error) {
	if h.agents == nil {
		return ListAgentProfiles503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse: agentsUnavailable()}, nil
	}
	items, err := h.agents.List(ctx)
	if err != nil {
		return ListAgentProfiles503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse: h.unavailable(ctx, "list agent profiles failed", err)}, nil
	}
	response := AgentProfileList{Items: make([]AgentProfile, 0, len(items)), SecretsKeyConfigured: h.agents.SecretsKeyConfigured()}
	for _, item := range items {
		response.Items = append(response.Items, agentProfileResponse(item))
	}
	return ListAgentProfiles200JSONResponse(response), nil
}

func (h *Server) CreateAgentProfile(ctx context.Context, request CreateAgentProfileRequestObject) (CreateAgentProfileResponseObject, error) {
	if h.agents == nil {
		return CreateAgentProfile503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse: agentsUnavailable()}, nil
	}
	created, err := h.agents.Create(ctx, specFromInput(request.Body))
	switch {
	case errors.Is(err, agentprofile.ErrConflict), errors.Is(err, agentprofile.ErrInvalid):
		return CreateAgentProfile409ApplicationProblemPlusJSONResponse{AgentProfileConflictApplicationProblemPlusJSONResponse: agentConflict(err)}, nil
	case err != nil:
		return CreateAgentProfile503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse: h.unavailable(ctx, "create agent profile failed", err)}, nil
	}
	return CreateAgentProfile201JSONResponse(agentProfileResponse(created)), nil
}

func (h *Server) UpdateAgentProfile(ctx context.Context, request UpdateAgentProfileRequestObject) (UpdateAgentProfileResponseObject, error) {
	if h.agents == nil {
		return UpdateAgentProfile503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse: agentsUnavailable()}, nil
	}
	updated, err := h.agents.Update(ctx, request.ProfileId, specFromInput(request.Body))
	switch {
	case errors.Is(err, agentprofile.ErrNotFound):
		return UpdateAgentProfile404ApplicationProblemPlusJSONResponse{AgentProfileNotFoundApplicationProblemPlusJSONResponse: agentNotFound()}, nil
	case errors.Is(err, agentprofile.ErrConflict), errors.Is(err, agentprofile.ErrInvalid):
		return UpdateAgentProfile409ApplicationProblemPlusJSONResponse{AgentProfileConflictApplicationProblemPlusJSONResponse: agentConflict(err)}, nil
	case err != nil:
		return UpdateAgentProfile503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse: h.unavailable(ctx, "update agent profile failed", err, "profile_id", request.ProfileId)}, nil
	}
	return UpdateAgentProfile200JSONResponse(agentProfileResponse(updated)), nil
}

func (h *Server) DeleteAgentProfile(ctx context.Context, request DeleteAgentProfileRequestObject) (DeleteAgentProfileResponseObject, error) {
	if h.agents == nil {
		return DeleteAgentProfile503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse: agentsUnavailable()}, nil
	}
	err := h.agents.Delete(ctx, request.ProfileId)
	switch {
	case errors.Is(err, agentprofile.ErrNotFound):
		return DeleteAgentProfile404ApplicationProblemPlusJSONResponse{AgentProfileNotFoundApplicationProblemPlusJSONResponse: agentNotFound()}, nil
	case errors.Is(err, agentprofile.ErrConflict):
		return DeleteAgentProfile409ApplicationProblemPlusJSONResponse{AgentProfileConflictApplicationProblemPlusJSONResponse: agentConflict(err)}, nil
	case err != nil:
		return DeleteAgentProfile503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse: h.unavailable(ctx, "delete agent profile failed", err, "profile_id", request.ProfileId)}, nil
	}
	return DeleteAgentProfile204Response{}, nil
}

func (h *Server) SetAgentProfileApiKey(ctx context.Context, request SetAgentProfileApiKeyRequestObject) (SetAgentProfileApiKeyResponseObject, error) {
	if h.agents == nil {
		return SetAgentProfileApiKey503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse: agentsUnavailable()}, nil
	}
	key := ""
	if request.Body != nil {
		key = request.Body.ApiKey
	}
	err := h.agents.SetAPIKey(ctx, request.ProfileId, key)
	switch {
	case errors.Is(err, agentprofile.ErrNotFound):
		return SetAgentProfileApiKey404ApplicationProblemPlusJSONResponse{AgentProfileNotFoundApplicationProblemPlusJSONResponse: agentNotFound()}, nil
	case errors.Is(err, agentprofile.ErrConflict), errors.Is(err, agentprofile.ErrInvalid):
		return SetAgentProfileApiKey409ApplicationProblemPlusJSONResponse{AgentProfileConflictApplicationProblemPlusJSONResponse: agentConflict(err)}, nil
	case errors.Is(err, agentprofile.ErrNoSecretsKey):
		return SetAgentProfileApiKey503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse: UnavailableApplicationProblemPlusJSONResponse(problem(
			503, "API keys unavailable", "The server has no SEBASTIAN_SECRETS_KEY to encrypt API keys with.",
		))}, nil
	case err != nil:
		return SetAgentProfileApiKey503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse: h.unavailable(ctx, "set agent profile api key failed", err, "profile_id", request.ProfileId)}, nil
	}
	return SetAgentProfileApiKey204Response{}, nil
}

func (h *Server) ClearAgentProfileApiKey(ctx context.Context, request ClearAgentProfileApiKeyRequestObject) (ClearAgentProfileApiKeyResponseObject, error) {
	if h.agents == nil {
		return ClearAgentProfileApiKey503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse: agentsUnavailable()}, nil
	}
	err := h.agents.ClearAPIKey(ctx, request.ProfileId)
	switch {
	case errors.Is(err, agentprofile.ErrNotFound):
		return ClearAgentProfileApiKey404ApplicationProblemPlusJSONResponse{AgentProfileNotFoundApplicationProblemPlusJSONResponse: agentNotFound()}, nil
	case err != nil:
		return ClearAgentProfileApiKey503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse: h.unavailable(ctx, "clear agent profile api key failed", err, "profile_id", request.ProfileId)}, nil
	}
	return ClearAgentProfileApiKey204Response{}, nil
}

// AgentProfileCredentials hands the agent the provider and stored API key of
// the profile it was dispatched with: GET /v1/agent-profiles/{id}/credentials
// with X-Agent-Secret. Outside the OpenAPI router like the other agent routes;
// the key never travels in the LiveKit dispatch metadata.
func AgentProfileCredentials(agents AgentProfileService, agentSecret string, logger *slog.Logger) http.Handler {
	expected := sha256.Sum256([]byte(agentSecret))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		provided := sha256.Sum256([]byte(r.Header.Get("X-Agent-Secret")))
		if agentSecret == "" || subtle.ConstantTimeCompare(expected[:], provided[:]) != 1 {
			writeProblem(w, 401, "Unauthorized", "Invalid agent credentials.")
			return
		}
		id, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			writeProblem(w, 404, "Agent profile not found", "The agent profile does not exist.")
			return
		}
		creds, err := agents.Credentials(r.Context(), id)
		switch {
		case errors.Is(err, agentprofile.ErrNotFound):
			writeProblem(w, 404, "Agent profile not found", "The agent profile does not exist.")
			return
		case err != nil:
			logger.ErrorContext(r.Context(), "agent credentials unavailable", "profile_id", id, "error", err)
			writeProblem(w, 503, "Credentials unavailable", "The API key of the agent profile could not be read.")
			return
		}
		body := map[string]string{}
		if creds.Provider != "" {
			body["provider"] = string(creds.Provider)
		}
		if creds.APIKey != "" {
			body["apiKey"] = creds.APIKey
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(body)
	})
}

func specFromInput(body *AgentProfileInput) agentprofile.Spec {
	if body == nil {
		return agentprofile.Spec{}
	}
	spec := agentprofile.Spec{Name: body.Name, Provider: agentprofile.Provider(body.Provider)}
	if body.Model != nil {
		spec.Model = *body.Model
	}
	if body.Voice != nil {
		spec.Voice = *body.Voice
	}
	if body.Language != nil {
		spec.Language = *body.Language
	}
	return spec
}

func agentProfileResponse(p agentprofile.Profile) AgentProfile {
	return AgentProfile{
		Id: p.ID, Name: p.Name, Source: AgentProfileSource(p.Source), Provider: optionalProvider(p.Provider),
		Model: optional(p.Model), Voice: optional(p.Voice), Language: optional(p.Language),
		ApiKeySet: p.APIKeySet, ApiKeyUpdatedAt: optionalTime(p.APIKeyUpdatedAt),
		DeviceCount: p.DeviceCount, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
	}
}

func optionalProvider(p agentprofile.Provider) *AgentProvider {
	if p == "" {
		return nil
	}
	value := AgentProvider(p)
	return &value
}

func agentConflict(err error) AgentProfileConflictApplicationProblemPlusJSONResponse {
	detail := err.Error()
	switch {
	case errors.Is(err, agentprofile.ErrNoProvider):
		detail = "This agent uses the agent's environment provider; choose a provider before storing a key."
	case errors.Is(err, agentprofile.ErrManaged):
		detail = "This agent is declared in Kubernetes; change it in the agent profiles file."
	case errors.Is(err, agentprofile.ErrInUse):
		detail = "Speakers still talk through this agent; move them to another one first."
	}
	return AgentProfileConflictApplicationProblemPlusJSONResponse(problem(409, "Agent profile conflict", detail))
}

func agentNotFound() AgentProfileNotFoundApplicationProblemPlusJSONResponse {
	return AgentProfileNotFoundApplicationProblemPlusJSONResponse(problem(404, "Agent profile not found", "The agent profile does not exist."))
}

func agentsUnavailable() UnavailableApplicationProblemPlusJSONResponse {
	return UnavailableApplicationProblemPlusJSONResponse(problem(503, "Agents unavailable", "This server does not manage agent profiles."))
}
