#include "profiles.h"

#include <limits.h>
#include <string.h>

#include "cJSON.h"
#include "esp_log.h"
#include "nvs.h"
#include "nvs_flash.h"

static const char *TAG = "profiles";

#define NVS_NS "sebastian" // same namespace as provisioning.c
#define KEY_PROFILES "profiles"
#define KEY_ACTIVE "active_prof"
#define KEY_BOOT_FAILS "boot_fails"

static void nvs_ensure_init(void) {
    esp_err_t err = nvs_flash_init();
    if (err == ESP_ERR_NVS_NO_FREE_PAGES || err == ESP_ERR_NVS_NEW_VERSION_FOUND) {
        ESP_ERROR_CHECK(nvs_flash_erase());
        ESP_ERROR_CHECK(nvs_flash_init());
    }
}

static bool read_str(const char *key, char *out, size_t out_size) {
    nvs_handle_t h;
    if (nvs_open(NVS_NS, NVS_READONLY, &h) != ESP_OK) return false;
    size_t len = out_size;
    esp_err_t err = nvs_get_str(h, key, out, &len);
    nvs_close(h);
    return err == ESP_OK;
}

static bool write_str(const char *key, const char *value) {
    nvs_handle_t h;
    if (nvs_open(NVS_NS, NVS_READWRITE, &h) != ESP_OK) return false;
    bool ok = nvs_set_str(h, key, value) == ESP_OK && nvs_commit(h) == ESP_OK;
    nvs_close(h);
    return ok;
}

static void profile_defaults(sebastian_profile_t *p) {
    memset(p, 0, sizeof(*p));
    p->mode = SEBASTIAN_PROFILE_MODE_AGENT;
    p->full_duplex = -1;
    p->fixed_beam = -1;
    p->beam_az = INT32_MIN;
    p->wifi = -1;
    p->telemetry = -1;
}

static int builtin_profiles(sebastian_profile_t *out, int max) {
    if (max < 1) return 0;
    profile_defaults(&out[0]);
    strlcpy(out[0].name, "agente", sizeof(out[0].name));
    if (max < 2) return 1;
    profile_defaults(&out[1]);
    strlcpy(out[1].name, "micro-usb", sizeof(out[1].name));
    out[1].mode = SEBASTIAN_PROFILE_MODE_USB_MIC;
    // No speaker in USB-mic mode → no AEC to converge → adaptive beam tracks
    // the talker. Explicit here so a legacy fixed_beam=true key can't leak in.
    out[1].fixed_beam = 0;
    return 2;
}

static void parse_opt_bool(const cJSON *obj, const char *key, int8_t *out) {
    const cJSON *item = cJSON_GetObjectItem(obj, key);
    if (cJSON_IsBool(item)) *out = cJSON_IsTrue(item) ? 1 : 0;
}

static bool parse_profile(const cJSON *obj, sebastian_profile_t *p) {
    const cJSON *name = cJSON_GetObjectItem(obj, "name");
    if (!cJSON_IsString(name) || name->valuestring[0] == '\0') return false;

    profile_defaults(p);
    strlcpy(p->name, name->valuestring, sizeof(p->name));

    const cJSON *mode = cJSON_GetObjectItem(obj, "mode");
    if (cJSON_IsString(mode) && strcmp(mode->valuestring, "usb_mic") == 0) {
        p->mode = SEBASTIAN_PROFILE_MODE_USB_MIC;
    }
    parse_opt_bool(obj, "fullDuplex", &p->full_duplex);
    parse_opt_bool(obj, "fixedBeam", &p->fixed_beam);
    parse_opt_bool(obj, "wifi", &p->wifi);
    parse_opt_bool(obj, "telemetry", &p->telemetry);
    const cJSON *az = cJSON_GetObjectItem(obj, "beamAzimuthDeg");
    if (cJSON_IsNumber(az)) {
        double d = az->valuedouble;
        p->beam_az = (int32_t)(d < 0 ? d - 0.5 : d + 0.5);
    }
    return true;
}

int sebastian_profiles_load(sebastian_profile_t *out, int max) {
    nvs_ensure_init();

    // NVS strings are capped well below this; the provisioning line buffer
    // (1 KB) is the real limit on how big the provisioned array can get.
    static char json[1024];
    if (!read_str(KEY_PROFILES, json, sizeof(json))) return builtin_profiles(out, max);

    cJSON *root = cJSON_Parse(json);
    if (!cJSON_IsArray(root)) {
        ESP_LOGE(TAG, "profiles key is not a JSON array — using built-ins");
        cJSON_Delete(root);
        return builtin_profiles(out, max);
    }

    int count = 0;
    const cJSON *item;
    cJSON_ArrayForEach(item, root) {
        if (count >= max) {
            ESP_LOGW(TAG, "more than %d profiles provisioned — extras ignored", max);
            break;
        }
        if (parse_profile(item, &out[count])) {
            count++;
        } else {
            ESP_LOGW(TAG, "profile entry without name skipped");
        }
    }
    cJSON_Delete(root);

    if (count == 0) return builtin_profiles(out, max);
    return count;
}

int sebastian_profiles_active_index(const sebastian_profile_t *list, int count) {
    char name[SEBASTIAN_PROFILE_NAME_MAX];
    if (!read_str(KEY_ACTIVE, name, sizeof(name))) return 0;
    for (int i = 0; i < count; i++) {
        if (strcmp(list[i].name, name) == 0) return i;
    }
    ESP_LOGW(TAG, "active profile '%s' not in list — falling back to index 0", name);
    return 0;
}

bool sebastian_profiles_set_active(const char *name) {
    sebastian_profile_t list[SEBASTIAN_PROFILES_MAX];
    int count = sebastian_profiles_load(list, SEBASTIAN_PROFILES_MAX);
    for (int i = 0; i < count; i++) {
        if (strcmp(list[i].name, name) == 0) return write_str(KEY_ACTIVE, name);
    }
    ESP_LOGE(TAG, "set_active: no profile named '%s'", name);
    return false;
}

int32_t sebastian_boot_note_start(void) {
    nvs_ensure_init();
    nvs_handle_t h;
    if (nvs_open(NVS_NS, NVS_READWRITE, &h) != ESP_OK) return 0;
    int32_t fails = 0;
    nvs_get_i32(h, KEY_BOOT_FAILS, &fails);
    fails++;
    nvs_set_i32(h, KEY_BOOT_FAILS, fails);
    nvs_commit(h);
    nvs_close(h);
    return fails;
}

void sebastian_boot_note_ok(void) {
    nvs_handle_t h;
    if (nvs_open(NVS_NS, NVS_READWRITE, &h) != ESP_OK) return;
    nvs_set_i32(h, KEY_BOOT_FAILS, 0);
    nvs_commit(h);
    nvs_close(h);
}
