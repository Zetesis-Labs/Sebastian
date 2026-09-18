// Authenticated HTTP against the control room: POST /v1/sessions (per-device
// secret, block 1 of docs/implementation/11-fleet-adoption-control-room.md),
// a GET that carries the device credentials and captures one response header
// (the desired-config poll), and the enrolment that turns the organization
// secret into a device secret (RF-51). Same shape as token_http.c; C because
// esp_http_client_config_t and cJSON are painful to bind from Zig.
#include "sebastian_fleet.h"

#include <stdio.h>
#include <string.h>
#include <strings.h>

#include "cJSON.h"
#include "esp_http_client.h"
#include "esp_log.h"

static const char *TAG = "session_http";

// esp_http_client_get_header() reads the REQUEST headers; response headers only
// arrive through the event handler. This captures the one header we care about.
typedef struct {
    const char *name;
    char *out;
    size_t size;
} header_capture_t;

static esp_err_t on_http_event(esp_http_client_event_t *evt) {
    header_capture_t *cap = evt->user_data;
    if (evt->event_id == HTTP_EVENT_ON_HEADER && cap && cap->name && evt->header_key && evt->header_value &&
        strcasecmp(evt->header_key, cap->name) == 0) {
        strlcpy(cap->out, evt->header_value, cap->size);
    }
    return ESP_OK;
}

static esp_http_client_handle_t open_client(const char *url, esp_http_client_method_t method,
                                            const char *device_id, const char *secret, header_capture_t *cap) {
    esp_http_client_config_t config = {
        .url = url,
        .method = method,
        .timeout_ms = 5000,
        .crt_bundle_attach = NULL, // the control room is plain HTTP on the LAN
        .event_handler = cap ? on_http_event : NULL,
        .user_data = cap,
    };
    esp_http_client_handle_t client = esp_http_client_init(&config);
    if (client == NULL) return NULL;
    if (device_id && device_id[0]) esp_http_client_set_header(client, "X-Device-Id", device_id);
    if (secret && secret[0]) esp_http_client_set_header(client, "X-Device-Secret", secret);
    return client;
}

int sebastian_http_get_auth(const char *url, const char *device_id, const char *secret,
                            const char *capture_header, char *hdr_out, size_t hdr_size,
                            char *out, size_t out_size, int *status) {
    if (hdr_out && hdr_size) hdr_out[0] = '\0';
    if (status) *status = 0;
    header_capture_t cap = {.name = capture_header, .out = hdr_out, .size = hdr_size};
    esp_http_client_handle_t client = open_client(url, HTTP_METHOD_GET, device_id, secret,
                                                  (capture_header && hdr_out && hdr_size) ? &cap : NULL);
    if (client == NULL) return -1;
    int result;
    if (esp_http_client_open(client, 0) != ESP_OK) { result = -2; goto cleanup; }
    esp_http_client_fetch_headers(client);
    int code = esp_http_client_get_status_code(client);
    if (status) *status = code;
    if (code != 200) { result = -3; goto cleanup; }
    int read = esp_http_client_read_response(client, out, (int)out_size - 1);
    if (read < 0) { result = -4; goto cleanup; }
    out[read] = '\0';
    result = read;
cleanup:
    esp_http_client_close(client);
    esp_http_client_cleanup(client);
    return result;
}

int sebastian_session_create(const char *base_url, const char *device_id, const char *secret,
                             char *url_out, size_t url_size, char *token_out, size_t token_size) {
    char url[300];
    snprintf(url, sizeof(url), "%s/v1/sessions", base_url);
    esp_http_client_handle_t client = open_client(url, HTTP_METHOD_POST, device_id, secret, NULL);
    if (client == NULL) return -1;
    esp_http_client_set_header(client, "Content-Length", "0");

    int result;
    static char body[1536]; // JSON with a ~500 B JWT; static: internal RAM is scarce
    if (esp_http_client_open(client, 0) != ESP_OK) { result = -2; goto cleanup; }
    esp_http_client_fetch_headers(client);
    int code = esp_http_client_get_status_code(client);
    if (code != 201) {
        ESP_LOGE(TAG, "POST /v1/sessions -> HTTP %d", code);
        result = code > 0 ? code : -3;
        goto cleanup;
    }
    int read = esp_http_client_read_response(client, body, sizeof(body) - 1);
    if (read <= 0) { result = -4; goto cleanup; }
    body[read] = '\0';

    cJSON *root = cJSON_Parse(body);
    const cJSON *server_url = root ? cJSON_GetObjectItem(root, "serverUrl") : NULL;
    const cJSON *token = root ? cJSON_GetObjectItem(root, "token") : NULL;
    if (!cJSON_IsString(server_url) || !cJSON_IsString(token) ||
        strlen(server_url->valuestring) >= url_size || strlen(token->valuestring) >= token_size) {
        cJSON_Delete(root);
        ESP_LOGE(TAG, "session response malformed");
        result = -5;
        goto cleanup;
    }
    strlcpy(url_out, server_url->valuestring, url_size);
    strlcpy(token_out, token->valuestring, token_size);
    cJSON_Delete(root);
    result = 0;
cleanup:
    esp_http_client_close(client);
    esp_http_client_cleanup(client);
    return result;
}

static bool json_string_field(const char *body, const char *field, char *out, size_t out_size) {
    cJSON *root = cJSON_Parse(body);
    const cJSON *item = root ? cJSON_GetObjectItem(root, field) : NULL;
    const bool ok = cJSON_IsString(item) && strlen(item->valuestring) < out_size;
    if (ok) strlcpy(out, item->valuestring, out_size);
    cJSON_Delete(root);
    return ok;
}

int sebastian_enroll(const char *base_url, const char *device_id, const char *org_secret,
                     char *secret_out, size_t secret_size) {
    static char body[512]; // static: internal RAM is scarce, the poll task stack is 4 KB
    char url[300];
    snprintf(url, sizeof(url), "%s/v1/devices/%s/enroll", base_url, device_id);

    int status = 0;
    int n = sebastian_http_get_auth(url, device_id, NULL, NULL, NULL, 0, body, sizeof(body), &status);
    if (n < 0) return status > 0 ? status : n;
    char nonce[65];
    if (!json_string_field(body, "nonce", nonce, sizeof(nonce))) return -5;
    char mac[65];
    if (!sebastian_hmac_sha256_hex(org_secret, nonce, device_id, mac)) return -6;
    int len = snprintf(body, sizeof(body), "{\"nonce\":\"%s\",\"mac\":\"%s\"}", nonce, mac);

    esp_http_client_handle_t client = open_client(url, HTTP_METHOD_POST, device_id, NULL, NULL);
    if (client == NULL) return -1;
    esp_http_client_set_header(client, "Content-Type", "application/json");
    int result;
    if (esp_http_client_open(client, len) != ESP_OK) { result = -2; goto cleanup; }
    if (esp_http_client_write(client, body, len) != len) { result = -2; goto cleanup; }
    esp_http_client_fetch_headers(client);
    int code = esp_http_client_get_status_code(client);
    if (code != 201) {
        ESP_LOGW(TAG, "POST %s -> HTTP %d", url, code);
        result = code > 0 ? code : -3;
        goto cleanup;
    }
    n = esp_http_client_read_response(client, body, sizeof(body) - 1);
    if (n <= 0) { result = -4; goto cleanup; }
    body[n] = '\0';
    result = json_string_field(body, "deviceSecret", secret_out, secret_size) ? 0 : -5;
cleanup:
    esp_http_client_close(client);
    esp_http_client_cleanup(client);
    return result;
}
