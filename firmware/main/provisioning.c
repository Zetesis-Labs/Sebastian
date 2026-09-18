// Serial provisioning + NVS-backed WiFi for the web installer (docs/installer).
//
// The web installer flashes a factory image and then sends one line over Web
// Serial:  "sebastian.config.v1 {json}\n". This receiver stores the WiFi creds
// in NVS and restarts; on the next boot sebastian_net_connect() uses them. If
// NVS has no creds it falls back to the compiled CONFIG_LK_EXAMPLE_WIFI_* — so a
// unit built the old way (secrets baked in) keeps working with zero change, and
// a factory image (those blanked) waits to be provisioned. See PROVISIONING.md.
//
// The esp_wifi/nvs/cJSON/usb_serial_jtag APIs are far easier in C than through
// hand-written Zig bindings, so this lives here like session_http.c.

#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "esp_event.h"
#include "esp_log.h"
#include "esp_netif.h"
#include "esp_random.h"
#include "esp_system.h"
#include "esp_timer.h"
#include "esp_wifi.h"
#include "nvs.h"
#include "nvs_flash.h"
#include "cJSON.h"
#include "driver/usb_serial_jtag.h"
#include "freertos/FreeRTOS.h"
#include "freertos/event_groups.h"
#include "freertos/task.h"

#include "profiles.h"
#include "sebastian_fleet.h"

static const char *TAG = "provisioning";

#define NVS_NS "sebastian"
#define PROV_PREFIX "sebastian.config.v1 "
#define PROV_SCHEMA "sebastian.config.v1"
#define PROFILE_PREFIX "sebastian.profile.set "
#define GET_CMD "sebastian.config.get"
#define DUMP_PREFIX "sebastian.config.dump "
// A config.get keeps the USB-serial window open this long so the installer can
// show the stored config, let the operator edit it and send it back on the same
// port (see app.zig: the window normally closes 5 s after boot).
#define HOLD_AFTER_GET_US (120LL * 1000 * 1000)

#define NET_CONNECTED (1 << 0)
#define NET_FAILED (1 << 1)
#define MAX_RETRIES 20

static EventGroupHandle_t net_events;
static int retry_attempt;
static bool usj_ready; // usb_serial_jtag driver installed (also used for replies)
static int64_t hold_until_us;

bool sebastian_provisioning_hold(void) {
    return esp_timer_get_time() < hold_until_us;
}

// ── Replies go out through stdout, i.e. the console — whose secondary output is
//    the USB-Serial-JTAG the installer listens on. Writing through the driver
//    (usb_serial_jtag_write_bytes) never reached the host on this board while the
//    console's polling writer owns the same FIFO; stdout is what demonstrably
//    arrives (2026-09-18). The log echo is truncated: the dump is ~300 bytes.
static void reply(const char *line) {
    printf("%s\n", line);
    fflush(stdout);
    ESP_LOGI(TAG, "reply: %.*s", 32, line);
}

// ── NVS ──────────────────────────────────────────────────────────────────────
static void nvs_ensure_init(void) {
    esp_err_t err = nvs_flash_init();
    if (err == ESP_ERR_NVS_NO_FREE_PAGES || err == ESP_ERR_NVS_NEW_VERSION_FOUND) {
        ESP_ERROR_CHECK(nvs_flash_erase());
        ESP_ERROR_CHECK(nvs_flash_init());
    } else {
        ESP_ERROR_CHECK(err);
    }
}

// Quiet on the empty-namespace case (NOT_FOUND) — that is the normal
// unprovisioned path, handled by the sdkconfig fallback in the caller.
static bool nvs_read_str(const char *key, char *out, size_t out_size) {
    nvs_handle_t h;
    if (nvs_open(NVS_NS, NVS_READONLY, &h) != ESP_OK) return false;
    size_t len = out_size;
    esp_err_t err = nvs_get_str(h, key, out, &len);
    nvs_close(h);
    return err == ESP_OK && len > 1; // len includes the NUL
}

