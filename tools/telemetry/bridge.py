# /// script
# requires-python = ">=3.11"
# dependencies = [
#   "pyserial>=3.5",
#   "opentelemetry-sdk>=1.25",
#   "opentelemetry-exporter-otlp-proto-http>=1.25",
# ]
# ///
"""Sebastian telemetry bridge: device serial → OpenTelemetry (metrics + logs).

The device's richest, most reliable telemetry channel is the serial port — it
keeps working precisely when WiFi/LiveKit are broken, which is when we need it
most. This bridge tails the serial, parses the firmware's log lines into OTel
metrics, and ships EVERY line as an OTel log record, both via OTLP/HTTP to a
local LGTM stack (grafana/otel-lgtm: collector + Prometheus + Loki + Grafana).

Run (deps auto-resolved by uv):
    uv run tools/telemetry/bridge.py

Grafana:  http://localhost:3000  (admin/admin)
  - Metrics: datasource Prometheus, names sebastian_*
  - Logs:    datasource Loki, {service_name="sebastian-device"}

Notes:
  - Never touches DTR/RTS — attaching never resets the device.
  - Reconnects if the port re-enumerates (flash, replug).
  - Only ONE process can hold the port: stop ad-hoc readers first.
"""

import glob
import os
import re
import time

import serial
from opentelemetry import metrics
from opentelemetry._logs import set_logger_provider, get_logger
from opentelemetry.exporter.otlp.proto.http._log_exporter import OTLPLogExporter
from opentelemetry.exporter.otlp.proto.http.metric_exporter import OTLPMetricExporter
from opentelemetry.sdk._logs import LoggerProvider, LoggingHandler
from opentelemetry.sdk._logs.export import BatchLogRecordProcessor
from opentelemetry.sdk.metrics import MeterProvider
from opentelemetry.sdk.metrics.export import PeriodicExportingMetricReader
from opentelemetry.sdk.resources import Resource

OTLP = "http://localhost:4318"
RESOURCE = Resource.create({"service.name": "sebastian-device"})

reader = PeriodicExportingMetricReader(
    OTLPMetricExporter(endpoint=f"{OTLP}/v1/metrics"), export_interval_millis=5000
)
metrics.set_meter_provider(MeterProvider(resource=RESOURCE, metric_readers=[reader]))
meter = metrics.get_meter("sebastian.bridge")

logger_provider = LoggerProvider(resource=RESOURCE)
logger_provider.add_log_record_processor(
    BatchLogRecordProcessor(OTLPLogExporter(endpoint=f"{OTLP}/v1/logs"))
)
set_logger_provider(logger_provider)
otel_logger = get_logger("sebastian.serial")

import logging

log_handler = LoggingHandler(logger_provider=logger_provider)
pylog = logging.getLogger("sebastian.serial")
pylog.addHandler(log_handler)
pylog.setLevel(logging.DEBUG)

# ── Gauges (last-value semantics via callbacks) ───────────────────────────────
last = {
    "pcm_peak": 0, "wake_prob_max": 0, "feed_max_us": 0, "gap_max_us": 0,
    "pcm_dc": 0, "aec_converged": 0, "agent_state": 0, "session_active": 0,
    "muted": 0, "uptime_ms": 0,
    "aec_path_change": 0, "aec_rt60_ms": 0, "aec_idle": 0, "aec_far_extgain": 0.0,
    "aec_ref_gain": 0.0, "aec_i2s_inactive": 0, "echo_gated_peak": 0,
    "echo_live": 0, "render_peak": 0, "keepalive": 0,
}
# Last in-session converged read, counted at session close (reset at open).
session_converged = -1
# Heartbeat: frozen gauges look healthy — these distinguish "alive and quiet"
# from "dead/unplugged". serial_age grows monotonically when the device stops
# talking; attached drops to 0 when the port is gone.
last_line_ts: float = 0.0
serial_attached: int = 0

def _gauge(name, key, desc):
    meter.create_observable_gauge(
        name, [lambda opts, k=key: [metrics.Observation(last[k])]], description=desc
    )

