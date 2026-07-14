package messaging

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/zetesis-labs/sebastian/server/internal/outbox"
)

func TestMarshalCloudEvent(t *testing.T) {
	id := uuid.New()
	payload, err := marshalCloudEvent(outbox.Event{
		ID:            id,
		Type:          "session.created",
		AggregateType: "session",
		AggregateID:   "session-1",
		OccurredAt:    time.Date(2026, 7, 14, 10, 0, 0, 0, time.FixedZone("CEST", 2*60*60)),
		Data:          json.RawMessage(`{"room":"sebastian-1"}`),
	})
	if err != nil {
		t.Fatalf("marshalCloudEvent() error = %v", err)
	}
	var event cloudEvent
	if err := json.Unmarshal(payload, &event); err != nil {
		t.Fatalf("decode cloud event: %v", err)
	}
	if event.SpecVersion != "1.0" || event.ID != id.String() || event.Source != "urn:sebastian:session" {
		t.Fatalf("unexpected cloud event: %#v", event)
	}
	if event.Time.Location() != time.UTC || string(event.Data) != `{"room":"sebastian-1"}` {
		t.Fatalf("unexpected normalized event: %#v", event)
	}
}