// ── WiFi creds: NVS only. No compiled fallback — the factory binary must carry
//    NO credentials; an unprovisioned unit waits for the serial config. ────────
static bool load_wifi_creds(char *ssid, size_t ssid_sz, char *pass, size_t pass_sz) {
    if (!nvs_read_str("wifi_ssid", ssid, ssid_sz)) {
        ESP_LOGW(TAG, "unprovisioned: no WiFi in NVS — send %s{json} over serial", PROV_PREFIX);
        return false;
    }
    if (!nvs_read_str("wifi_pass", pass, pass_sz)) pass[0] = '\0';
    ESP_LOGI(TAG, "wifi creds from NVS: ssid=%s", ssid);
    return ssid[0] != '\0';
}

// Token-server URL, provisioned (token.zig reads it here). NVS only, no default.
bool sebastian_get_token_url(char *out, size_t out_size) {
    return nvs_read_str("token_url", out, out_size);
}

// ── Runtime config, provisioned (config.zig::load reads these at boot). Missing
//    key → the supplied default, so an unprovisioned unit keeps config.zig's. ──
bool sebastian_cfg_get_bool(const char *key, bool def) {
    nvs_ensure_init(); // may run before net_connect; nvs_flash_init is idempotent
    nvs_handle_t h;
    if (nvs_open(NVS_NS, NVS_READONLY, &h) != ESP_OK) return def;
    uint8_t v;
    esp_err_t err = nvs_get_u8(h, key, &v);
    nvs_close(h);
    return err == ESP_OK ? (v != 0) : def;
}

int32_t sebastian_cfg_get_i32(const char *key, int32_t def) {
    nvs_ensure_init();
    nvs_handle_t h;
    if (nvs_open(NVS_NS, NVS_READONLY, &h) != ESP_OK) return def;
    int32_t v;
    esp_err_t err = nvs_get_i32(h, key, &v);
    nvs_close(h);
    return err == ESP_OK ? v : def;
}

// ── Station bring-up (mirrors livekit_example_net.c, creds from load above) ──
static void ip_event_handler(void *arg, esp_event_base_t base, int32_t id, void *data) {
    (void)arg; (void)base; (void)id;
    ip_event_got_ip_t *event = (ip_event_got_ip_t *)data;
    ESP_LOGI(TAG, "Connected: ip=" IPSTR ", gw=" IPSTR,
             IP2STR(&event->ip_info.ip), IP2STR(&event->ip_info.gw));
    retry_attempt = 0;
    xEventGroupSetBits(net_events, NET_CONNECTED);
}

static void wifi_event_handler(void *arg, esp_event_base_t base, int32_t id, void *data) {
    (void)arg; (void)base; (void)data;
    switch (id) {
    case WIFI_EVENT_STA_START:
        esp_wifi_connect();
        break;
    case WIFI_EVENT_STA_DISCONNECTED:
        if (retry_attempt < MAX_RETRIES) {
            ESP_LOGI(TAG, "Retry: attempt=%d", retry_attempt + 1);
            esp_wifi_connect();
            retry_attempt++;
            return;
        }
        ESP_LOGE(TAG, "Unable to establish connection");
        xEventGroupSetBits(net_events, NET_FAILED);
        break;
    default:
        break;
    }
}

// A WiFi change pushed over the network (adoption / desired config) is a
// trial: the previous credentials stay in NVS until the new ones obtain an IP.
// No IP within WIFI_TRIAL_MS ⇒ restore the previous network and restart, and
// the failure is reported on the next announce (RF-45).
#define WIFI_TRIAL_MS (120 * 1000)

static bool wifi_trial_pending(void) {
    return sebastian_cfg_get_bool("wifi_trial", false);
}

