// Authenticated HTTP against the control room: POST /v1/sessions (per-device
// secret, block 1 of docs/implementation/11-fleet-adoption-control-room.md),
// a GET that carries the device credentials and captures one response header
// (the desired-config poll), and the enrolment that turns the organization
// secret into a device secret (RF-51). C because
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
    const char *name[2];
    char *out[2];
    size_t size[2];
} header_capture_t;

static esp_err_t on_http_event(esp_http_client_event_t *evt) {
    header_capture_t *cap = evt->user_data;
    if (evt->event_id != HTTP_EVENT_ON_HEADER || !cap || !evt->header_key || !evt->header_value) return ESP_OK;
    for (int i = 0; i < 2; i++) {
        if (cap->name[i] && cap->out[i] && strcasecmp(evt->header_key, cap->name[i]) == 0) {
            strlcpy(cap->out[i], evt->header_value, cap->size[i]);
        }
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
    return sebastian_http_get_auth2(url, device_id, secret, capture_header, hdr_out, hdr_size, NULL, NULL, 0, out, out_size, status);
}

int sebastian_http_get_auth2(const char *url, const char *device_id, const char *secret,
                             const char *header1, char *hdr1_out, size_t hdr1_size,
                             const char *header2, char *hdr2_out, size_t hdr2_size,
                             char *out, size_t out_size, int *status) {
    if (hdr1_out && hdr1_size) hdr1_out[0] = '\0';
    if (hdr2_out && hdr2_size) hdr2_out[0] = '\0';
    if (status) *status = 0;
    header_capture_t cap = {.name = {header1, header2}, .out = {hdr1_out, hdr2_out}, .size = {hdr1_size, hdr2_size}};
    const bool capturing = (header1 && hdr1_out && hdr1_size) || (header2 && hdr2_out && hdr2_size);
    esp_http_client_handle_t client = open_client(url, HTTP_METHOD_GET, device_id, secret, capturing ? &cap : NULL);
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
                             const char *json_body,
                             char *url_out, size_t url_size, char *token_out, size_t token_size) {
    char url[300];
    snprintf(url, sizeof(url), "%s/v1/sessions", base_url);
    esp_http_client_handle_t client = open_client(url, HTTP_METHOD_POST, device_id, secret, NULL);
    if (client == NULL) return -1;
    const size_t body_len = json_body ? strlen(json_body) : 0;
    if (body_len) esp_http_client_set_header(client, "Content-Type", "application/json");

    int result;
    static char body[1536]; // JSON with a ~500 B JWT; static: internal RAM is scarce
    if (esp_http_client_open(client, (int)body_len) != ESP_OK) { result = -2; goto cleanup; }
    if (body_len && esp_http_client_write(client, json_body, (int)body_len) != (int)body_len) { result = -2; goto cleanup; }
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

int sebastian_report_running_config(const char *base_url, const char *device_id, const char *secret) {
    char *body = sebastian_config_dump_json();
    if (!body) return -7;
    static char url[300]; // static: called from the 4 KB poll task
    snprintf(url, sizeof(url), "%s/v1/devices/%s/running-config", base_url, device_id);
    esp_http_client_handle_t client = open_client(url, HTTP_METHOD_PUT, device_id, secret, NULL);
    int result;
    if (client == NULL) { result = -1; goto done; }
    esp_http_client_set_header(client, "Content-Type", "application/json");
    const int len = (int)strlen(body);
    if (esp_http_client_open(client, len) != ESP_OK) { result = -2; goto cleanup; }
    if (esp_http_client_write(client, body, len) != len) { result = -2; goto cleanup; }
    esp_http_client_fetch_headers(client);
    const int code = esp_http_client_get_status_code(client);
    result = code == 204 ? 0 : (code > 0 ? code : -3);
cleanup:
    esp_http_client_close(client);
    esp_http_client_cleanup(client);
done:
    sebastian_config_dump_free(body);
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

int sebastian_enroll(const char *base_url, const char *device_id, const char *org_secret, const char *device_secret) {
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
    int len = snprintf(body, sizeof(body), "{\"nonce\":\"%s\",\"mac\":\"%s\",\"deviceSecret\":\"%s\"}", nonce, mac, device_secret);
    if (len <= 0 || len >= (int)sizeof(body)) return -6;

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
    result = 0;
cleanup:
    esp_http_client_close(client);
    esp_http_client_cleanup(client);
    return result;
}

int sebastian_report_meeting(const char *base_url, const char *device_id, const char *secret,
                             const char *meeting_id, const char *state, const char *reason) {
    static char url[300];  // static: called from the app task with modest stack
    static char body[160];
    snprintf(url, sizeof(url), "%s/v1/devices/%s/meeting", base_url, device_id);
    if (reason && reason[0]) {
        snprintf(body, sizeof(body), "{\"meetingId\":\"%s\",\"state\":\"%s\",\"reason\":\"%s\"}", meeting_id, state, reason);
    } else {
        snprintf(body, sizeof(body), "{\"meetingId\":\"%s\",\"state\":\"%s\"}", meeting_id, state);
    }
    esp_http_client_handle_t client = open_client(url, HTTP_METHOD_PUT, device_id, secret, NULL);
    if (client == NULL) return -1;
    esp_http_client_set_header(client, "Content-Type", "application/json");
    const int len = (int)strlen(body);
    int result;
    if (esp_http_client_open(client, len) != ESP_OK) { result = -2; goto cleanup; }
    if (esp_http_client_write(client, body, len) != len) { result = -2; goto cleanup; }
    esp_http_client_fetch_headers(client);
    const int code = esp_http_client_get_status_code(client);
    result = code == 204 ? 0 : (code > 0 ? code : -3);
    if (result != 0) ESP_LOGW(TAG, "PUT /v1/devices/%s/meeting (%s) -> HTTP %d", device_id, state, code);
cleanup:
    esp_http_client_close(client);
    esp_http_client_cleanup(client);
    return result;
}

int sebastian_request_meeting(const char *base_url, const char *device_id, const char *secret,
                              char *id_out, size_t id_size) {
    static char url[300];
    static char body[512];
    snprintf(url, sizeof(url), "%s/v1/devices/%s/meetings", base_url, device_id);
    esp_http_client_handle_t client = open_client(url, HTTP_METHOD_POST, device_id, secret, NULL);
    if (client == NULL) return -1;
    int result;
    if (esp_http_client_open(client, 0) != ESP_OK) { result = -2; goto cleanup; }
    esp_http_client_fetch_headers(client);
    const int code = esp_http_client_get_status_code(client);
    if (code != 202) {
        ESP_LOGW(TAG, "POST /v1/devices/%s/meetings -> HTTP %d", device_id, code);
        result = code > 0 ? code : -3;
        goto cleanup;
    }
    const int read = esp_http_client_read_response(client, body, sizeof(body) - 1);
    if (read <= 0) { result = -4; goto cleanup; }
    body[read] = '\0';
    cJSON *root = cJSON_Parse(body);
    const cJSON *id = root ? cJSON_GetObjectItem(root, "id") : NULL;
    if (!cJSON_IsString(id) || strlen(id->valuestring) >= id_size) {
        cJSON_Delete(root);
        result = -5;
        goto cleanup;
    }
    strlcpy(id_out, id->valuestring, id_size);
    cJSON_Delete(root);
    result = 0;
cleanup:
    esp_http_client_close(client);
    esp_http_client_cleanup(client);
    return result;
}
