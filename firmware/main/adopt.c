// LAN adoption listener (block 3 of docs/implementation/11-fleet-adoption-control-room.md).
//
// One UDP port, JSON datagrams, one round trip plus reply:
//   → {"t":"hello"}                          ← {"t":"nonce","n":"<64 hex>"}
//   → {"t":"adopt","n":"<nonce>","cfg":"<sebastian.config.v1 as a JSON string>","mac":"<64 hex>"}
//   ← {"t":"wait","why":"consent"} | {"t":"queued"}   (progress, optional)
//   ← {"t":"ok"} | {"t":"err","why":"auth|nonce|json|wifi|consent-timeout|busy"}
// with mac = HMAC-SHA256(secret, nonce + "." + cfg). The device accepts the
// organization secret OR its own device secret; a factory unit (no organization
// secret) needs the MUTE button pressed while the ring blinks amber (RF-34). The
// stored config is applied through provisioning.c and the unit restarts — after
// the current conversation, if there is one (RF-39).
//
// Stack in internal RAM (NVS writes disable the cache, so a PSRAM stack would
// fault); the datagram buffers are in PSRAM.
#include "sebastian_fleet.h"

#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "cJSON.h"
#include "esp_heap_caps.h"
#include "esp_log.h"
#include "esp_random.h"
#include "esp_system.h"
#include "esp_timer.h"
#include "freertos/FreeRTOS.h"
#include "freertos/task.h"
#include "lwip/sockets.h"
#include "mbedtls/md.h"

static const char *TAG = "adopt";

#define RX_SIZE 4096
#define NONCE_BYTES 32
#define NONCE_TTL_US (30LL * 1000 * 1000)
#define CONSENT_WAIT_MS (30 * 1000)
#define SESSION_WAIT_MS (120 * 1000)
#define ACCEPTED_SHOW_MS 2000 // solid amber before the restart (RF-64)

static uint8_t nonce[NONCE_BYTES];
static int64_t nonce_issued_us; // 0 = none outstanding
static struct sockaddr_in nonce_peer;

static volatile bool session_active;
static volatile bool consent_pending;
static volatile bool consent_granted;
static volatile bool accepted;

void sebastian_adopt_session_active(bool active) { session_active = active; }
bool sebastian_adopt_session_is_active(void) { return session_active; }
bool sebastian_adopt_consent_pending(void) { return consent_pending; }
void sebastian_adopt_consent_grant(void) { if (consent_pending) consent_granted = true; }
bool sebastian_adopt_accepted(void) { return accepted; }

static void hex_encode(const uint8_t *in, size_t n, char *out) {
    static const char hex[] = "0123456789abcdef";
    for (size_t i = 0; i < n; i++) {
        out[i * 2] = hex[in[i] >> 4];
        out[i * 2 + 1] = hex[in[i] & 0xF];
    }
    out[n * 2] = '\0';
}

static int hex_nibble(char c) {
    if (c >= '0' && c <= '9') return c - '0';
    if (c >= 'a' && c <= 'f') return c - 'a' + 10;
    if (c >= 'A' && c <= 'F') return c - 'A' + 10;
    return -1;
}

static bool hex_decode(const char *in, uint8_t *out, size_t n) {
    if (strlen(in) != n * 2) return false;
    for (size_t i = 0; i < n; i++) {
        int hi = hex_nibble(in[i * 2]), lo = hex_nibble(in[i * 2 + 1]);
        if (hi < 0 || lo < 0) return false;
        out[i] = (uint8_t)((hi << 4) | lo);
    }
    return true;
}

static void send_json(int sock, const struct sockaddr_in *to, const char *json) {
    sendto(sock, json, strlen(json), 0, (const struct sockaddr *)to, sizeof(*to));
}

static void send_err(int sock, const struct sockaddr_in *to, const char *why) {
    char msg[64];
    snprintf(msg, sizeof(msg), "{\"t\":\"err\",\"why\":\"%s\"}", why);
    send_json(sock, to, msg);
}

// HMAC-SHA256(secret, a + "." + b): the proof used by LAN adoption (adopt) and
// by the HTTP enrolment (session_http.c); the secret itself never travels.
static bool hmac_sha256(const char *secret, const char *a, const char *b, uint8_t out[32]) {
    if (!secret || !secret[0]) return false;
    const mbedtls_md_info_t *md = mbedtls_md_info_from_type(MBEDTLS_MD_SHA256);
    mbedtls_md_context_t ctx;
    mbedtls_md_init(&ctx);
    bool ok = mbedtls_md_setup(&ctx, md, 1) == 0 &&
              mbedtls_md_hmac_starts(&ctx, (const uint8_t *)secret, strlen(secret)) == 0 &&
              mbedtls_md_hmac_update(&ctx, (const uint8_t *)a, strlen(a)) == 0 &&
              mbedtls_md_hmac_update(&ctx, (const uint8_t *)".", 1) == 0 &&
              mbedtls_md_hmac_update(&ctx, (const uint8_t *)b, strlen(b)) == 0 &&
              mbedtls_md_hmac_finish(&ctx, out) == 0;
    mbedtls_md_free(&ctx);
    return ok;
}