static void wifi_trial_settle(bool success) {
    nvs_handle_t h;
    if (nvs_open(NVS_NS, NVS_READWRITE, &h) != ESP_OK) return;
    if (!success) {
        char ssid[33] = {0}, pass[65] = {0};
        size_t sl = sizeof(ssid), pl = sizeof(pass);
        if (nvs_get_str(h, "wifi_prev_ssid", ssid, &sl) == ESP_OK && ssid[0]) {
            nvs_set_str(h, "wifi_ssid", ssid);
            if (nvs_get_str(h, "wifi_prev_pass", pass, &pl) == ESP_OK) nvs_set_str(h, "wifi_pass", pass);
            else nvs_erase_key(h, "wifi_pass");
            ESP_LOGW(TAG, "wifi trial failed — restored previous network %s", ssid);
        }
        nvs_set_str(h, "last_err", "wifi-rollback");
    }
    nvs_erase_key(h, "wifi_prev_ssid");
    nvs_erase_key(h, "wifi_prev_pass");
    nvs_erase_key(h, "wifi_trial");
    nvs_commit(h);
    nvs_close(h);
}

// Returns true once an IP is obtained. Blocks like lk_example_network_connect.
bool sebastian_net_connect(void) {
    nvs_ensure_init(); // MUST precede load_wifi_creds — it reads provisioned NVS

    char ssid[33] = {0};
    char pass[65] = {0};
    if (!load_wifi_creds(ssid, sizeof(ssid), pass, sizeof(pass))) {
        ESP_LOGE(TAG, "no WiFi ssid (unprovisioned) — send sebastian.config.v1 over serial");
        return false;
    }
    const bool trial = wifi_trial_pending();
    if (trial) ESP_LOGW(TAG, "wifi trial: %s must obtain an IP within %d s or the previous network is restored", ssid, WIFI_TRIAL_MS / 1000);

    if (!net_events) net_events = xEventGroupCreate();
    ESP_ERROR_CHECK(esp_netif_init());
    ESP_ERROR_CHECK(esp_event_loop_create_default());
    esp_netif_create_default_wifi_sta();

    wifi_init_config_t init_config = WIFI_INIT_CONFIG_DEFAULT();
    ESP_ERROR_CHECK(esp_wifi_init(&init_config));
    ESP_ERROR_CHECK(esp_event_handler_register(WIFI_EVENT, ESP_EVENT_ANY_ID, &wifi_event_handler, NULL));
    ESP_ERROR_CHECK(esp_event_handler_register(IP_EVENT, IP_EVENT_STA_GOT_IP, &ip_event_handler, NULL));

    wifi_config_t wifi_config = {0};
    strlcpy((char *)wifi_config.sta.ssid, ssid, sizeof(wifi_config.sta.ssid));
    strlcpy((char *)wifi_config.sta.password, pass, sizeof(wifi_config.sta.password));
    wifi_config.sta.threshold.authmode = pass[0] ? WIFI_AUTH_WPA2_PSK : WIFI_AUTH_OPEN;

    ESP_ERROR_CHECK(esp_wifi_set_mode(WIFI_MODE_STA));
    ESP_ERROR_CHECK(esp_wifi_set_ps(WIFI_PS_NONE));
    ESP_ERROR_CHECK(esp_wifi_set_config(WIFI_IF_STA, &wifi_config));
    ESP_ERROR_CHECK(esp_wifi_start());

    EventBits_t bits;
    do {
        bits = xEventGroupWaitBits(net_events, NET_CONNECTED | NET_FAILED, pdFALSE, pdFALSE,
                                   trial ? pdMS_TO_TICKS(WIFI_TRIAL_MS) : portMAX_DELAY);
        if (trial && !(bits & (NET_CONNECTED | NET_FAILED))) bits |= NET_FAILED; // trial timeout
    } while (!(bits & (NET_CONNECTED | NET_FAILED)));
    const bool connected = (bits & NET_CONNECTED) != 0;
    if (trial) {
        wifi_trial_settle(connected);
        if (!connected) {
            vTaskDelay(pdMS_TO_TICKS(300));
            esp_restart();
        }
    }
    return connected;
}

// ── Provisioning receiver ────────────────────────────────────────────────────
static void store_cfg_bool(nvs_handle_t h, const char *key, const cJSON *item) {
    if (cJSON_IsBool(item)) nvs_set_u8(h, key, cJSON_IsTrue(item) ? 1 : 0);
}

// A string field that is present and empty ERASES the key (that is how "forget"
// clears the control room and the secrets); absent keeps the stored value.
static void store_str_or_erase(nvs_handle_t h, const char *key, const cJSON *item) {
    if (!cJSON_IsString(item)) return;
    if (item->valuestring[0] == '\0') nvs_erase_key(h, key);
    else nvs_set_str(h, key, item->valuestring);
}

