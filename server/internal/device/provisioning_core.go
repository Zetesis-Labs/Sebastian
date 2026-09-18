package device

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"maps"
	"strings"
	"time"

	"github.com/zetesis-labs/sebastian/server/internal/adoption"
)

// Functional core of provisioning: the documents that go to a device and the
// codes the operator sees. Pure functions; the Service applies them.

// ConfigVersion is a short content hash: same document, same version, so a
// device that already runs it does not restart.
func ConfigVersion(config map[string]any) (string, json.RawMessage, error) {
	delete(config, "configVersion")
	if config["schema"] == nil {
		config["schema"] = "sebastian.config.v1"
	}
	canonical, err := json.Marshal(config) // map keys are sorted
	if err != nil {
		return "", nil, err
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:6]), canonical, nil
}

// adoptionConfig is what binds a unit to this control room (design §3, block 3).
func adoptionConfig(room ControlRoom, deviceSecret string, overrides map[string]any) (string, error) {
	doc := map[string]any{
		"schema":  "sebastian.config.v1",
		"livekit": map[string]any{"tokenServerUrl": room.origin() + "/token"},
		"adoption": map[string]any{
			"orgSecret":    room.OrgSecret,
			"deviceSecret": deviceSecret,
		},
		"configVersion": "",
	}
	if room.SyslogIP != "" {
		port := room.SyslogPort
		if port == 0 {
			port = 514
		}
		doc["telemetry"] = map[string]any{"syslogIp": room.SyslogIP, "syslogPort": port}
	}
	for k, v := range overrides {
		switch k {
		case "schema", "adoption", "configVersion":
			continue
		case "livekit", "telemetry":
			base, _ := doc[k].(map[string]any)
			over, _ := v.(map[string]any)
			if base == nil {
				base = map[string]any{}
			}
			maps.Copy(base, over)
			doc[k] = base
		default:
			doc[k] = v
		}
	}
	body, err := json.Marshal(doc)
	return string(body), err
}

// Enrolment (RF-03/RF-51): a unit provisioned from the embedded installer holds
// the organization secret but no device secret, and asks for one over HTTP.
// It proves the secret the way LAN adoption does — HMAC over a nonce — so the
// secret itself never travels.
type enrollChallenge struct {
	Nonce  string
	Issued time.Time
}

const enrollTTL = 60 * time.Second

func enrollProof(orgSecret, nonce, deviceID string) string {
	return adoption.Sign(orgSecret, nonce, deviceID)
}

func enrollValid(ch enrollChallenge, nonce, mac, orgSecret, deviceID string, now time.Time) bool {
	if ch.Nonce == "" || nonce != ch.Nonce || now.Sub(ch.Issued) > enrollTTL {
		return false
	}
	return hmac.Equal([]byte(strings.ToLower(mac)), []byte(enrollProof(orgSecret, nonce, deviceID)))
}

func forgetConfig() string {
	return `{"schema":"sebastian.config.v1","livekit":{"tokenServerUrl":""},"adoption":{"orgSecret":"","deviceSecret":""},"configVersion":""}`
}

func phaseOf(p adoption.Phase) string {
	switch p {
	case adoption.PhaseWaitingConsent:
		return "waiting_consent"
	case adoption.PhaseQueued:
		return "queued"
	default:
		return "starting"
	}
}

func errorCode(err error) string {
	switch {
	case errors.Is(err, adoption.ErrDenied):
		return "auth"
	case errors.Is(err, adoption.ErrNonce):
		return "nonce"
	case errors.Is(err, adoption.ErrConsentTimeout):
		return "consent_timeout"
	case errors.Is(err, adoption.ErrNoReply), errors.Is(err, context.DeadlineExceeded):
		return "no_reply"
	case errors.Is(err, ErrNoAddress):
		return "no_address"
	case errors.Is(err, adoption.ErrRejected):
		return "rejected:" + strings.TrimPrefix(err.Error(), adoption.ErrRejected.Error()+": ")
	default:
		return "transport"
	}
}
