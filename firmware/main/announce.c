// mDNS announcement (block 2 of docs/implementation/11-fleet-adoption-control-room.md):
// _sebastian._tcp on the LAN with what a control room needs to list the unit
// without talking to it — id (MAC), firmware, running profile, the control
// room it is bound to, the result of the last contact, the config version and
// the last event worth telling the owner (RF-36/42: a denied adoption, a
// rejected config, an error from the previous boot). The event also travels in
// every poll, so the owner sees it off-LAN too.
// mDNS task and allocations live in PSRAM (sdkconfig: MDNS_*_SPIRAM).
#include "sebastian_fleet.h"

#include <string.h>

#include "esp_app_desc.h"
#include "esp_log.h"
#include "mdns.h"

static const char *TAG = "announce";
static bool started;
static char last_event[64];

#define SERVICE "_sebastian"
#define PROTO "_tcp"

const char *sebastian_fw_version(void) {
    return esp_app_get_description()->version;
}

void sebastian_announce_start(const char *id, const char *prof, const char *cr) {
    if (started) return;
    esp_err_t err = mdns_init();
    if (err != ESP_OK) {
        ESP_LOGE(TAG, "mdns_init: %s — unit will not be discoverable", esp_err_to_name(err));
        return;
    }
    char host[32];
    snprintf(host, sizeof(host), "sebastian-%s", id);
    mdns_hostname_set(host);
    mdns_instance_name_set(host);

    if (!sebastian_take_last_error(last_event, sizeof(last_event))) last_event[0] = 0;
    char cfg[24] = {0};
    sebastian_get_cfg_version(cfg, sizeof(cfg));

    mdns_txt_item_t txt[] = {
        {"id", id},
        {"fw", sebastian_fw_version()},
        {"prof", prof ? prof : ""},
        {"cr", cr ? cr : ""},
        {"err", ""},
        {"ev", last_event},
        {"cfg", cfg},
    };
    err = mdns_service_add(host, SERVICE, PROTO, SEBASTIAN_ADOPT_PORT, txt, sizeof(txt) / sizeof(txt[0]));
    if (err != ESP_OK) {
        ESP_LOGE(TAG, "mdns_service_add: %s", esp_err_to_name(err));
        return;
    }
    started = true;
    ESP_LOGI(TAG, "announcing %s.%s.%s.local fw=%s prof=%s cr=%s ev=%s", host, SERVICE, PROTO,
             sebastian_fw_version(), prof ? prof : "", cr ? cr : "", last_event);
}

void sebastian_announce_set(const char *key, const char *value) {
    if (!started) return;
    mdns_service_txt_item_set(SERVICE, PROTO, key, value ? value : "");
}

void sebastian_announce_event(const char *event) {
    snprintf(last_event, sizeof(last_event), "%s", event ? event : "");
    sebastian_announce_set("ev", last_event);
}

const char *sebastian_announce_last_event(void) { return last_event; }
