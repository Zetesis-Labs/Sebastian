#!/usr/bin/env python3
"""Export the repo's current install-time settings as sebastian.config.v1 JSON."""

from __future__ import annotations

import argparse
import ast
import json
import re
from pathlib import Path
from typing import Any

ROOT = Path(__file__).resolve().parents[1]
SCHEMA = "sebastian.config.v1"


def read_text(path: str) -> str:
    return (ROOT / path).read_text(encoding="utf-8")


def parse_sdkconfig_value(text: str, key: str, default: str = "") -> str:
    match = re.search(rf"^{re.escape(key)}=(.*)$", text, re.MULTILINE)
    if not match:
        return default
    raw = match.group(1).strip()
    if raw.startswith('"') and raw.endswith('"'):
        return ast.literal_eval(raw)
    return raw


def parse_zig_string(text: str, name: str, default: str = "") -> str:
    match = re.search(rf'pub const {re.escape(name)}\s*=\s*(".*?");', text)
    return ast.literal_eval(match.group(1)) if match else default


def parse_zig_bool(text: str, name: str, default: bool = False) -> bool:
    match = re.search(rf"pub const {re.escape(name)}:[^=]*=\s*(true|false);", text)
    return (match.group(1) == "true") if match else default


def parse_zig_float(text: str, name: str, default: float = 0.0) -> float:
    match = re.search(rf"pub const {re.escape(name)}:[^=]*=\s*(-?\d+(?:\.\d+)?);", text)
    return float(match.group(1)) if match else default


def parse_zig_enum(text: str, name: str, default: str = "") -> str:
    match = re.search(rf"pub const {re.escape(name)}:[^=]*=\s*\.([a-zA-Z0-9_]+);", text)
    return match.group(1) if match else default


def parse_py_constant(text: str, name: str, default: Any = None) -> Any:
    tree = ast.parse(text)
    for node in tree.body:
        if not isinstance(node, ast.Assign):
            continue
        if not any(isinstance(target, ast.Name) and target.id == name for target in node.targets):
            continue
        try:
            return ast.literal_eval(node.value)
        except ValueError:
            return default
    return default


def parse_const_u32(text: str, name: str, default: int, constants: dict[str, int] | None = None) -> int:
    match = re.search(rf"const {re.escape(name)}:[^=]*=\s*([^;]+);", text)
    if not match:
        return default
    expression = match.group(1).strip()
    return int(eval(expression, {"__builtins__": {}}, constants or {}))


def build_config() -> dict[str, Any]:
    sdkconfig = read_text("firmware/sdkconfig")
    secrets = read_text("firmware/main/secrets.zig")
    firmware_config = read_text("firmware/main/config.zig")
    app = read_text("firmware/main/app.zig")
    token_server = read_text("agent/token_server.py")
    bridge = read_text("tools/telemetry/bridge.py")

    otlp = parse_py_constant(bridge, "OTLP", "http://localhost:4318")

    session_tick_ms = parse_const_u32(app, "SESSION_TICK_MS", 10)
    session_silence_ticks = parse_const_u32(
        app,
        "SESSION_SILENCE_TICKS",
        1200,
        {"SESSION_TICK_MS": session_tick_ms},
    )

    return {
        "schema": SCHEMA,
        "wifi": {
            "ssid": parse_sdkconfig_value(sdkconfig, "CONFIG_LK_EXAMPLE_WIFI_SSID"),
            "password": parse_sdkconfig_value(sdkconfig, "CONFIG_LK_EXAMPLE_WIFI_PASSWORD"),
            "hidden": False,
        },
        "livekit": {
            "tokenServerUrl": parse_zig_string(secrets, "token_server_url"),
            "deviceIdentity": parse_py_constant(token_server, "IDENTITY", "esp32-respeaker"),
            "room": parse_py_constant(token_server, "ROOM", "sebastian"),
            "agentName": parse_py_constant(token_server, "AGENT_NAME", "sebastian"),
        },
        "telemetry": {
            "otlpEndpoint": otlp,
            "grafanaUrl": "http://localhost:3000/d/sebastian-device",
        },
        "audio": {
            "micChannel": parse_zig_enum(firmware_config, "mic_channel", "right"),
            "fixedBeam": parse_zig_bool(firmware_config, "fixed_beam", True),
            "fixedBeamAzimuthDeg": parse_zig_float(firmware_config, "fixed_beam_azimuth_deg", 0.0),
            "fullDuplex": parse_zig_bool(firmware_config, "full_duplex", True),
            "probeAecOnBoot": parse_zig_bool(firmware_config, "probe_aec_on_boot", False),
        },
        "session": {
            "silenceTimeoutMs": session_silence_ticks * session_tick_ms,
            "voiceLevel": parse_const_u32(app, "SESSION_VOICE_LEVEL", 3000),
        },
    }


def redact(config: dict[str, Any]) -> dict[str, Any]:
    clone = json.loads(json.dumps(config))
    if clone["wifi"]["password"]:
        clone["wifi"]["password"] = "<redacted>"
    return clone


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--out", type=Path, help="Write JSON to this path instead of stdout.")
    parser.add_argument("--redact-secrets", action="store_true", help="Redact WiFi password.")
    args = parser.parse_args()

    config = build_config()
    if args.redact_secrets:
        config = redact(config)
    payload = json.dumps(config, indent=2, ensure_ascii=False) + "\n"

    if args.out:
        output = args.out if args.out.is_absolute() else ROOT / args.out
        output.write_text(payload, encoding="utf-8")
        print(output)
        return

    print(payload, end="")


if __name__ == "__main__":
    main()
