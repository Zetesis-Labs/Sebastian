import type { DeviceConfig, OperatingMode } from "./config";

// Single source of truth for the config form: every field's label, help text,
// whether the firmware applies it today, and which mode it belongs to. The form
// (ConfigForm) is rendered from this — no hand-wired inputs.

export type FieldType = "text" | "password" | "url" | "number" | "toggle" | "enum";

export interface FieldMeta {
  path: string; // dot path into DeviceConfig, e.g. "audio.fixedBeamAzimuthDeg"
  label: string;
  help: string;
  type: FieldType;
  applied?: boolean; // firmware stores + applies it today (NVS badge)
  advanced?: boolean; // hidden until "Show advanced" is on
  options?: { value: string; label: string }[];
  unit?: string;
  min?: number;
  max?: number;
  placeholder?: string;
  issuePath?: string; // where to look up a validation issue (defaults to path)
}

export interface Lock {
  label: string;
  value: string;
  why: string;
}

export interface ModeMeta {
  id: OperatingMode;
  pill: string;
  title: string;
  sub: string;
  blurb: string;
  best: string;
  callout?: { tone: "info" | "warn"; text: string };
  locks: Lock[];
  fields: FieldMeta[];
}

export type SharedIcon = "wifi" | "cloud" | "chart";
export interface SharedGroup {
  id: string;
  title: string;
  sub: string;
  icon: SharedIcon;
  fields: FieldMeta[];
}

// ── read/write a config value by dot path (paths are always section.key) ──────
export function getField(config: DeviceConfig, path: string): unknown {
  const [section, key] = path.split(".") as [keyof DeviceConfig, string];
  return (config[section] as Record<string, unknown>)[key];
}

export function setField(config: DeviceConfig, path: string, value: unknown): DeviceConfig {
  const [section, key] = path.split(".") as [keyof DeviceConfig, string];
  return { ...config, [section]: { ...(config[section] as object), [key]: value } };
}

// ── reused field definitions ──────────────────────────────────────────────────
const micChannel: FieldMeta = {
  path: "audio.micChannel",
  label: "Microphone channel",
  type: "enum",
  advanced: true,
  options: [
    { value: "right", label: "Raw ASR (recommended)" },
    { value: "left", label: "Comms (on-chip NS)" },
  ],
  help:
    "Which XVF3800 output is used. “Raw ASR” = clean beam with no on-chip noise suppression (let the agent do the single NS pass). “Comms” = de-reverb + NS + residual-echo suppression done on-chip, meant for full-duplex calls.",
};

const sessionFields: FieldMeta[] = [
  {
    path: "session.silenceTimeoutMs",
    label: "Silence to close",
    type: "number",
    unit: "ms",
    min: 1000,
    max: 120000,
    advanced: true,
    help:
      "How long it waits in silence before ending the session and going back to listening for the wake word. Higher tolerates long pauses; lower closes sooner.",
  },
  {
    path: "session.voiceLevel",
    label: "Voice threshold",
    type: "number",
    min: 0,
    max: 32767,
    advanced: true,
    help:
      "Audio level (0–32767) above which it counts as you speaking, so your turn isn't cut short. Raise it in noisy rooms.",
  },
];