// Set by the serial receiver around its own store: a USB provisioning is done
// with the unit in hand, so a WiFi change there is final (no trial/rollback).
static bool usj_ready_for_serial_provisioning;

// The WiFi block is optional: a config pushed over the network (adoption,
// desired config) normally leaves it out and keeps the stored network. When it
// changes the network, the previous credentials are kept for the trial rollback.
static bool store_wifi_block(nvs_handle_t h, const cJSON *wifi) {
    if (!cJSON_IsObject(wifi)) return true;
    const cJSON *ssid = cJSON_GetObjectItem(wifi, "ssid");
    const cJSON *pass = cJSON_GetObjectItem(wifi, "password");
    if (!cJSON_IsString(ssid) || ssid->valuestring[0] == '\0') return false;

    char cur_ssid[33] = {0}, cur_pass[65] = {0};
    size_t sl = sizeof(cur_ssid), pl = sizeof(cur_pass);
    const bool had = nvs_get_str(h, "wifi_ssid", cur_ssid, &sl) == ESP_OK && cur_ssid[0];
    if (nvs_get_str(h, "wifi_pass", cur_pass, &pl) != ESP_OK) cur_pass[0] = '\0';
    const bool changes = !had || strcmp(cur_ssid, ssid->valuestring) != 0 ||
                         (cJSON_IsString(pass) && strcmp(cur_pass, pass->valuestring) != 0);
    if (had && changes && !usj_ready_for_serial_provisioning) {
        nvs_set_str(h, "wifi_prev_ssid", cur_ssid);
        nvs_set_str(h, "wifi_prev_pass", cur_pass);
        nvs_set_u8(h, "wifi_trial", 1);
    }
    if (nvs_set_str(h, "wifi_ssid", ssid->valuestring) != ESP_OK) return false;
    if (cJSON_IsString(pass) && nvs_set_str(h, "wifi_pass", pass->valuestring) != ESP_OK) return false;
    return true;
}

