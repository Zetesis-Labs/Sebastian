// Reads back the core dump the panic handler saved to flash on the previous
// boot and logs its summary — task, exception cause, PC and backtrace — over
// ESP_LOG, so it reaches syslog. The panic itself only prints to the console,
// which on this board is UART0 (repurposed as I2S) or the USB-JTAG (owned by
// TinyUSB after boot): nothing reads it in the field. Decode the addresses on
// the host with:
//   xtensa-esp32s3-elf-addr2line -pfiaC -e build/sebastian.elf <pc> <bt...>
// A small C shim (like token_http.c) so Zig avoids the coredump structs.

#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "esp_core_dump.h"
#include "esp_log.h"

static const char *TAG = "coredump";

void sebastian_coredump_report(void) {
    esp_err_t chk = esp_core_dump_image_check();
    if (chk != ESP_OK) {
        ESP_LOGI(TAG, "no core dump in flash (%s)", esp_err_to_name(chk));
        return;
    }
    esp_core_dump_summary_t *s = calloc(1, sizeof(*s));
    if (s == NULL) {
        ESP_LOGE(TAG, "summary alloc failed");
        return;
    }
    esp_err_t err = esp_core_dump_get_summary(s);
    if (err != ESP_OK) {
        ESP_LOGE(TAG, "summary read failed: %s", esp_err_to_name(err));
    } else {
        ESP_LOGE(TAG, "task=%s exc_cause=%lu exc_vaddr=0x%08lx pc=0x%08lx elf=%s",
                 s->exc_task, (unsigned long)s->ex_info.exc_cause,
                 (unsigned long)s->ex_info.exc_vaddr, (unsigned long)s->exc_pc,
                 (const char *)s->app_elf_sha256);
        char line[16 * 11 + 1];
        size_t n = 0;
        uint32_t depth = s->exc_bt_info.depth;
        if (depth > 16) depth = 16;
        for (uint32_t i = 0; i < depth && n < sizeof(line) - 11; i++) {
            n += (size_t)snprintf(line + n, sizeof(line) - n, "0x%08lx ",
                                  (unsigned long)s->exc_bt_info.bt[i]);
        }
        ESP_LOGE(TAG, "bt depth=%lu%s: %s", (unsigned long)s->exc_bt_info.depth,
                 s->exc_bt_info.corrupted ? " (corrupted)" : "", line);
    }
    free(s);
    // One report per crash: erase so the next boot does not repeat it.
    esp_core_dump_image_erase();
}