bool sebastian_hmac_sha256_hex(const char *secret, const char *a, const char *b, char out[65]) {
    uint8_t raw[32];
    if (!hmac_sha256(secret, a, b, raw)) return false;
    hex_encode(raw, sizeof(raw), out);
    return true;
}

static bool hmac_matches(const char *secret, const char *nonce_hex, const char *cfg, const uint8_t expected[32]) {
    uint8_t out[32];
    if (!hmac_sha256(secret, nonce_hex, cfg, out)) return false;
    int diff = 0;
    for (int i = 0; i < 32; i++) diff |= out[i] ^ expected[i];
    return diff == 0;
}

// Who may adopt: the organization secret, the device secret, or — for a unit
// nobody owns (no organization secret and not bound to a control room; the
// device secret is born with the unit, so it never counts as ownership) —
// whoever presses MUTE within 30 s (RF-32..34).
typedef enum { AUTH_OK, AUTH_DENIED, AUTH_CONSENT } auth_t;

static auth_t authorize(const char *nonce_hex, const char *cfg, const uint8_t mac[32]) {
    char org[129] = {0}, dev[129] = {0};
    const bool has_org = sebastian_get_org_secret(org, sizeof(org));
    const bool has_dev = sebastian_get_device_secret(dev, sizeof(dev));
    if (has_org && hmac_matches(org, nonce_hex, cfg, mac)) return AUTH_OK;
    if (has_dev && hmac_matches(dev, nonce_hex, cfg, mac)) return AUTH_OK;
    if (!has_org && !sebastian_is_bound()) return AUTH_CONSENT;
    return AUTH_DENIED;
}

static bool wait_consent(int sock, const struct sockaddr_in *peer) {
    consent_granted = false;
    consent_pending = true;
    send_json(sock, peer, "{\"t\":\"wait\",\"why\":\"consent\"}");
    ESP_LOGW(TAG, "factory unit: adoption needs the MUTE button within %d s", CONSENT_WAIT_MS / 1000);
    for (int waited = 0; waited < CONSENT_WAIT_MS && !consent_granted; waited += 100) vTaskDelay(pdMS_TO_TICKS(100));
    consent_pending = false;
    return consent_granted;
}

// A conversation in progress is never cut by a config change: wait for it to
// end, up to SESSION_WAIT_MS, telling the control room the adoption is queued.
static bool wait_session_end(int sock, const struct sockaddr_in *peer) {
    if (!session_active) return true;
    send_json(sock, peer, "{\"t\":\"queued\"}");
    ESP_LOGI(TAG, "conversation in progress — adoption queued (max %d s)", SESSION_WAIT_MS / 1000);
    for (int waited = 0; waited < SESSION_WAIT_MS && session_active; waited += 250) vTaskDelay(pdMS_TO_TICKS(250));
    return true; // past the cap the change applies anyway (RF-65)
}

static void handle_hello(int sock, const struct sockaddr_in *peer) {
    esp_fill_random(nonce, sizeof(nonce));
    nonce_issued_us = esp_timer_get_time();
    nonce_peer = *peer;
    char hex[NONCE_BYTES * 2 + 1];
    hex_encode(nonce, sizeof(nonce), hex);
    char msg[96];
    snprintf(msg, sizeof(msg), "{\"t\":\"nonce\",\"n\":\"%s\"}", hex);
    send_json(sock, peer, msg);
}

