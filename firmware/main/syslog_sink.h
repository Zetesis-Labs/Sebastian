#pragma once

// Start mirroring the device's ESP_LOG output to a remote UDP syslog server.
// The Sebastian device's only physical link in production is power + WiFi — no
// USB host reads its serial — so its firmware logs would otherwise be lost. This
// redirects esp_log to a syslog receiver on the LAN (which forwards to Loki),
// while keeping UART output intact.
//
// Config lives in NVS (namespace "sebastian", provisioned over serial):
//   syslog_ip   — receiver IPv4. Empty/absent ⇒ sink disabled (no-op).
//   syslog_port — UDP port (default 514).
//
// Call AFTER the network is up (WiFi connected). Safe to call unprovisioned.
void sebastian_syslog_start(void);
