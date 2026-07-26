// Package device owns the device inventory and the desired-profile
// reconciliation state. The "device profile" here is the firmware-side
// personality (NVS profile: agent / usb_mic) — a concept distinct from
// agent_profiles, which configure the cloud LiveKit agent.
package device

import (
	"context"
	"errors"
	"time"
)

var ErrNotFound = errors.New("device: not found")

type Device struct {
	ID                string
	DisplayName       string
	Enabled           bool
	DesiredProfile    string
	ReportedProfile   string
	ProfileReportedAt time.Time // zero value = the device never polled
}

type Store interface {
	// TouchProfile upserts the device (auto-registration on first contact),
	// records the profile it reports running, and returns the desired profile
	// name ("" when none is set).
	TouchProfile(ctx context.Context, id, reported string) (string, error)
	List(ctx context.Context) ([]Device, error)
	// SetDesiredProfile stores the reconciliation target; an empty name
	// clears it. ErrNotFound when the device id is unknown.
	SetDesiredProfile(ctx context.Context, id, name string) error
}

type Service struct {
	store Store
}

func NewService(store Store) *Service {
	return &Service{store: store}
}

func (s *Service) Reconcile(ctx context.Context, id, reported string) (string, error) {
	return s.store.TouchProfile(ctx, id, reported)
}

func (s *Service) List(ctx context.Context) ([]Device, error) {
	return s.store.List(ctx)
}

func (s *Service) SetDesired(ctx context.Context, id, name string) error {
	return s.store.SetDesiredProfile(ctx, id, name)
}
