//go:build integration

package messaging

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/zetesis-labs/sebastian/server/internal/outbox"
)

func TestPublisherStoresDeduplicatedCloudEvent(t *testing.T) {
	url := os.Getenv("NATS_URL")
	if url == "" {
		t.Skip("NATS_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	testID := uuid.NewString()
	streamName := "SEBASTIAN_TEST_" + testID
	subjectPrefix := "test." + testID + ".sebastian"
	publisher, err := NewPublisher(ctx, url, streamName, subjectPrefix+".>", 3*time.Second)
	if err != nil {
		t.Fatalf("NewPublisher: %v", err)
	}
	defer publisher.Close()
	t.Cleanup(func() { _ = publisher.jetstream.DeleteStream(context.Background(), streamName) })

	event := outbox.Event{
		ID:            uuid.New(),
		Type:          "session.created",
		AggregateType: "session",
		AggregateID:   uuid.NewString(),
		Subject:       subjectPrefix + ".session.created",
		OccurredAt:    time.Now().UTC(),
		Data:          json.RawMessage(`{"room":"integration"}`),
	}
	if err := publisher.Publish(ctx, event); err != nil {
		t.Fatalf("Publish(first): %v", err)
	}
	if err := publisher.Publish(ctx, event); err != nil {
		t.Fatalf("Publish(duplicate): %v", err)
	}
	stream, err := publisher.jetstream.Stream(ctx, streamName)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	info, err := stream.Info(ctx)
	if err != nil {
		t.Fatalf("Stream.Info: %v", err)
	}
	if info.State.Msgs != 1 {
		t.Fatalf("stream messages = %d, want 1", info.State.Msgs)
	}
	message, err := stream.GetLastMsgForSubject(ctx, event.Subject)
	if err != nil {
		t.Fatalf("GetLastMsgForSubject: %v", err)
	}
	var stored cloudEvent
	if err := json.Unmarshal(message.Data, &stored); err != nil {
		t.Fatalf("decode stored event: %v", err)
	}
	if stored.ID != event.ID.String() || stored.Data == nil {
		t.Fatalf("unexpected stored event: %#v", stored)
	}
}
