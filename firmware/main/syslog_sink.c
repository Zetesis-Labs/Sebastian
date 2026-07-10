// Ships the device's ESP_LOG output to a remote UDP syslog server, while keeping
// UART output intact. See syslog_sink.h for the why. UDP is fire-and-forget: the
// hook never blocks the caller, and a dropped datagram just drops a log line — the
// device keeps running regardless of the receiver's health.
//
// A small C shim (like token_http.c / provisioning.c) so Zig avoids binding
// esp_log_set_vprintf's va_list and the BSD socket structs.

#include <stdint.h>
#include <stdio.h>
#include <string.h>
#include <stdarg.h>

#include "lwip/sockets.h"
#include "esp_log.h"
#include "nvs.h"
#include "freertos/FreeRTOS.h"
#include "freertos/semphr.h"

#include "syslog_sink.h"

#define NVS_NS "sebastian"
static const char *TAG = "syslog";

static int sock = -1;
static struct sockaddr_in dest;
static vprintf_like_t orig = NULL;   // the previous handler (UART); we chain to it
static SemaphoreHandle_t lock = NULL;
static char msg[512];

// esp_log invokes this for every log line, from many tasks. We must be fast and
// never block: keep UART (chain to `orig`), then best-effort mirror over UDP.
static int syslog_vprintf(const char *fmt, va_list args) {
    va_list uart;
    va_copy(uart, args);
    int r = orig ? orig(fmt, uart) : vprintf(fmt, uart);
    va_end(uart);

    // Non-blocking guard: if another line holds the buffer, skip the mirror (UART
    // already emitted it). Timeout 0 → never stalls the logging task.
    if (sock < 0 || lock == NULL || xSemaphoreTake(lock, 0) != pdTRUE) return r;

    // RFC3164: "<PRI>tag: message". PRI = facility(1=user) << 3 | severity(6=info).
    int n = snprintf(msg, sizeof(msg), "<14>sebastian-device: ");
    int m = vsnprintf(msg + n, sizeof(msg) - (size_t)n, fmt, args);
    int len = n + (m > 0 ? m : 0);
    if (len > (int)sizeof(msg)) len = (int)sizeof(msg);
    while (len > 0 && (msg[len - 1] == '\n' || msg[len - 1] == '\r')) len--;
    if (len > 0) {
        sendto(sock, msg, (size_t)len, 0, (struct sockaddr *)&dest, sizeof(dest));
    }

    xSemaphoreGive(lock);
    return r;
}

static bool read_str(const char *key, char *out, size_t out_size) {
    nvs_handle_t h;
    if (nvs_open(NVS_NS, NVS_READONLY, &h) != ESP_OK) return false;
    size_t len = out_size;
    esp_err_t err = nvs_get_str(h, key, out, &len);
    nvs_close(h);
    return err == ESP_OK && len > 1; // len includes the NUL
}

static int32_t read_i32(const char *key, int32_t def) {
    nvs_handle_t h;
    if (nvs_open(NVS_NS, NVS_READONLY, &h) != ESP_OK) return def;
    int32_t v = def;
    if (nvs_get_i32(h, key, &v) != ESP_OK) v = def;
    nvs_close(h);
    return v;
}

void sebastian_syslog_start(void) {
    char ip[64] = {0};
    if (!read_str("syslog_ip", ip, sizeof(ip)) || ip[0] == '\0') {
        ESP_LOGI(TAG, "syslog sink off (no syslog_ip in NVS)");
        return;
    }
    int32_t port = read_i32("syslog_port", 514);

    sock = socket(AF_INET, SOCK_DGRAM, IPPROTO_UDP);
    if (sock < 0) {
        ESP_LOGE(TAG, "socket() failed — syslog sink off");
        return;
    }
    memset(&dest, 0, sizeof(dest));
    dest.sin_family = AF_INET;
    dest.sin_port = htons((uint16_t)port);
    dest.sin_addr.s_addr = inet_addr(ip);

    lock = xSemaphoreCreateMutex();
    orig = esp_log_set_vprintf(syslog_vprintf);
    ESP_LOGI(TAG, "syslog sink -> %s:%ld", ip, (long)port);
}
