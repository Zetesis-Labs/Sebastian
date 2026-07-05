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
// hand-written Zig bindings, so this lives here like token_http.c.

#include <string.h>

#include "esp_event.h"
#include "esp_log.h"
#include "esp_netif.h"
#include "esp_system.h"
#include "esp_wifi.h"
#include "nvs.h"
#include "nvs_flash.h"
#include "cJSON.h"
#include "driver/usb_serial_jtag.h"
#include "freertos/FreeRTOS.h"
#include "freertos/event_groups.h"
#include "freertos/task.h"

static const char *TAG = "provisioning";

#define NVS_NS "sebastian"
#define PROV_PREFIX "sebastian.config.v1 "
#define PROV_SCHEMA "sebastian.config.v1"

#define NET_CONNECTED (1 << 0)
#define NET_FAILED (1 << 1)
#define MAX_RETRIES 20

static EventGroupHandle_t net_events;
static int retry_attempt;
static bool usj_ready; // usb_serial_jtag driver installed (also used for replies)

// ── Replies go to USB-Serial-JTAG (where the installer listens), not the UART
//    primary console, so log noise and the ack/err lines stay on the right pipe.
static void reply(const char *line) {
    if (usj_ready) {
        usb_serial_jtag_write_bytes((const uint8_t *)line, strlen(line), pdMS_TO_TICKS(100));
        usb_serial_jtag_write_bytes((const uint8_t *)"\n", 1, pdMS_TO_TICKS(100));
    }
    ESP_LOGI(TAG, "%s", line);
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

// Returns true once an IP is obtained. Blocks like lk_example_network_connect.
bool sebastian_net_connect(void) {
    nvs_ensure_init(); // MUST precede load_wifi_creds — it reads provisioned NVS

    char ssid[33] = {0};
    char pass[65] = {0};
    if (!load_wifi_creds(ssid, sizeof(ssid), pass, sizeof(pass))) {
        ESP_LOGE(TAG, "no WiFi ssid (unprovisioned) — send sebastian.config.v1 over serial");
        return false;
    }

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
        bits = xEventGroupWaitBits(net_events, NET_CONNECTED | NET_FAILED, pdFALSE, pdFALSE, portMAX_DELAY);
    } while (!(bits & (NET_CONNECTED | NET_FAILED)));
    return (bits & NET_CONNECTED) != 0;
}

// ── Provisioning receiver ────────────────────────────────────────────────────
static void store_cfg_bool(nvs_handle_t h, const char *key, const cJSON *item) {
    if (cJSON_IsBool(item)) nvs_set_u8(h, key, cJSON_IsTrue(item) ? 1 : 0);
}

static bool store_wifi(cJSON *root) {
    cJSON *wifi = cJSON_GetObjectItem(root, "wifi");
    cJSON *ssid = wifi ? cJSON_GetObjectItem(wifi, "ssid") : NULL;
    cJSON *pass = wifi ? cJSON_GetObjectItem(wifi, "password") : NULL;
    if (!cJSON_IsString(ssid) || ssid->valuestring[0] == '\0') return false;

    nvs_ensure_init(); // the receiver can run before net_connect inits NVS
    nvs_handle_t h;
    esp_err_t oe = nvs_open(NVS_NS, NVS_READWRITE, &h);
    if (oe != ESP_OK) { ESP_LOGE(TAG, "store: open=%s", esp_err_to_name(oe)); return false; }
    esp_err_t se = nvs_set_str(h, "wifi_ssid", ssid->valuestring);
    bool ok = se == ESP_OK;
    if (ok && cJSON_IsString(pass)) ok = nvs_set_str(h, "wifi_pass", pass->valuestring) == ESP_OK;
    // token server URL is optional here (read path lives in token.zig) — store it
    // when present so a future factory image can drop the compiled default too.
    cJSON *lk = cJSON_GetObjectItem(root, "livekit");
    cJSON *url = lk ? cJSON_GetObjectItem(lk, "tokenServerUrl") : NULL;
    if (ok && cJSON_IsString(url)) nvs_set_str(h, "token_url", url->valuestring);
    // Operating mode + audio behaviour (config.zig::load reads these at boot). All
    // optional: absent keys keep config.zig's compiled defaults. mic_channel and
    // session timing stay compile-time, so they are intentionally not stored here.
    cJSON *mode = cJSON_GetObjectItem(root, "mode");
    if (ok && cJSON_IsString(mode)) nvs_set_str(h, "mode", mode->valuestring);
    cJSON *audio = cJSON_GetObjectItem(root, "audio");
    if (ok && cJSON_IsObject(audio)) {
        store_cfg_bool(h, "full_duplex", cJSON_GetObjectItem(audio, "fullDuplex"));
        store_cfg_bool(h, "fixed_beam", cJSON_GetObjectItem(audio, "fixedBeam"));
        store_cfg_bool(h, "probe_aec", cJSON_GetObjectItem(audio, "probeAecOnBoot"));
        store_cfg_bool(h, "probe_dual", cJSON_GetObjectItem(audio, "probeDualChannelOnBoot"));
        store_cfg_bool(h, "probe_ogain", cJSON_GetObjectItem(audio, "probeOutputGainOnBoot"));
        cJSON *az = cJSON_GetObjectItem(audio, "fixedBeamAzimuthDeg");
        if (cJSON_IsNumber(az)) {
            double d = az->valuedouble;
            nvs_set_i32(h, "beam_az", (int32_t)(d < 0 ? d - 0.5 : d + 0.5));
        }
    }
    esp_err_t ce = nvs_commit(h);
    if (ok) ok = ce == ESP_OK;
    nvs_close(h);
    ESP_LOGI(TAG, "store: set=%s commit=%s -> %s", esp_err_to_name(se), esp_err_to_name(ce), ok ? "ok" : "FAIL");
    return ok;
}

static void handle_line(const char *line) {
    if (strncmp(line, PROV_PREFIX, strlen(PROV_PREFIX)) != 0) return;
    cJSON *root = cJSON_Parse(line + strlen(PROV_PREFIX));
    if (!root) { reply("sebastian.config.err json_parse"); return; }

    cJSON *schema = cJSON_GetObjectItem(root, "schema");
    if (!cJSON_IsString(schema) || strcmp(schema->valuestring, PROV_SCHEMA) != 0) {
        reply("sebastian.config.err schema");
        cJSON_Delete(root);
        return;
    }
    bool ok = store_wifi(root);
    cJSON_Delete(root);
    if (!ok) { reply("sebastian.config.err wifi"); return; }

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
