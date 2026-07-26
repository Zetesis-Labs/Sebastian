package messaging

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/zetesis-labs/sebastian/server/internal/outbox"
)

type Publisher struct {
	connection *nats.Conn
	jetstream  jetstream.JetStream
	timeout    time.Duration
}

type cloudEvent struct {
	SpecVersion string          `json:"specversion"`
	ID          string          `json:"id"`
	Source      string          `json:"source"`
	Type        string          `json:"type"`
	Subject     string          `json:"subject"`
	Time        time.Time       `json:"time"`
	Data        json.RawMessage `json:"data"`
}

func NewPublisher(
	ctx context.Context,
	url string,
	streamName string,
	subjects string,
	timeout time.Duration,
) (*Publisher, error) {
	connection, err := nats.Connect(
		url,
		nats.Name("sebastian-outbox"),
		nats.Timeout(timeout),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(time.Second),
	)
	if err != nil {
		return nil, fmt.Errorf("connect to NATS: %w", err)
	}
	js, err := jetstream.New(connection)
	if err != nil {
		connection.Close()
		return nil, fmt.Errorf("create JetStream client: %w", err)
	}
	streamCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if _, err := js.CreateOrUpdateStream(streamCtx, jetstream.StreamConfig{
		Name:        streamName,
		Description: "Sebastian domain events",
		Subjects:    []string{subjects},
		Retention:   jetstream.LimitsPolicy,
		Discard:     jetstream.DiscardOld,
		MaxAge:      30 * 24 * time.Hour,
		Storage:     jetstream.FileStorage,
		Replicas:    1,
		Duplicates:  24 * time.Hour,
		AllowDirect: true,
	}); err != nil {
		connection.Close()
		return nil, fmt.Errorf("ensure JetStream event stream: %w", err)
	}
	return &Publisher{connection: connection, jetstream: js, timeout: timeout}, nil
}

func (p *Publisher) Publish(ctx context.Context, event outbox.Event) error {
	payload, err := marshalCloudEvent(event)
	if err != nil {
		return err
	}
	publishCtx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	message := &nats.Msg{
		Subject: event.Subject,
		Header: nats.Header{
			"Content-Type":       []string{"application/cloudevents+json"},
			"Sebastian-Event-Id": []string{event.ID.String()},
		},
		Data: payload,
	}
	if _, err := p.jetstream.PublishMsg(publishCtx, message, jetstream.WithMsgID(event.ID.String())); err != nil {
		return fmt.Errorf("publish event %s: %w", event.ID, err)
	}
	return nil
}

func (p *Publisher) Close() {
	p.connection.Close()
}

func marshalCloudEvent(event outbox.Event) ([]byte, error) {
	payload, err := json.Marshal(cloudEvent{
		SpecVersion: "1.0",
		ID:          event.ID.String(),
		Source:      "urn:sebastian:" + event.AggregateType,
		Type:        event.Type,
		Subject:     event.AggregateID,
		Time:        event.OccurredAt.UTC(),
		Data:        event.Data,
	})
	if err != nil {
		return nil, fmt.Errorf("encode event %s: %w", event.ID, err)
	}
	return payload, nil
}