static bool store_config(cJSON *root) {
    nvs_ensure_init(); // the receiver can run before net_connect inits NVS
    nvs_handle_t h;
    esp_err_t oe = nvs_open(NVS_NS, NVS_READWRITE, &h);
    if (oe != ESP_OK) { ESP_LOGE(TAG, "store: open=%s", esp_err_to_name(oe)); return false; }
    esp_err_t se = ESP_OK;
    bool ok = store_wifi_block(h, cJSON_GetObjectItem(root, "wifi"));
    // token server URL = the control room this unit is bound to. Empty = unbound
    // (forget). token.zig / control.zig read it.
    cJSON *lk = cJSON_GetObjectItem(root, "livekit");
    if (ok && lk) store_str_or_erase(h, "token_url", cJSON_GetObjectItem(lk, "tokenServerUrl"));
    // Adoption secrets (docs/implementation/11-fleet-adoption-control-room.md §5):
    // the organization secret authorizes adoption, the device secret opens
    // sessions. Empty strings clear them (forget / factory).
    cJSON *ad = cJSON_GetObjectItem(root, "adoption");
    if (ok && cJSON_IsObject(ad)) {
        store_str_or_erase(h, "org_secret", cJSON_GetObjectItem(ad, "orgSecret"));
        store_str_or_erase(h, "dev_secret", cJSON_GetObjectItem(ad, "deviceSecret"));
    }
    // Session timing (session_core / session_reducer read these through config.zig).
    cJSON *sess = cJSON_GetObjectItem(root, "session");
    if (ok && cJSON_IsObject(sess)) {
        cJSON *sil = cJSON_GetObjectItem(sess, "silenceTimeoutMs");
        if (cJSON_IsNumber(sil) && sil->valueint >= 5000) nvs_set_i32(h, "silence_ms", (int32_t)sil->valueint);
        cJSON *vl = cJSON_GetObjectItem(sess, "voiceLevel");
        if (cJSON_IsNumber(vl) && vl->valueint > 0) nvs_set_i32(h, "voice_lvl", (int32_t)vl->valueint);
    }
    // The control room's desired-config version this payload carries; echoed in
    // every reconciliation poll so the dashboard can show running vs desired.
    if (ok) store_str_or_erase(h, "cfg_ver", cJSON_GetObjectItem(root, "configVersion"));
    // Optional telemetry: UDP syslog receiver for the device's logs (the firmware
    // ships ESP_LOG here since nothing reads its serial in prod). syslog_sink.c
    // reads these at boot; absent ⇒ the sink stays off.
    cJSON *tel = cJSON_GetObjectItem(root, "telemetry");
    if (ok && cJSON_IsObject(tel)) {
        cJSON *sip = cJSON_GetObjectItem(tel, "syslogIp");
        if (cJSON_IsString(sip)) nvs_set_str(h, "syslog_ip", sip->valuestring);
        cJSON *sport = cJSON_GetObjectItem(tel, "syslogPort");
        if (cJSON_IsNumber(sport)) nvs_set_i32(h, "syslog_port", (int32_t)sport->valueint);
    }
    // Operating mode + audio behaviour (config.zig::load reads these at boot). All
    // optional: absent keys keep config.zig's compiled defaults. mic_channel and
    // session timing stay compile-time, so they are intentionally not stored here.
    cJSON *mode = cJSON_GetObjectItem(root, "mode");
    if (ok && cJSON_IsString(mode)) nvs_set_str(h, "mode", mode->valuestring);
    cJSON *audio = cJSON_GetObjectItem(root, "audio");
    if (ok && cJSON_IsObject(audio)) {
        store_cfg_bool(h, "full_duplex", cJSON_GetObjectItem(audio, "fullDuplex"));
        store_cfg_bool(h, "fixed_beam", cJSON_GetObjectItem(audio, "fixedBeam"));
        cJSON *az = cJSON_GetObjectItem(audio, "fixedBeamAzimuthDeg");
        if (cJSON_IsNumber(az)) {
            double d = az->valuedouble;
            nvs_set_i32(h, "beam_az", (int32_t)(d < 0 ? d - 0.5 : d + 0.5));
        }
    }
    // Profiles: named capability bundles (profiles.c reads these at boot). The
    // array is stored as its raw JSON; activeProfile selects one by name.
    cJSON *profs = cJSON_GetObjectItem(root, "profiles");
    if (ok && cJSON_IsArray(profs)) {
        char *s = cJSON_PrintUnformatted(profs);
        if (s) {
            nvs_set_str(h, "profiles", s);
            cJSON_free(s);
        }
    }
    cJSON *ap = cJSON_GetObjectItem(root, "activeProfile");
    if (ok && cJSON_IsString(ap)) nvs_set_str(h, "active_prof", ap->valuestring);
    esp_err_t ce = nvs_commit(h);
    if (ok) ok = ce == ESP_OK;
    nvs_close(h);
    ESP_LOGI(TAG, "store: set=%s commit=%s -> %s", esp_err_to_name(se), esp_err_to_name(ce), ok ? "ok" : "FAIL");
    return ok;
}

// Parse + schema-check + store a sebastian.config.v1 document. Shared by the
// serial receiver, the LAN adoption listener (adopt.c) and the desired-config
// poll (control.zig). Never restarts: the caller decides when.
bool sebastian_provisioning_apply(const char *json, char *err, size_t err_size) {
    cJSON *root = cJSON_Parse(json);
    if (!root) { snprintf(err, err_size, "json_parse"); return false; }
    cJSON *schema = cJSON_GetObjectItem(root, "schema");
    if (!cJSON_IsString(schema) || strcmp(schema->valuestring, PROV_SCHEMA) != 0) {
        cJSON_Delete(root);
        snprintf(err, err_size, "schema");
        return false;
    }
    bool ok = store_config(root);
    cJSON_Delete(root);
    if (!ok) snprintf(err, err_size, "wifi");
    return ok;
}

bool sebastian_get_device_secret(char *out, size_t out_size) { return nvs_read_str("dev_secret", out, out_size); }

