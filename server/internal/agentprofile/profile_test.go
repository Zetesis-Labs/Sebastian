package agentprofile

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
)

func TestValidateAcceptsTheThreeProviders(t *testing.T) {
	for _, p := range []Provider{Gemini, OpenAI, Phonic} {
		if err := (Spec{Name: "casa", Provider: p}).Validate(); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
	}
}

func TestValidateRejects(t *testing.T) {
	cases := map[string]Spec{
		"empty name":       {Name: " ", Provider: Gemini},
		"long name":        {Name: string(make([]byte, 65)), Provider: Gemini},
		"unknown provider": {Name: "x", Provider: "ultravox"},
		"bad language":     {Name: "x", Provider: Phonic, Language: "es_ES"},
		"long voice":       {Name: "x", Provider: Phonic, Voice: string(make([]byte, 65))},
	}
	for name, spec := range cases {
		if err := spec.Validate(); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s: got %v, want ErrInvalid", name, err)
		}
	}
}

func TestNormalizeTrims(t *testing.T) {
	got := Spec{Name: " casa ", Provider: " Phonic ", Model: " ", Voice: " eduardo", Language: "ES "}.Normalize()
	want := Spec{Name: "casa", Provider: Phonic, Voice: "eduardo", Language: "es"}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestConfigRoundTripOmitsEmptyFields(t *testing.T) {
	spec := Spec{Name: "casa", Provider: Phonic, Voice: "eduardo", Language: "es"}
	raw := spec.Config()
	var doc map[string]string
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc) != 3 || doc["provider"] != "phonic" || doc["voice"] != "eduardo" || doc["language"] != "es" {
		t.Fatalf("config = %s", raw)
	}
	if back := SpecFromConfig("casa", raw); back != spec {
		t.Fatalf("round trip = %+v", back)
	}
}

func TestALegacyEmptyConfigHasNoProviderSoTheAgentUsesItsOwn(t *testing.T) {
	got := SpecFromConfig("default", json.RawMessage(`{}`))
	if got.Provider != "" || got.Name != "default" {
		t.Fatalf("got %+v", got)
	}
}

func TestParseManifest(t *testing.T) {
	specs, err := ParseManifest([]byte(`{"profiles":[
		{"name":"gemini","provider":"gemini","voice":"Puck"},
		{"name":"phonic","provider":"phonic","voice":"eduardo","language":"es"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(specs) != 2 || specs[1] != (Spec{Name: "phonic", Provider: Phonic, Voice: "eduardo", Language: "es"}) {
		t.Fatalf("specs = %+v", specs)
	}
}

func TestParseManifestRejects(t *testing.T) {
	cases := map[string]string{
		"not json":       `profiles: []`,
		"unknown field":  `{"profiles":[{"name":"a","provider":"gemini","apiKey":"sk-1"}]}`,
		"duplicate name": `{"profiles":[{"name":"a","provider":"gemini"},{"name":" a ","provider":"openai"}]}`,
		"invalid spec":   `{"profiles":[{"name":"a","provider":"nope"}]}`,
		"no profiles":    `{"agents":[]}`,
	}
	for name, doc := range cases {
		if _, err := ParseManifest([]byte(doc)); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s: got %v, want ErrInvalid", name, err)
		}
	}
}

func TestPlanSync(t *testing.T) {
	kept := Profile{ID: uuid.New(), Spec: Spec{Name: "gemini", Provider: Gemini, Voice: "Puck"}, Source: SourceKubernetes}
	changed := Profile{ID: uuid.New(), Spec: Spec{Name: "phonic", Provider: Phonic}, Source: SourceKubernetes}
	takenOver := Profile{ID: uuid.New(), Spec: Spec{Name: "openai", Provider: OpenAI}, Source: SourcePanel}
	droppedUnused := Profile{ID: uuid.New(), Spec: Spec{Name: "viejo", Provider: Gemini}, Source: SourceKubernetes}
	droppedInUse := Profile{ID: uuid.New(), Spec: Spec{Name: "salon", Provider: Gemini}, Source: SourceKubernetes, DeviceCount: 2}
	panelOnly := Profile{ID: uuid.New(), Spec: Spec{Name: "pruebas", Provider: Phonic}, Source: SourcePanel}

	manifest := []Spec{
		{Name: "gemini", Provider: Gemini, Voice: "Puck"},
		{Name: "phonic", Provider: Phonic, Voice: "eduardo", Language: "es"},
		{Name: "openai", Provider: OpenAI, Voice: "alloy"},
		{Name: "nuevo", Provider: Phonic},
	}
	plan := PlanSync([]Profile{kept, changed, takenOver, droppedUnused, droppedInUse, panelOnly}, manifest)

	if len(plan.Create) != 1 || plan.Create[0].Name != "nuevo" {
		t.Fatalf("create = %+v", plan.Create)
	}
	if len(plan.Update) != 2 {
		t.Fatalf("update = %+v", plan.Update)
	}
	updated := map[uuid.UUID]Spec{}
	for _, u := range plan.Update {
		updated[u.ID] = u.Spec
	}
	if updated[changed.ID].Voice != "eduardo" || updated[takenOver.ID].Voice != "alloy" {
		t.Fatalf("update = %+v", plan.Update)
	}
	if len(plan.Delete) != 1 || plan.Delete[0] != droppedUnused.ID {
		t.Fatalf("delete = %+v", plan.Delete)
	}
	if len(plan.Release) != 1 || plan.Release[0] != droppedInUse.ID {
		t.Fatalf("release = %+v", plan.Release)
	}
}

func TestPlanSyncWithNoChangesIsEmpty(t *testing.T) {
	existing := []Profile{{ID: uuid.New(), Spec: Spec{Name: "gemini", Provider: Gemini}, Source: SourceKubernetes}}
	plan := PlanSync(existing, []Spec{{Name: "gemini", Provider: Gemini}})
	if !plan.Empty() {
		t.Fatalf("plan = %+v", plan)
	}
}