// ── the three operating modes ─────────────────────────────────────────────────
export const MODES: ModeMeta[] = [
  {
    id: "full_duplex",
    pill: "Default",
    title: "Full-duplex",
    sub: "Natural conversation",
    blurb: "The mic stays open while it speaks: you cut in just by talking, no wake word.",
    best: "Quiet room · speaker in a fixed spot",
    callout: {
      tone: "info",
      text: "Relies on the AEC cancelling the speaker's echo. If your room is echoey or reverberant, use Half-duplex.",
    },
    locks: [
      {
        label: "Mic beam",
        value: "Fixed",
        why: "The echo canceller (AEC) only converges with a fixed beam. Without it, the agent would hear itself.",
      },
    ],
    fields: [
      {
        path: "audio.fixedBeamAzimuthDeg",
        label: "Speaker direction",
        type: "number",
        unit: "°",
        min: -180,
        max: 180,
        applied: true,
        help:
          "Where the fixed beam points. 0° = straight ahead of the array; positive = counter-clockwise. Aim it at where you sit: it improves pickup and helps the AEC converge.",
      },
      micChannel,
      {
        path: "audio.probeAecOnBoot",
        label: "AEC self-test on boot",
        type: "toggle",
        advanced: true,
        applied: true,
        help:
          "Plays a tone at boot and checks whether the AEC converges, with no human needed. Handy when installing; beeps ~10 s. Leave off in production.",
      },
      ...sessionFields,
    ],
  },
  {
    id: "half_duplex",
    pill: "Robust",
    title: "Half-duplex",
    sub: "Turn-taking",
    blurb:
      "You talk in turns; to interrupt while it speaks you say “Sebastián”. Far more immune to echo, noise and flaky networks.",
    best: "Echoey/noisy rooms · congested 2.4 GHz",
    callout: {
      tone: "info",
      text: "The recommended mode when there's echo or the device's link is weak: it kills the “hears-itself” loop.",
    },
    locks: [
      {
        label: "Mic beam",
        value: "Adaptive",
        why: "Tracks the speaker wherever they are (no need to aim it). Echo is handled by turn-taking + wake-word barge-in.",
      },
      {
        label: "Interrupt",
        value: "Wake word",
        why: "Since the mic is muted while the agent speaks, you cut in by saying “Sebastián” (barge-in).",
      },
    ],
    fields: [micChannel, ...sessionFields],
  },
  {
    id: "diagnostics",
    pill: "Setup only",
    title: "Diagnostics",
    sub: "Bring-up",
    blurb:
      "Runs the audio self-tests at boot and reports the results to Grafana. For validating a freshly assembled unit, not for daily use.",
    best: "Validate hardware before deploying",
    callout: {
      tone: "warn",
      text: "Plays tones/noise ~10–30 s on every boot. Don't leave this in production.",
    },
    locks: [
      {
        label: "Use",
        value: "Temporary",
        why: "Once the unit passes, re-provision it to Full- or Half-duplex for production.",
      },
    ],
    fields: [
      {
        path: "audio.probeAecOnBoot",
        label: "AEC convergence test",
        type: "toggle",
        applied: true,
        help: "Plays a tone and measures whether the AEC converges. Result lands in Grafana after the reset.",
      },
      {
        path: "audio.probeDualChannelOnBoot",
        label: "Dual-channel test (ASR vs Comms)",
        type: "toggle",
        applied: true,
        help:
          "Compares residual echo on the Comms (left) beam vs the raw ASR (right) beam. Answers whether Comms enables full-duplex with tracking.",
      },
      {
        path: "audio.probeOutputGainOnBoot",
        label: "Output-gain test",
        type: "toggle",
        applied: true,
        help:
          "Plays noise at 0 dB vs −12 dB and compares pre-AEC echo. Answers whether FAR_EXTGAIN works as a master volume for echo headroom.",
      },
    ],
  },
];

// ── sections shown in every mode ──────────────────────────────────────────────
export const SHARED: SharedGroup[] = [
  {
    id: "net",
    icon: "wifi",
    title: "WiFi network",
    sub: "2.4 GHz",
    fields: [
      {
        path: "wifi.ssid",
        label: "Network name (SSID)",
        type: "text",
        applied: true,
        placeholder: "MyNetwork",
        issuePath: "wifi.ssid",
        help: "The WiFi network the device joins. The ESP32-S3 is 2.4 GHz only.",
      },
      {
        path: "wifi.password",
        label: "Password",
        type: "password",
        applied: true,
        help: "Stored in the device's NVS, never leaves it. Leave empty only for an open network.",
      },
      {
        path: "wifi.hidden",
        label: "Hidden network",
        type: "toggle",
        advanced: true,
        help: "Turn on if the SSID isn't broadcast. The device does a directed scan.",
      },
    ],
  },
  {
    id: "lk",
    icon: "cloud",
    title: "LiveKit",
    sub: "token + room",
    fields: [
      {
        path: "livekit.tokenServerUrl",
        label: "Token server URL",
        type: "url",
        applied: true,
        placeholder: "http://192.168.1.10:8787/token",
        issuePath: "livekit.tokenServerUrl",
        help: "Where the device fetches its per-session token, on the LAN. Must point at the machine running token_server.py.",
      },
      {
        path: "livekit.deviceIdentity",
        label: "Device identity",
        type: "text",
        advanced: true,
        help: "The name the device joins the room with. The agent waits for it to greet.",
      },
      {
        path: "livekit.room",
        label: "Room",
        type: "text",
        advanced: true,
        help: "The LiveKit room where device and agent meet.",
      },
      {
        path: "livekit.agentName",
        label: "Agent",
        type: "text",
        advanced: true,
        help: "The agent worker to dispatch when the device joins.",
      },
    ],
  },
  {
    id: "tel",
    icon: "chart",
    title: "Telemetry",
    sub: "optional",
    fields: [
      {
        path: "telemetry.otlpEndpoint",
        label: "OTLP endpoint",
        type: "url",
        advanced: true,
        placeholder: "https://otel.example.com",
        issuePath: "telemetry.otlpEndpoint",
        help: "Where the telemetry bridge ships metrics/logs. Empty = disabled.",
      },
      {
        path: "telemetry.grafanaUrl",
        label: "Grafana URL",
        type: "url",
        advanced: true,
        placeholder: "https://grafana.example.com/d/sebastian-device",
        issuePath: "telemetry.grafanaUrl",
        help: "Informational only, to link to the device dashboard.",
      },
    ],
  },
];
