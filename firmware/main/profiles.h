// Provisioning profiles: named capability bundles stored in NVS.
//
// A profile composes what the device IS after boot: which app runs (agent /
// USB mic) plus optional overrides of the audio config keys config.zig loads.
// Unset fields (-1 / INT32_MIN) inherit whatever the legacy NVS keys or the
// compiled defaults say, so a unit provisioned before profiles existed keeps
// its behaviour under the built-in "agente" profile.
//
// C shim for the same reason as provisioning.c: NVS + cJSON are far easier
// here than through hand-written Zig bindings. profile.zig binds these.
#pragma once

#include <stdbool.h>
#include <stdint.h>

#define SEBASTIAN_PROFILE_NAME_MAX 24
#define SEBASTIAN_PROFILES_MAX 6

#define SEBASTIAN_PROFILE_MODE_AGENT 0
#define SEBASTIAN_PROFILE_MODE_USB_MIC 1

typedef struct {
    char name[SEBASTIAN_PROFILE_NAME_MAX];
    uint8_t mode;       // SEBASTIAN_PROFILE_MODE_*
    int8_t full_duplex; // -1 inherit, 0/1 override config.full_duplex
    int8_t fixed_beam;  // -1 inherit, 0/1 override config.fixed_beam
    int32_t beam_az;    // INT32_MIN inherit, else degrees
    int8_t wifi;        // reserved (phase 3: WiFi+telemetry in usb_mic mode)
    int8_t telemetry;   // reserved (phase 3)
} sebastian_profile_t;

// Fill `out` from the NVS `profiles` JSON array; built-ins ("agente",
// "micro-usb") when the key is absent, unparseable or empty. Returns count.
int sebastian_profiles_load(sebastian_profile_t *out, int max);

// Index of the NVS `active_prof` name inside `list`, or 0 when absent/unmatched.
int sebastian_profiles_active_index(const sebastian_profile_t *list, int count);

// Persist `name` as the active profile. False if it names no loaded profile.
bool sebastian_profiles_set_active(const char *name);

// Boot-failure guard: note_start increments and persists a counter and returns
// the new value; note_ok resets it once the selected mode is up. Three straight
// failed boots ⇒ the caller falls back to an agent profile (always
// administrable) instead of boot-looping in a broken one.
int32_t sebastian_boot_note_start(void);
void sebastian_boot_note_ok(void);
