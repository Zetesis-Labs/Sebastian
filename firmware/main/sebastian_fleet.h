// Fleet provisioning + adoption from the control room
// (docs/implementation/11-fleet-adoption-control-room.md). C-side surface shared
// by provisioning.c (store), adopt.c (LAN listener), announce.c (mDNS) and
// session_http.c (authenticated HTTP), bound from Zig in csdk.zig.
#pragma once

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

// UDP port of the adoption listener (adopt.c) and the port announced over mDNS.
#define SEBASTIAN_ADOPT_PORT 5688

// ── provisioning.c ──────────────────────────────────────────────────────────
// Parse + schema-check + store a sebastian.config.v1 document. Never restarts.
bool sebastian_provisioning_apply(const char *json, char *err, size_t err_size);
bool sebastian_get_token_url(char *out, size_t out_size);
bool sebastian_get_device_secret(char *out, size_t out_size);
// Generate the unit's own secret if it has none (born with the unit, RF-51).
bool sebastian_ensure_device_secret(void);
// Binding memory: which control room (origin of the token URL) already holds
// our secret. Set after an adoption or enrolment; cleared to force re-enrolment.
void sebastian_mark_bound(void);
void sebastian_clear_bound(void);
bool sebastian_is_bound(void);
bool sebastian_get_org_secret(char *out, size_t out_size);
bool sebastian_get_cfg_version(char *out, size_t out_size);
// One-shot error recorded by a previous boot (e.g. "wifi-rollback").
bool sebastian_take_last_error(char *out, size_t out_size);

// ── session_http.c ──────────────────────────────────────────────────────────
// POST {base}/v1/sessions with the device credentials. Returns 0 and fills the
// LiveKit URL + token on success, the HTTP status when the server answered
// something else, or a negative value for transport/parse errors.
int sebastian_session_create(const char *base_url, const char *device_id, const char *secret,
                             char *url_out, size_t url_size, char *token_out, size_t token_size);
// Enrolment (RF-03/RF-51): a unit that holds the organization secret hands its
// own device secret to {base}/v1/devices/{id}/enroll, proving the organization
// secret with an HMAC over the server's nonce. Returns 0 when the control room
// adopted it, the HTTP status when it refused, or negative for transport/parse
// errors.
int sebastian_enroll(const char *base_url, const char *device_id, const char *org_secret, const char *device_secret);
// GET with optional device credentials, capturing one response header. Returns
// the body length (>= 0), or negative: -1 init, -2 open, -3 http (status in
// *status), -4 read.
int sebastian_http_get_auth(const char *url, const char *device_id, const char *secret,
                            const char *capture_header, char *hdr_out, size_t hdr_size,
                            char *out, size_t out_size, int *status);

// ── announce.c ──────────────────────────────────────────────────────────────
// Advertise _sebastian._tcp on the LAN. Call once the network is up.
void sebastian_announce_start(const char *id, const char *prof, const char *cr);
// Update one TXT record (err, cfg, cr, prof).
void sebastian_announce_set(const char *key, const char *value);
const char *sebastian_fw_version(void);

// ── adopt.c ─────────────────────────────────────────────────────────────────
void sebastian_adopt_start(void);
// The app tells the listener when a conversation is open: config changes and
// the restart they imply wait for it to end (up to 2 min).
void sebastian_adopt_session_active(bool active);
bool sebastian_adopt_session_is_active(void);
// Physical consent for a factory unit: the ring blinks amber while pending;
// xvf_ui grants it when the MUTE button toggles.
bool sebastian_adopt_consent_pending(void);
void sebastian_adopt_consent_grant(void);
// Peer that last failed authentication ("adopt-denied:<ip>"), "" when none.
const char *sebastian_adopt_last_denied(void);
// hex HMAC-SHA256(secret, a + "." + b) — the proof shared by adoption and enrolment.
bool sebastian_hmac_sha256_hex(const char *secret, const char *a, const char *b, char out[65]);
