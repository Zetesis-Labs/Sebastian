#include "token_http.h"

#include <string.h>
#include "esp_http_client.h"
#include "esp_log.h"

static const char *TAG = "token_http";

int token_http_get(const char *url, char *out, size_t out_size) {
    esp_http_client_config_t config = {
        .url = url,
        .method = HTTP_METHOD_GET,
        .timeout_ms = 5000,
        .crt_bundle_attach = NULL, // token server is plain HTTP on the LAN
    };
    esp_http_client_handle_t client = esp_http_client_init(&config);
    if (client == NULL) {
        ESP_LOGE(TAG, "client init failed");
        return -1;
    }

    int result;
    esp_err_t err = esp_http_client_open(client, 0);
    if (err != ESP_OK) {
        ESP_LOGE(TAG, "open failed: %s", esp_err_to_name(err));
        result = -2;
        goto cleanup;
    }

    esp_http_client_fetch_headers(client);
    int status = esp_http_client_get_status_code(client);
    if (status != 200) {
        ESP_LOGE(TAG, "HTTP %d", status);
        result = -3;
        goto cleanup;
    }

    // out_size - 1 to leave room for the null terminator.
    int read = esp_http_client_read_response(client, out, (int)out_size - 1);
    if (read < 0) {
        ESP_LOGE(TAG, "read failed");
        result = -4;
        goto cleanup;
    }
    out[read] = '\0';
    result = read;

cleanup:
    esp_http_client_close(client);
    esp_http_client_cleanup(client);
    return result;
}
