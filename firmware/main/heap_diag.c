// Internal-RAM telemetry over ESP_LOG, so it reaches syslog. Each heap[...]
// line adds the free/used block counts to the free and largest-block figures:
// a rising free-block count with a flat free total is fragmentation, not a
// leak. Failed allocations are kept from the hook (which must not log: the
// syslog sink allocates) and reported at the next heap[...] line, with the
// task, the heap state at that moment and a backtrace. Decode it with:
//   xtensa-esp32s3-elf-addr2line -pfiaC -e build/sebastian.elf <bt...>

#include <inttypes.h>
#include <stdbool.h>
#include <stdio.h>
#include <string.h>

#include "esp_cpu_utils.h"
#include "esp_debug_helpers.h"
#include "esp_heap_caps.h"
#include "esp_log.h"
#include "freertos/FreeRTOS.h"
#include "freertos/task.h"

#define FAIL_SLOTS 4
#define FAIL_DEPTH 8

typedef struct {
    uint32_t t_ms;
    const char *via;
    uint32_t size;
    uint32_t caps;
    uint32_t free;
    uint32_t largest;
    uint32_t pc[FAIL_DEPTH];
    char task[configMAX_TASK_NAME_LEN];
} alloc_failure_t;

static const char *TAG = "heap";
static portMUX_TYPE fail_lock = portMUX_INITIALIZER_UNLOCKED;
static alloc_failure_t fail_slots[FAIL_SLOTS];
static uint32_t fail_total;
static uint32_t fail_reported;

static void on_alloc_failed(size_t size, uint32_t caps, const char *via) {
    alloc_failure_t rec = {
        .t_ms = esp_log_timestamp(),
        .via = via,
        .size = size,
        .caps = caps,
    };
    if (!xPortInIsrContext()) {
        rec.free = heap_caps_get_free_size(caps);
        rec.largest = heap_caps_get_largest_free_block(caps);
        strlcpy(rec.task, pcTaskGetName(NULL), sizeof(rec.task));
        esp_backtrace_frame_t frame = {0};
        esp_backtrace_get_start(&frame.pc, &frame.sp, &frame.next_pc);
        for (int i = 0; i < FAIL_DEPTH && esp_backtrace_get_next_frame(&frame); i++) {
            rec.pc[i] = esp_cpu_process_stack_pc(frame.pc);
        }
    } else {
        strlcpy(rec.task, "isr", sizeof(rec.task));
    }
    portENTER_CRITICAL_SAFE(&fail_lock);
    fail_slots[fail_total % FAIL_SLOTS] = rec;
    fail_total++;
    portEXIT_CRITICAL_SAFE(&fail_lock);
}

void sebastian_heap_diag_start(void) {
    esp_err_t err = heap_caps_register_failed_alloc_callback(on_alloc_failed);
    if (err != ESP_OK) {
        ESP_LOGE(TAG, "failed-allocation hook not installed: %s", esp_err_to_name(err));
    }
}

static void report_failures(void) {
    alloc_failure_t recs[FAIL_SLOTS];
    portENTER_CRITICAL(&fail_lock);
    uint32_t total = fail_total;
    uint32_t from = fail_reported;
    memcpy(recs, fail_slots, sizeof(recs));
    fail_reported = total;
    portEXIT_CRITICAL(&fail_lock);

    if (total - from > FAIL_SLOTS) {
        ESP_LOGW(TAG, "alloc failed: %" PRIu32 " older failures not kept", total - from - FAIL_SLOTS);
        from = total - FAIL_SLOTS;
    }
    for (uint32_t n = from; n < total; n++) {
        const alloc_failure_t *r = &recs[n % FAIL_SLOTS];
        char bt[FAIL_DEPTH * 11 + 1] = "";
        size_t used = 0;
        for (int i = 0; i < FAIL_DEPTH && r->pc[i] != 0; i++) {
            used += snprintf(bt + used, sizeof(bt) - used, " 0x%08" PRIx32, r->pc[i]);
        }
        ESP_LOGW(TAG,
                 "alloc failed #%" PRIu32 " size=%" PRIu32 " caps=0x%" PRIx32 " via %s task=%s"
                 " free=%" PRIu32 " largest=%" PRIu32 " t=%" PRIu32 "ms bt%s",
                 n + 1, r->size, r->caps, r->via ? r->via : "?", r->task, r->free, r->largest, r->t_ms, bt);
    }
}

