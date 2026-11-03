package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/zetesis-labs/sebastian/server/internal/agentprofile"
)

type stubAgents struct {
	items  []agentprofile.Profile
	err    error
	creds  agentprofile.Credentials
	secret bool
	key    string
}

func (s *stubAgents) List(context.Context) ([]agentprofile.Profile, error) { return s.items, s.err }
func (s *stubAgents) Create(_ context.Context, spec agentprofile.Spec) (agentprofile.Profile, error) {
	return agentprofile.Profile{ID: uuid.New(), Spec: spec, Source: agentprofile.SourcePanel}, s.err
}
func (s *stubAgents) Update(_ context.Context, id uuid.UUID, spec agentprofile.Spec) (agentprofile.Profile, error) {
	return agentprofile.Profile{ID: id, Spec: spec}, s.err
}
func (s *stubAgents) Delete(context.Context, uuid.UUID) error { return s.err }
func (s *stubAgents) SetAPIKey(_ context.Context, _ uuid.UUID, key string) error {
	s.key = key
	return s.err
}
func (s *stubAgents) ClearAPIKey(context.Context, uuid.UUID) error { return s.err }
func (s *stubAgents) Credentials(context.Context, uuid.UUID) (agentprofile.Credentials, error) {
	return s.creds, s.err
}
func (s *stubAgents) SecretsKeyConfigured() bool { return s.secret }

func agentsHandler(agents AgentProfileService, devices DeviceService) *Server {
	return NewHandler(nil, nil, devices, nil, stubReadiness{}, testLogger(), time.Second).WithAgentProfiles(agents)
}

func TestListAgentProfilesReportsTheSecretsKeyAndNeverTheKey(t *testing.T) {
	agents := &stubAgents{secret: true, items: []agentprofile.Profile{{
		ID: uuid.New(), Spec: agentprofile.Spec{Name: "phonic", Provider: agentprofile.Phonic, Voice: "eduardo"},
		Source: agentprofile.SourceKubernetes, APIKeySet: true, DeviceCount: 1,
	}}}
	response, err := agentsHandler(agents, &stubDevices{}).ListAgentProfiles(context.Background(), ListAgentProfilesRequestObject{})
	if err != nil {
		t.Fatal(err)
	}
	list, ok := response.(ListAgentProfiles200JSONResponse)
	if !ok || !list.SecretsKeyConfigured || len(list.Items) != 1 {
		t.Fatalf("response = %#v", response)
	}
	item := list.Items[0]
	if item.Source != Kubernetes || item.Provider == nil || *item.Provider != Phonic || !item.ApiKeySet || item.DeviceCount != 1 || item.Model != nil {
		t.Fatalf("item = %#v", item)
	}
}

func TestEditingAKubernetesProfileIsAConflictWithAClearReason(t *testing.T) {
	agents := &stubAgents{err: agentprofile.ErrManaged}
	response, err := agentsHandler(agents, &stubDevices{}).UpdateAgentProfile(context.Background(), UpdateAgentProfileRequestObject{
		ProfileId: uuid.New(), Body: &AgentProfileInput{Name: "x", Provider: Gemini},
	})
	if err != nil {
		t.Fatal(err)
	}
	conflict, ok := response.(UpdateAgentProfile409ApplicationProblemPlusJSONResponse)
	if !ok || conflict.Detail == nil || !strings.Contains(*conflict.Detail, "Kubernetes") {
		t.Fatalf("response = %#v", response)
	}
}

func TestStoringAKeyOnAnAgentWithoutProviderIsAConflict(t *testing.T) {
	response, _ := agentsHandler(&stubAgents{err: agentprofile.ErrNoProvider}, &stubDevices{}).SetAgentProfileApiKey(context.Background(), SetAgentProfileApiKeyRequestObject{
		ProfileId: uuid.New(), Body: &AgentProfileApiKey{ApiKey: "AIza-12345678"},
	})
	conflict, ok := response.(SetAgentProfileApiKey409ApplicationProblemPlusJSONResponse)
	if !ok || !strings.Contains(*conflict.Detail, "choose a provider") {
		t.Fatalf("response = %#v", response)
	}
}

func TestALegacyAgentIsListedWithoutProvider(t *testing.T) {
	agents := &stubAgents{items: []agentprofile.Profile{{ID: uuid.New(), Spec: agentprofile.Spec{Name: "default"}, Source: agentprofile.SourcePanel}}}
	response, _ := agentsHandler(agents, &stubDevices{}).ListAgentProfiles(context.Background(), ListAgentProfilesRequestObject{})
	if list := response.(ListAgentProfiles200JSONResponse); list.Items[0].Provider != nil {
		t.Fatalf("provider = %v, want absent", *list.Items[0].Provider)
	}
}

