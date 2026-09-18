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
// The stored config as JSON, without secrets (for the RF-42 report). Free with
// sebastian_config_dump_free. NULL when NVS cannot be opened.
char *sebastian_config_dump_json(void);
void sebastian_config_dump_free(char *s);
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
                             const char *json_body, // NULL = no body (a conversation)
                             char *url_out, size_t url_size, char *token_out, size_t token_size);
// PUT {base}/v1/devices/{id}/meeting: the unit confirms it records, or that it
// stopped and why (meeting recordings, RM-52). Returns 0 on 204, the HTTP
// status when refused, negative on transport errors.
int sebastian_report_meeting(const char *base_url, const char *device_id, const char *secret,
                             const char *meeting_id, const char *state, const char *reason);
// POST {base}/v1/devices/{id}/meetings: a gesture start asks the control room
// for a meeting (RM-01/02); fills its id. 0 on 202, HTTP status when refused
// (409 busy, 422 profile), negative on transport/parse errors.
int sebastian_request_meeting(const char *base_url, const char *device_id, const char *secret,
                              char *id_out, size_t id_size);
// Enrolment (RF-03/RF-51): a unit that holds the organization secret hands its
// own device secret to {base}/v1/devices/{id}/enroll, proving the organization
// secret with an HMAC over the server's nonce. Returns 0 when the control room
// adopted it, the HTTP status when it refused, or negative for transport/parse
// errors.
int sebastian_enroll(const char *base_url, const char *device_id, const char *org_secret, const char *device_secret);
// PUT {base}/v1/devices/{id}/running-config with the stored config (RF-42).
// Returns 0 on 204, the HTTP status when refused, negative on transport errors.
int sebastian_report_running_config(const char *base_url, const char *device_id, const char *secret);
// GET with optional device credentials, capturing one response header. Returns
// the body length (>= 0), or negative: -1 init, -2 open, -3 http (status in
// *status), -4 read.
int sebastian_http_get_auth(const char *url, const char *device_id, const char *secret,
                            const char *capture_header, char *hdr_out, size_t hdr_size,
                            char *out, size_t out_size, int *status);
// Same, capturing two response headers.
int sebastian_http_get_auth2(const char *url, const char *device_id, const char *secret,
                             const char *header1, char *hdr1_out, size_t hdr1_size,
                             const char *header2, char *hdr2_out, size_t hdr2_size,
                             char *out, size_t out_size, int *status);

// ── announce.c ──────────────────────────────────────────────────────────────
// Advertise _sebastian._tcp on the LAN. Call once the network is up.
void sebastian_announce_start(const char *id, const char *prof, const char *cr);
// Update one TXT record (err, cfg, cr, prof).
void sebastian_announce_set(const char *key, const char *value);
// The last event worth telling the owner (RF-36/42): "adopt-denied:<ip>",
// "cfg-rejected:<why>" or the error recorded by the previous boot. Announced
// under the TXT key "ev" and reported in every poll; kept until the next one.
void sebastian_announce_event(const char *event);
const char *sebastian_announce_last_event(void);
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
// True from the moment an adoption is accepted until the unit restarts: the
// ring shows it in solid amber (RF-64).
bool sebastian_adopt_accepted(void);

// ── meeting orders (docs/implementation/14 §3.2) ─────────────────────────────
// One mailbox for the next order the app must act on: from the LAN command
// ({"t":"cmd"}), from the poll's X-Meeting header, or from the MUTE gesture.
// origin: 'n' network, 'g' gesture. cmd: "record-start" | "record-stop" | "record-warn".
void sebastian_meeting_push(const char *cmd, const char *id, char origin);
bool sebastian_meeting_take(char *cmd, size_t cmd_size, char *id, size_t id_size, char *origin);
// The app's facts the listener needs to answer busy/idle/profile.
void sebastian_meeting_set_active(bool active);
bool sebastian_meeting_is_active(void);
void sebastian_meeting_set_recordable(bool recordable); // agente profile (RM-54)
bool sebastian_meeting_is_recordable(void);
// hex HMAC-SHA256(secret, a + "." + b) — the proof shared by adoption and enrolment.
bool sebastian_hmac_sha256_hex(const char *secret, const char *a, const char *b, char out[65]);