_gauge("sebastian_pcm_peak", "pcm_peak", "Wake path 5s-window PCM peak (0..32767)")
_gauge("sebastian_wake_prob_max", "wake_prob_max", "Wake model max probability per 5s window (%)")
_gauge("sebastian_feed_max_us", "feed_max_us", "Slowest mww_feed per window (us)")
_gauge("sebastian_gap_max_us", "gap_max_us", "Longest i2s read gap per window (us)")
_gauge("sebastian_pcm_dc", "pcm_dc", "Wake path DC mean per 5s window")
_gauge("sebastian_aec_converged", "aec_converged", "AEC converged flag (0/1; XMOS latches it for life)")
_gauge("sebastian_aec_path_change", "aec_path_change", "AEC path-change flag (1 = output suppressed, re-adapting)")
_gauge("sebastian_aec_rt60_ms", "aec_rt60_ms", "AEC room reverb estimate (ms; needs a converged AEC)")
_gauge("sebastian_aec_idle_ms", "aec_idle", "AEC ms since last far-end — high + converged=0 ⇒ idle auto-reset")
_gauge("sebastian_aec_far_extgain", "aec_far_extgain", "AEC far-end external gain (dB; should stay at the applied value)")
_gauge("sebastian_aec_ref_gain", "aec_ref_gain", "Audio-mgr reference gain (SUSPECT #1: 8.0 clips the reference at full-scale)")
_gauge("sebastian_aec_i2s_inactive", "aec_i2s_inactive", "XVF I2S-inactive flag")
_gauge("sebastian_echo_gated_peak", "echo_gated_peak", "Half-duplex residual echo peak while the agent speaks (0 in full-duplex)")
_gauge("sebastian_echo_live", "echo_live", "Full-duplex residual echo: mic level while the agent speaks (high = AEC leaking)")
_gauge("sebastian_render_peak", "render_peak", "Speaker output peak while the agent speaks (keepalive signal)")
_gauge("sebastian_keepalive", "keepalive", "1 while the render-based keepalive holds the session open")
_gauge("sebastian_agent_state", "agent_state", "Agent state (0=idle 1=listening 2=thinking 3=speaking)")
_gauge("sebastian_session_active", "session_active", "1 while a LiveKit session is open")
_gauge("sebastian_muted", "muted", "1 while the XVF mute button is engaged (I2S streams zeros)")
meter.create_observable_gauge(
    "sebastian_device_uptime_seconds",
    [lambda opts: [metrics.Observation(last["uptime_ms"] / 1000)]],
    description="Device uptime from IDF log timestamps — a backwards jump = reboot",
)
meter.create_observable_gauge(
    "sebastian_serial_age_seconds",
    [lambda opts: [metrics.Observation(time.time() - last_line_ts if last_line_ts else -1)]],
    description="Seconds since the last serial line — grows when the device goes silent/dead",
)
meter.create_observable_gauge(
    "sebastian_serial_attached",
    [lambda opts: [metrics.Observation(serial_attached)]],
    description="1 while the bridge holds the serial port",
)

# ── Counters ──────────────────────────────────────────────────────────────────
c_wake = meter.create_counter("sebastian_wake_detections_total")
c_spike = meter.create_counter("sebastian_prob_spikes_total")
c_session_open = meter.create_counter("sebastian_sessions_opened_total")
c_session_close = meter.create_counter("sebastian_sessions_closed_total")
c_sctp = meter.create_counter("sebastian_sctp_init_total", description="SCTP INIT retries — orphaned session indicator")
c_reboot = meter.create_counter("sebastian_reboots_total")
c_panic = meter.create_counter("sebastian_panics_total")
c_heal = meter.create_counter("sebastian_channel_heals_total", description="I2S self-heal resyncs")
c_wdg = meter.create_counter("sebastian_watchdog_fires_total")
c_preroll = meter.create_counter("sebastian_preroll_sent_total")
c_barge = meter.create_counter("sebastian_barge_ins_total", description="Wake-word interrupts over agent speech")
c_aec_sessions = meter.create_counter("sebastian_aec_sessions_total", description="Sessions by whether the AEC converged (last in-session read)")
c_err = meter.create_counter("sebastian_error_lines_total")

AGENT_STATES = {"idle": 0, "listening": 1, "thinking": 2, "speaking": 3}

RE_WINDOW = re.compile(r"5s window: pcm peak=(\d+) max prob=(\d+)% feed_max=(\d+)us gap_max=(\d+)us(?: dc=(-?\d+))?")
RE_SPIKE = re.compile(r"prob spike: (\d+)%")
RE_AEC = re.compile(r"xvf_aec: state: converged=(-?\d+)")
# Extended fields (new firmware). Kept separate so `converged` still parses off
# old firmware during a bridge-ahead-of-flash transition.
RE_AEC_FULL = re.compile(
    r"path_change=(-?\d+) rt60=(-?\d+)ms idle=(-?\d+) "
    r"far_extgain=(-?\d+)m ref_gain=(-?\d+)m i2s_inactive=(\d+)"
)
RE_ECHO = re.compile(
    r"echo: gated_peak=(?P<gated>\d+)"
    r"(?: live_echo=(?P<live>\d+))?"
    r"(?: render_peak=(?P<render>\d+))?"
    r"(?: keepalive=(?P<ka>\w+))?"
)
RE_AGENT = re.compile(r"agent state: (\w+)")
RE_CLOSE_REASON = re.compile(r"(silence timeout|max duration|disconnected early)")
RE_MUTE = re.compile(r"xvf_ui: mute: (on|off)")
RE_MUTE_BOOT = re.compile(r"mute readback: (MUTED|UNMUTED)")
RE_IDF_TS = re.compile(r"^[IWE] \((\d+)\)")

# The firmware logs the close REASON one line before "session closed" — hold it
# so the close counter gets a real label instead of "unknown".
pending_close_reason = "unknown"