static bool nvs_write_str(const char *key, const char *value) {
    nvs_handle_t h;
    if (nvs_open(NVS_NS, NVS_READWRITE, &h) != ESP_OK) return false;
    esp_err_t e = value[0] ? nvs_set_str(h, key, value) : nvs_erase_key(h, key);
    if (e == ESP_ERR_NVS_NOT_FOUND) e = ESP_OK;
    if (e == ESP_OK) e = nvs_commit(h);
    nvs_close(h);
    return e == ESP_OK;
}

// The device secret is born with the unit (RF-51): generated once, at the
// first boot that needs it, and kept across adoptions. Only "forget" (factory)
// erases it, and a regeneration from the ficha replaces it.
bool sebastian_ensure_device_secret(void) {
    nvs_ensure_init();
    char cur[129];
    if (nvs_read_str("dev_secret", cur, sizeof(cur))) return true;
    uint8_t raw[32];
    esp_fill_random(raw, sizeof(raw));
    char hex[65];
    static const char digits[] = "0123456789abcdef";
    for (int i = 0; i < 32; i++) { hex[i * 2] = digits[raw[i] >> 4]; hex[i * 2 + 1] = digits[raw[i] & 0xF]; }
    hex[64] = '\0';
    if (!nvs_write_str("dev_secret", hex)) return false;
    ESP_LOGI(TAG, "device secret generated");
    return true;
}

// Which control room already holds our secret: the origin of the token URL
// at the time of the last adoption/enrolment. A different token URL (USB
// re-provisioning) or a 401 from the control room clears it, so the unit
// enrols again (control.zig).
static void token_origin(char *out, size_t out_size) {
    char url[256] = {0};
    out[0] = '\0';
    if (!nvs_read_str("token_url", url, sizeof(url))) return;
    const char *p = strstr(url, "://");
    p = p ? strchr(p + 3, '/') : strchr(url, '/');
    size_t n = p ? (size_t)(p - url) : strlen(url);
    if (n >= out_size) n = out_size - 1;
    memcpy(out, url, n);
    out[n] = '\0';
}

void sebastian_mark_bound(void) {
    char origin[256];
    token_origin(origin, sizeof(origin));
    nvs_write_str("cr_bound", origin);
}

void sebastian_clear_bound(void) { nvs_write_str("cr_bound", ""); }

bool sebastian_is_bound(void) {
    char origin[256], bound[256];
    token_origin(origin, sizeof(origin));
    if (!origin[0] || !nvs_read_str("cr_bound", bound, sizeof(bound))) return false;
    return strcmp(origin, bound) == 0;
}
bool sebastian_get_org_secret(char *out, size_t out_size) { return nvs_read_str("org_secret", out, out_size); }
bool sebastian_get_cfg_version(char *out, size_t out_size) { return nvs_read_str("cfg_ver", out, out_size); }

// One-shot: the error recorded by a previous boot (wifi-rollback…) is announced
// once and cleared.
bool sebastian_take_last_error(char *out, size_t out_size) {
    if (!nvs_read_str("last_err", out, out_size)) return false;
    nvs_handle_t h;
    if (nvs_open(NVS_NS, NVS_READWRITE, &h) == ESP_OK) {
        nvs_erase_key(h, "last_err");
        nvs_commit(h);
        nvs_close(h);
    }
    return true;
}

// Lightweight profile switch — no WiFi payload required, so an already
// provisioned unit can change personality with one serial line:
//   sebastian.profile.set {"name":"micro-usb"}
static void handle_profile_set(const char *json) {
    cJSON *root = cJSON_Parse(json);
    const cJSON *name = root ? cJSON_GetObjectItem(root, "name") : NULL;
    bool ok = cJSON_IsString(name) && sebastian_profiles_set_active(name->valuestring);
    cJSON_Delete(root);
    if (!ok) {
        reply("sebastian.profile.err");
        return;
    }
    reply("sebastian.profile.ok");
    vTaskDelay(pdMS_TO_TICKS(300)); // let the reply drain before the reset
    esp_restart();
}