func TestDeletingAnAgentInUseIsAConflict(t *testing.T) {
	response, _ := agentsHandler(&stubAgents{err: agentprofile.ErrInUse}, &stubDevices{}).DeleteAgentProfile(context.Background(), DeleteAgentProfileRequestObject{ProfileId: uuid.New()})
	if _, ok := response.(DeleteAgentProfile409ApplicationProblemPlusJSONResponse); !ok {
		t.Fatalf("response = %T", response)
	}
}

func TestSetAPIKeyWithoutTheSecretsKeyIs503(t *testing.T) {
	response, _ := agentsHandler(&stubAgents{err: agentprofile.ErrNoSecretsKey}, &stubDevices{}).SetAgentProfileApiKey(context.Background(), SetAgentProfileApiKeyRequestObject{
		ProfileId: uuid.New(), Body: &AgentProfileApiKey{ApiKey: "ph_123456789"},
	})
	unavailable, ok := response.(SetAgentProfileApiKey503ApplicationProblemPlusJSONResponse)
	if !ok || !strings.Contains(*unavailable.Detail, "SEBASTIAN_SECRETS_KEY") {
		t.Fatalf("response = %#v", response)
	}
}

func TestUpdateDeviceAssignsTheAgent(t *testing.T) {
	devices := &stubDevices{}
	id := uuid.New()
	response, err := agentsHandler(&stubAgents{}, devices).UpdateDevice(context.Background(), UpdateDeviceRequestObject{
		DeviceId: "e072a1f96ef0", Body: &DeviceUpdate{AgentProfileId: &id},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := response.(UpdateDevice204Response); !ok || devices.assigned != id {
		t.Fatalf("response = %T, assigned = %s", response, devices.assigned)
	}
}

func TestUpdateDeviceWithAnUnknownAgentIs422(t *testing.T) {
	id := uuid.New()
	response, _ := agentsHandler(&stubAgents{}, &stubDevices{err: agentprofile.ErrNotFound}).UpdateDevice(context.Background(), UpdateDeviceRequestObject{
		DeviceId: "e072a1f96ef0", Body: &DeviceUpdate{AgentProfileId: &id},
	})
	if _, ok := response.(UpdateDevice422ApplicationProblemPlusJSONResponse); !ok {
		t.Fatalf("response = %T", response)
	}
}

func credentialsRequest(t *testing.T, agents AgentProfileService, secret, id string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle("GET /v1/agent-profiles/{id}/credentials", AgentProfileCredentials(agents, "agent-secret", testLogger()))
	request := httptest.NewRequest(http.MethodGet, "/v1/agent-profiles/"+id+"/credentials", nil)
	request.Header.Set("X-Agent-Secret", secret)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	return recorder
}

func TestAgentCredentialsNeedTheAgentSecret(t *testing.T) {
	agents := &stubAgents{creds: agentprofile.Credentials{Provider: agentprofile.Phonic, APIKey: "ph_secret"}}
	recorder := credentialsRequest(t, agents, "wrong", uuid.NewString())
	if recorder.Code != http.StatusUnauthorized || strings.Contains(recorder.Body.String(), "ph_secret") {
		t.Fatalf("code = %d, body = %s", recorder.Code, recorder.Body)
	}
}

func TestAgentCredentialsHandTheKeyOrOmitIt(t *testing.T) {
	agents := &stubAgents{creds: agentprofile.Credentials{Provider: agentprofile.Phonic, APIKey: "ph_secret"}}
	recorder := credentialsRequest(t, agents, "agent-secret", uuid.NewString())
	var body map[string]string
	_ = json.Unmarshal(recorder.Body.Bytes(), &body)
	if recorder.Code != http.StatusOK || body["provider"] != "phonic" || body["apiKey"] != "ph_secret" || recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("code = %d, body = %s", recorder.Code, recorder.Body)
	}

	agents.creds = agentprofile.Credentials{Provider: agentprofile.Gemini}
	recorder = credentialsRequest(t, agents, "agent-secret", uuid.NewString())
	body = map[string]string{}
	_ = json.Unmarshal(recorder.Body.Bytes(), &body)
	if _, present := body["apiKey"]; recorder.Code != http.StatusOK || present {
		t.Fatalf("code = %d, body = %s", recorder.Code, recorder.Body)
	}
}

func TestAgentCredentialsForAnUnknownProfileAre404(t *testing.T) {
	recorder := credentialsRequest(t, &stubAgents{err: agentprofile.ErrNotFound}, "agent-secret", uuid.NewString())
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("code = %d", recorder.Code)
	}
	recorder = credentialsRequest(t, &stubAgents{}, "agent-secret", "not-a-uuid")
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("code = %d", recorder.Code)
	}
}