def handle(line: str) -> None:
    global pending_close_reason, session_converged
    if m := RE_IDF_TS.match(line):
        last["uptime_ms"] = int(m.group(1))

    if m := RE_WINDOW.search(line):
        last["pcm_peak"] = int(m.group(1))
        last["wake_prob_max"] = int(m.group(2))
        last["feed_max_us"] = int(m.group(3))
        last["gap_max_us"] = int(m.group(4))
        if m.group(5) is not None:
            last["pcm_dc"] = int(m.group(5))
    elif m := RE_SPIKE.search(line):
        c_spike.add(1, {"pct": str((int(m.group(1)) // 10) * 10)})
    elif "WAKE WORD DETECTED" in line:
        c_wake.add(1)
    elif "session open" in line:
        c_session_open.add(1)
        last["session_active"] = 1
        session_converged = -1
    elif "session closed" in line:
        last["session_active"] = 0
        c_session_close.add(1, {"reason": pending_close_reason})
        c_aec_sessions.add(1, {"converged": "1" if session_converged == 1 else "0"})
        pending_close_reason = "unknown"
    elif m := RE_CLOSE_REASON.search(line):
        pending_close_reason = m.group(1).replace(" ", "_")
    elif m := RE_ECHO.search(line):
        last["echo_gated_peak"] = int(m.group("gated"))
        if m.group("live") is not None:
            last["echo_live"] = int(m.group("live"))
        if m.group("render") is not None:
            last["render_peak"] = int(m.group("render"))
        if m.group("ka") is not None:
            last["keepalive"] = 1 if m.group("ka") == "true" else 0
    elif m := RE_AEC.search(line):
        conv = int(m.group(1))
        last["aec_converged"] = max(0, conv)
        session_converged = conv
        if f := RE_AEC_FULL.search(line):
            last["aec_path_change"] = int(f.group(1))
            last["aec_rt60_ms"] = int(f.group(2))
            last["aec_idle"] = int(f.group(3))
            last["aec_far_extgain"] = int(f.group(4)) / 1000.0
            last["aec_ref_gain"] = int(f.group(5)) / 1000.0
            last["aec_i2s_inactive"] = int(f.group(6))
    elif m := RE_AGENT.search(line):
        last["agent_state"] = AGENT_STATES.get(m.group(1), 0)
    elif m := RE_MUTE.search(line):
        last["muted"] = 1 if m.group(1) == "on" else 0
    elif m := RE_MUTE_BOOT.search(line):
        last["muted"] = 1 if m.group(1) == "MUTED" else 0
    elif "SCTP: Send INIT" in line:
        c_sctp.add(1)
    elif line.startswith("rst:") or " rst:" in line:
        reason = line.split("rst:")[1].split(" ")[0].rstrip(",")
        c_reboot.add(1, {"reason": reason})
        last["session_active"] = 0
    elif "PANIC" in line or "Guru Meditation" in line:
        c_panic.add(1)
    elif "self-heal" in line:
        c_heal.add(1)
    elif "watchdog expired" in line:
        c_wdg.add(1)
    elif "sent pre-roll" in line:
        c_preroll.add(1)
    elif "barge-in:" in line:
        c_barge.add(1)

    if line.startswith("E (") or "PANIC" in line:
        c_err.add(1)
        pylog.error(line)
    elif line.startswith("W ("):
        pylog.warning(line)
    else:
        pylog.info(line)

def find_port() -> str | None:
    # SEBASTIAN_SERIAL_URL permite correr el bridge dentro del devcontainer
    # contra el serial compartido del host (make serial-share):
    #   SEBASTIAN_SERIAL_URL=rfc2217://<ip-del-mac>:4000 uv run bridge.py
    if url := os.environ.get("SEBASTIAN_SERIAL_URL"):
        return url
    ports = glob.glob("/dev/cu.usbmodem*") or glob.glob("/dev/ttyACM*") or glob.glob("/dev/ttyUSB*")
    return ports[0] if ports else None

def main() -> None:
    global last_line_ts, serial_attached
    print(f"[bridge] OTLP → {OTLP} | Grafana http://localhost:3000")
    while True:
        port = find_port()
        if not port:
            serial_attached = 0
            time.sleep(5)
            continue
        try:
            # Plain open, no DTR/RTS games — never resets the device.
            # serial_for_url acepta tanto /dev/... como rfc2217://host:port.
            s = serial.serial_for_url(port, baudrate=115200, timeout=1)
            serial_attached = 1
            print(f"[bridge] attached to {port}")
            buf = b""
            while True:
                chunk = s.read(4096)
                if not chunk:
                    continue
                buf += chunk
                last_line_ts = time.time()
                while b"\n" in buf:
                    raw, buf = buf.split(b"\n", 1)
                    line = raw.decode("utf8", "replace").strip()
                    if line:
                        handle(line)
        except (serial.SerialException, OSError) as e:
            serial_attached = 0
            print(f"[bridge] detached ({e!r}); retrying in 5s")
            time.sleep(5)

if __name__ == "__main__":
    main()