void sebastian_heap_log(const char *label) {
    multi_heap_info_t in;
    heap_caps_get_info(&in, MALLOC_CAP_INTERNAL);
    size_t dma_free = heap_caps_get_free_size(MALLOC_CAP_INTERNAL | MALLOC_CAP_DMA);
    size_t dma_big = heap_caps_get_largest_free_block(MALLOC_CAP_INTERNAL | MALLOC_CAP_DMA);
    portENTER_CRITICAL(&fail_lock);
    uint32_t fails = fail_total;
    portEXIT_CRITICAL(&fail_lock);
    ESP_LOGI(TAG,
             "heap[%s] internal free=%zu largest=%zu | dma free=%zu largest=%zu"
             " | blocks free=%zu used=%zu | min=%zu | alloc_fails=%" PRIu32,
             label, in.total_free_bytes, in.largest_free_block, dma_free, dma_big,
             in.free_blocks, in.allocated_blocks, in.minimum_free_bytes, fails);
    report_failures();
}

// Session leak window (diagnostic builds with CONFIG_HEAP_TRACING_STANDALONE,
// see sdkconfig.heaptrace): traces every allocation from just before the room
// is created until the post-session drain, keeps the ones still alive and logs
// those in internal RAM with their call stack. The records live in PSRAM.
#if CONFIG_HEAP_TRACING_STANDALONE
#include "esp_heap_trace.h"
#include "esp_memory_utils.h"

#define TRACE_RECORDS 2000

static heap_trace_record_t *trace_records;
static bool trace_ready;
static uint32_t trace_window;

void sebastian_heap_trace_begin(void) {
    if (!trace_ready) {
        if (trace_records == NULL) {
            trace_records = heap_caps_calloc(TRACE_RECORDS, sizeof(heap_trace_record_t), MALLOC_CAP_SPIRAM);
            if (trace_records == NULL) {
                ESP_LOGE(TAG, "trace: no PSRAM for %d records", TRACE_RECORDS);
                return;
            }
        }
        esp_err_t err = heap_trace_init_standalone(trace_records, TRACE_RECORDS);
        if (err != ESP_OK) {
            ESP_LOGE(TAG, "trace: init failed: %s", esp_err_to_name(err));
            return;
        }
        trace_ready = true;
    }
    if (heap_trace_stop() == ESP_OK) {
        ESP_LOGW(TAG, "trace[%" PRIu32 "] was never closed: its window is lost", trace_window);
    }
    esp_err_t err = heap_trace_start(HEAP_TRACE_LEAKS);
    if (err != ESP_OK) {
        ESP_LOGE(TAG, "trace: start failed: %s", esp_err_to_name(err));
        return;
    }
    trace_window++;
}

void sebastian_heap_trace_end(void) {
    esp_err_t err = heap_trace_stop();
    if (err != ESP_OK) {
        if (err != ESP_ERR_INVALID_STATE) {
            ESP_LOGW(TAG, "trace: stop failed: %s", esp_err_to_name(err));
        }
        return;
    }
    heap_trace_summary_t sum = {0};
    err = heap_trace_summary(&sum);
    if (err != ESP_OK) {
        ESP_LOGE(TAG, "trace[%" PRIu32 "] summary failed: %s", trace_window, esp_err_to_name(err));
        return;
    }
    size_t internal = 0, internal_bytes = 0, psram = 0, unreadable = 0;
    for (size_t i = 0; i < sum.count; i++) {
        heap_trace_record_t r;
        if (heap_trace_get(i, &r) != ESP_OK) {
            unreadable++;
            continue;
        }
        if (r.address == NULL) {
            continue;
        }
        if (!esp_ptr_internal(r.address)) {
            psram++;
            continue;
        }
        internal++;
        internal_bytes += r.size;
        char bt[CONFIG_HEAP_TRACING_STACK_DEPTH * 11 + 1] = "";
        size_t used = 0;
        for (int j = 0; j < CONFIG_HEAP_TRACING_STACK_DEPTH && r.alloced_by[j] != NULL; j++) {
            used += snprintf(bt + used, sizeof(bt) - used, " %p", r.alloced_by[j]);
        }
        ESP_LOGI(TAG, "trace[%" PRIu32 "] alive addr=%p size=%zu by%s", trace_window, r.address, r.size, bt);
        if (internal % 8 == 0) {
            vTaskDelay(pdMS_TO_TICKS(20));
        }
    }
    if (sum.has_overflowed || unreadable) {
        ESP_LOGW(TAG, "trace[%" PRIu32 "] INCOMPLETE: records overflowed=%zu unreadable=%zu",
                 trace_window, sum.has_overflowed, unreadable);
    }
    ESP_LOGI(TAG,
             "trace[%" PRIu32 "] summary: internal alive=%zu (%zu B) psram alive=%zu | allocs=%zu frees=%zu"
             " | records high=%zu/%zu overflowed=%zu | psram free=%zu",
             trace_window, internal, internal_bytes, psram, sum.total_allocations, sum.total_frees,
             sum.high_water_mark, sum.capacity, sum.has_overflowed, heap_caps_get_free_size(MALLOC_CAP_SPIRAM));
}
#else
void sebastian_heap_trace_begin(void) {}
void sebastian_heap_trace_end(void) {}
#endif