// Read back what is stored in NVS, in the sebastian.config.v1 shape the installer
// edits, so an already provisioned unit can be re-provisioned without retyping
// everything. Only keys present in NVS are emitted (the installer merges them
// over its defaults). The WiFi password never leaves the device: `passwordSet`
// tells the installer there is one, and a config without `wifi.password` keeps it.
static void dump_str(nvs_handle_t h, cJSON *obj, const char *json_key, const char *nvs_key) {
    char buf[256];
    size_t len = sizeof(buf);
    if (nvs_get_str(h, nvs_key, buf, &len) == ESP_OK) cJSON_AddStringToObject(obj, json_key, buf);
}

static void dump_bool(nvs_handle_t h, cJSON *obj, const char *json_key, const char *nvs_key) {
    uint8_t v;
    if (nvs_get_u8(h, nvs_key, &v) == ESP_OK) cJSON_AddBoolToObject(obj, json_key, v != 0);
}

static void dump_i32(nvs_handle_t h, cJSON *obj, const char *json_key, const char *nvs_key) {
    int32_t v;
    if (nvs_get_i32(h, nvs_key, &v) == ESP_OK) cJSON_AddNumberToObject(obj, json_key, v);
}

// The stored config as a sebastian.config.v1 document. in_hand: the reader has
// the unit on a USB cable, so its own secret may go out; over the network
// (RF-42 report) no secret ever does.
static cJSON *config_dump(bool in_hand) {
    nvs_ensure_init();
    nvs_handle_t h;
    esp_err_t oe = nvs_open(NVS_NS, NVS_READONLY, &h);
    if (oe != ESP_OK && oe != ESP_ERR_NVS_NOT_FOUND) {
        ESP_LOGE(TAG, "dump: open=%s", esp_err_to_name(oe));
        return NULL;
    }
    cJSON *root = cJSON_CreateObject();
    cJSON_AddStringToObject(root, "schema", PROV_SCHEMA);
    cJSON_AddBoolToObject(root, "provisioned", false);
    if (oe == ESP_OK) {
        cJSON *wifi = cJSON_AddObjectToObject(root, "wifi");
        dump_str(h, wifi, "ssid", "wifi_ssid");
        char pass[65];
        size_t plen = sizeof(pass);
        bool has_pass = nvs_get_str(h, "wifi_pass", pass, &plen) == ESP_OK && plen > 1;
        cJSON_AddBoolToObject(wifi, "passwordSet", has_pass);
        cJSON_ReplaceItemInObject(root, "provisioned", cJSON_CreateBool(cJSON_HasObjectItem(wifi, "ssid")));

        cJSON *lk = cJSON_AddObjectToObject(root, "livekit");
        dump_str(h, lk, "tokenServerUrl", "token_url");

        cJSON *tel = cJSON_AddObjectToObject(root, "telemetry");
        dump_str(h, tel, "syslogIp", "syslog_ip");
        dump_i32(h, tel, "syslogPort", "syslog_port");

        dump_str(h, root, "mode", "mode");
        cJSON *audio = cJSON_AddObjectToObject(root, "audio");
        dump_bool(h, audio, "fullDuplex", "full_duplex");
        dump_bool(h, audio, "fixedBeam", "fixed_beam");
        dump_i32(h, audio, "fixedBeamAzimuthDeg", "beam_az");

        // Heap, not stack: the RF-42 report runs this from the 4 KB poll task.
        size_t len = 0;
        if (nvs_get_str(h, "profiles", NULL, &len) == ESP_OK && len > 0) {
            char *profs = malloc(len);
            if (profs && nvs_get_str(h, "profiles", profs, &len) == ESP_OK) {
                cJSON *arr = cJSON_Parse(profs);
                if (arr) cJSON_AddItemToObject(root, "profiles", arr);
            }
            free(profs);
        }
        dump_str(h, root, "activeProfile", "active_prof");

        cJSON *ad = cJSON_AddObjectToObject(root, "adoption");
        char sec[129];
        size_t slen = sizeof(sec);
        cJSON_AddBoolToObject(ad, "orgSecretSet", nvs_get_str(h, "org_secret", sec, &slen) == ESP_OK && slen > 1);
        slen = sizeof(sec);
        // The unit is in hand (USB): its own secret is readable here, so an
        // operator who lost it does not have to regenerate.
        const bool has_dev = nvs_get_str(h, "dev_secret", sec, &slen) == ESP_OK && slen > 1;
        if (has_dev && in_hand) cJSON_AddStringToObject(ad, "deviceSecret", sec);
        cJSON_AddBoolToObject(ad, "deviceSecretSet", has_dev);
        cJSON *sess = cJSON_AddObjectToObject(root, "session");
        dump_i32(h, sess, "silenceTimeoutMs", "silence_ms");
        dump_i32(h, sess, "voiceLevel", "voice_lvl");
        dump_str(h, root, "configVersion", "cfg_ver");
        nvs_close(h);
    }
    return root;
}

