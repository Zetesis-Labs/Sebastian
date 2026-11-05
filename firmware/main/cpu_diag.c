// CPU per task (the cpustats diagnostic build, sdkconfig.cpustats): every call
// logs the share of one core each task used since the previous call, with
// the core it is pinned to and its priority, plus each core's idle task —
// the free CPU. A no-op in the normal build.
#include "sebastian_fleet.h"

#include <stdio.h>
#include <string.h>

#include "av_render.h"
#include "esp_heap_caps.h"
#include "esp_log.h"
#include "freertos/FreeRTOS.h"
#include "freertos/task.h"

#if CONFIG_FREERTOS_GENERATE_RUN_TIME_STATS
static const char *TAG = "cpu";

#define MAX_TASKS 48
#define TOP 10

static TaskStatus_t *prev, *now;
static UBaseType_t prev_n;
static configRUN_TIME_COUNTER_TYPE prev_total;

static configRUN_TIME_COUNTER_TYPE previous_counter(TaskHandle_t handle) {
    for (UBaseType_t i = 0; i < prev_n; i++) {
        if (prev[i].xHandle == handle) return prev[i].ulRunTimeCounter;
    }
    return 0;
}

static int core_of(const TaskStatus_t *t) {
#if configTASKLIST_INCLUDE_COREID
    return t->xCoreID == tskNO_AFFINITY ? -1 : (int)t->xCoreID;
#else
    (void)t;
    return -1;
#endif
}

void sebastian_cpu_report(const char *label) {
    if (!prev) {
        prev = heap_caps_calloc(MAX_TASKS, sizeof(TaskStatus_t), MALLOC_CAP_SPIRAM);
        now = heap_caps_calloc(MAX_TASKS, sizeof(TaskStatus_t), MALLOC_CAP_SPIRAM);
        if (!prev || !now) {
            ESP_LOGE(TAG, "no PSRAM for the task snapshots — no CPU report");
            return;
        }
    }
    configRUN_TIME_COUNTER_TYPE total = 0;
    UBaseType_t n = uxTaskGetSystemState(now, MAX_TASKS, &total);
    if (n == 0) {
        ESP_LOGW(TAG, "more than %d tasks — no CPU report", MAX_TASKS);
        return;
    }
    const configRUN_TIME_COUNTER_TYPE dt = total - prev_total;
    if (prev_total != 0 && dt > 0) {
        int top[TOP];
        uint32_t top_pm[TOP];
        int ntop = 0;
        char line[512];
        int off = snprintf(line, sizeof(line), "cpu[%s]", label);
        for (UBaseType_t i = 0; i < n; i++) {
            const uint32_t pm = (uint32_t)((uint64_t)(now[i].ulRunTimeCounter - previous_counter(now[i].xHandle)) * 1000 / dt);
            if (strncmp(now[i].pcTaskName, "IDLE", 4) == 0) {
                off += snprintf(line + off, sizeof(line) - off, " %s=%lu.%lu%%", now[i].pcTaskName, (unsigned long)(pm / 10), (unsigned long)(pm % 10));
                continue;
            }
            int at = ntop < TOP ? ntop++ : TOP;
            while (at > 0 && top_pm[at - 1] < pm) {
                if (at < TOP) { top[at] = top[at - 1]; top_pm[at] = top_pm[at - 1]; }
                at--;
            }
            if (at < TOP) { top[at] = (int)i; top_pm[at] = pm; }
        }
        off += snprintf(line + off, sizeof(line) - off, " |");
        for (int k = 0; k < ntop && off < (int)sizeof(line); k++) {
            const TaskStatus_t *t = &now[top[k]];
            off += snprintf(line + off, sizeof(line) - off, " %s(%d,%u)=%lu.%lu%%", t->pcTaskName, core_of(t),
                            (unsigned)t->uxCurrentPriority, (unsigned long)(top_pm[k] / 10), (unsigned long)(top_pm[k] % 10));
        }
        ESP_LOGI(TAG, "%s", line);
    }
    memcpy(prev, now, n * sizeof(TaskStatus_t));
    prev_n = n;
    prev_total = total;
}
void livekit_rx_stats(uint32_t *frames, uint32_t *refused, uint32_t *max_gap_ms);

void sebastian_playback_diag(uint32_t render_peak, void *render) {
    uint32_t frames = 0, refused = 0, max_gap_ms = 0;
    livekit_rx_stats(&frames, &refused, &max_gap_ms);
    av_render_fifo_stat_t fifo = {0};
    if (render) av_render_get_audio_fifo_level((av_render_handle_t)render, &fifo);
    ESP_LOGI(TAG, "rx: frames=%lu refused=%lu max_gap=%lums render_peak=%lu (full scale 8388607) | fifo: dec q=%d %dB render q=%d %dB dur=%lums",
             (unsigned long)frames, (unsigned long)refused, (unsigned long)max_gap_ms, (unsigned long)render_peak,
             fifo.q_num, fifo.data_size, fifo.render_q_num, fifo.render_data_size, (unsigned long)fifo.duration);
}
#else
void sebastian_cpu_report(const char *label) { (void)label; }
void sebastian_playback_diag(uint32_t render_peak, void *render) { (void)render_peak; (void)render; }
#endif