static void handle_adopt(int sock, const struct sockaddr_in *peer, const cJSON *root) {
    const cJSON *n = cJSON_GetObjectItem(root, "n");
    const cJSON *cfg = cJSON_GetObjectItem(root, "cfg");
    const cJSON *mac = cJSON_GetObjectItem(root, "mac");
    if (!cJSON_IsString(n) || !cJSON_IsString(cfg) || !cJSON_IsString(mac)) { send_err(sock, peer, "json"); return; }

    // Single-use nonce, 30 s, same peer.
    uint8_t got[NONCE_BYTES];
    const bool fresh = nonce_issued_us != 0 && esp_timer_get_time() - nonce_issued_us < NONCE_TTL_US;
    const bool same_peer = nonce_peer.sin_addr.s_addr == peer->sin_addr.s_addr;
    if (!fresh || !same_peer || !hex_decode(n->valuestring, got, sizeof(got)) || memcmp(got, nonce, sizeof(nonce)) != 0) {
        send_err(sock, peer, "nonce");
        return;
    }
    nonce_issued_us = 0;

    uint8_t expected[32];
    if (!hex_decode(mac->valuestring, expected, sizeof(expected))) { send_err(sock, peer, "json"); return; }

    char ip[16];
    inet_ntoa_r(peer->sin_addr, ip, sizeof(ip));
    switch (authorize(n->valuestring, cfg->valuestring, expected)) {
    case AUTH_DENIED:
        char denied[40];
        snprintf(denied, sizeof(denied), "adopt-denied:%s", ip);
        sebastian_announce_event(denied);
        ESP_LOGW(TAG, "adoption from %s denied: secret does not match", ip);
        send_err(sock, peer, "auth");
        return;
    case AUTH_CONSENT:
        if (!wait_consent(sock, peer)) {
            ESP_LOGW(TAG, "adoption from %s expired: no MUTE press", ip);
            send_err(sock, peer, "consent-timeout");
            return;
        }
        break;
    case AUTH_OK:
        break;
    }

    wait_session_end(sock, peer);
    char why[32];
    if (!sebastian_provisioning_apply(cfg->valuestring, why, sizeof(why))) {
        ESP_LOGE(TAG, "adoption from %s rejected by the store: %s", ip, why);
        send_err(sock, peer, why);
        return;
    }
    // The unit's own secret goes to the adopter (RF-51): it is what the control
    // room will authenticate our sessions with.
    sebastian_mark_bound();
    char dev[129] = {0};
    if (!sebastian_ensure_device_secret() || !sebastian_get_device_secret(dev, sizeof(dev))) {
        ESP_LOGE(TAG, "adopted from %s but no device secret to hand over", ip);
        send_err(sock, peer, "secret");
        return;
    }
    char ok[192];
    snprintf(ok, sizeof(ok), "{\"t\":\"ok\",\"dev\":\"%s\"}", dev);
    ESP_LOGI(TAG, "adopted from %s — restarting into the new control room", ip);
    send_json(sock, peer, ok);
    accepted = true; // the ring turns solid amber (RF-64)
    vTaskDelay(pdMS_TO_TICKS(ACCEPTED_SHOW_MS)); // let the reply, the log and the ring say it
    esp_restart();
}

static void adopt_task(void *arg) {
    (void)arg;
    char *rx = heap_caps_malloc(RX_SIZE, MALLOC_CAP_SPIRAM);
    if (!rx) { ESP_LOGE(TAG, "no PSRAM for the receive buffer"); vTaskDelete(NULL); return; }

    int sock = socket(AF_INET, SOCK_DGRAM, IPPROTO_UDP);
    struct sockaddr_in addr = {.sin_family = AF_INET, .sin_port = htons(SEBASTIAN_ADOPT_PORT), .sin_addr.s_addr = htonl(INADDR_ANY)};
    if (sock < 0 || bind(sock, (struct sockaddr *)&addr, sizeof(addr)) < 0) {
        ESP_LOGE(TAG, "cannot listen on udp/%d", SEBASTIAN_ADOPT_PORT);
        vTaskDelete(NULL);
        return;
    }
    ESP_LOGI(TAG, "adoption listener on udp/%d", SEBASTIAN_ADOPT_PORT);
    for (;;) {
        struct sockaddr_in peer;
        socklen_t plen = sizeof(peer);
        int n = recvfrom(sock, rx, RX_SIZE - 1, 0, (struct sockaddr *)&peer, &plen);
        if (n <= 0) continue;
        rx[n] = '\0';
        cJSON *root = cJSON_Parse(rx);
        const cJSON *t = root ? cJSON_GetObjectItem(root, "t") : NULL;
        if (!cJSON_IsString(t)) { cJSON_Delete(root); continue; }
        if (strcmp(t->valuestring, "hello") == 0) handle_hello(sock, &peer);
        else if (strcmp(t->valuestring, "adopt") == 0) handle_adopt(sock, &peer, root);
        cJSON_Delete(root);
    }
}

void sebastian_adopt_start(void) {
    static bool started;
    if (started) return;
    started = true;
    xTaskCreatePinnedToCore(adopt_task, "adopt", 5120, NULL, 4, NULL, 0);
}