char *sebastian_config_dump_json(void) {
    cJSON *root = config_dump(false);
    if (!root) return NULL;
    char *s = cJSON_PrintUnformatted(root);
    cJSON_Delete(root);
    return s;
}

void sebastian_config_dump_free(char *s) { cJSON_free(s); }

static void handle_config_get(void) {
    hold_until_us = esp_timer_get_time() + HOLD_AFTER_GET_US;
    cJSON *root = config_dump(true);
    if (!root) { reply("sebastian.config.err nvs_open"); return; }
    char *s = cJSON_PrintUnformatted(root);
    cJSON_Delete(root);
    if (!s) { reply("sebastian.config.err oom"); return; }
    size_t n = strlen(DUMP_PREFIX) + strlen(s) + 1;
    char *line = malloc(n);
    if (line) {
        snprintf(line, n, "%s%s", DUMP_PREFIX, s);
        reply(line);
        free(line);
    } else {
        reply("sebastian.config.err oom");
    }
    cJSON_free(s);
    ESP_LOGI(TAG, "dump sent (%u bytes); USB window held open 120 s", (unsigned)n);
}

static void handle_line(const char *line) {
    if (strcmp(line, GET_CMD) == 0) {
        handle_config_get();
        return;
    }
    if (strncmp(line, PROFILE_PREFIX, strlen(PROFILE_PREFIX)) == 0) {
        handle_profile_set(line + strlen(PROFILE_PREFIX));
        return;
    }
    if (strncmp(line, PROV_PREFIX, strlen(PROV_PREFIX)) != 0) return;
    char why[32];
    usj_ready_for_serial_provisioning = true;
    bool ok = sebastian_provisioning_apply(line + strlen(PROV_PREFIX), why, sizeof(why));
    usj_ready_for_serial_provisioning = false;
    if (!ok) {
        char msg[64];
        snprintf(msg, sizeof(msg), "sebastian.config.err %s", why);
        reply(msg);
        return;
    }

    reply("sebastian.config.ok");
    vTaskDelay(pdMS_TO_TICKS(300)); // let the reply drain before the reset
    esp_restart();
}

static void provisioning_task(void *arg) {
    (void)arg;
    static char line[1024];
    size_t pos = 0;
    for (;;) {
        uint8_t byte;
        if (usb_serial_jtag_read_bytes(&byte, 1, pdMS_TO_TICKS(200)) != 1) continue;
        if (byte == '\n' || byte == '\r') {
            line[pos] = '\0';
            if (pos > 0) handle_line(line);
            pos = 0;
        } else if (pos < sizeof(line) - 1) {
            line[pos++] = (char)byte;
        } else {
            pos = 0; // overlong line — drop it
        }
    }
}

// Install the USB-Serial-JTAG driver and spawn the receiver. Idempotent.
void sebastian_provisioning_start(void) {
    if (!usj_ready) {
        usb_serial_jtag_driver_config_t cfg = {
            .tx_buffer_size = 256,
            .rx_buffer_size = 1024,
        };
        if (usb_serial_jtag_driver_install(&cfg) == ESP_OK) usj_ready = true;
    }
    xTaskCreate(provisioning_task, "provisioning", 4096, NULL, 5, NULL);
    ESP_LOGI(TAG, "provisioning receiver ready (send %s{json})", PROV_PREFIX);
}
