export const CONFIG_SCHEMA = "sebastian.config.v1";

// Operating modes are the top-level, mutually-exclusive choice. Each mode locks
// the interdependent audio settings (duplex + beam) so the operator can't pick a
// combination that breaks echo cancellation; everything else stays tunable.
export type OperatingMode = "full_duplex" | "half_duplex";

export const OPERATING_MODES: OperatingMode[] = ["full_duplex", "half_duplex"];

export interface DeviceConfig {
  schema: typeof CONFIG_SCHEMA;
  mode: OperatingMode;
  wifi: { ssid: string; password: string; hidden: boolean };
  livekit: {
    tokenServerUrl: string;
    deviceIdentity: string;
    room: string;
    agentName: string;
  };
  telemetry: { syslogIp: string; syslogPort: number; otlpEndpoint: string; grafanaUrl: string };
  audio: {
    micChannel: "right" | "left";
    fixedBeam: boolean;
    fixedBeamAzimuthDeg: number;
    fullDuplex: boolean;
  };
  session: { silenceTimeoutMs: number; voiceLevel: number };
  adoption?: { orgSecret: string; deviceSecret?: string };
}

// The audio settings each mode fixes. Selecting a mode merges these over the
// current audio config; the mode's own form then only exposes what's left to tune.
export const MODE_PRESETS: Record<OperatingMode, Partial<DeviceConfig["audio"]>> = {
  // Full-duplex needs a fixed beam for the AEC to converge.
  full_duplex: { fullDuplex: true, fixedBeam: true },
  // Half-duplex gates the mic while the agent speaks; an adaptive beam tracks the talker.
  half_duplex: { fullDuplex: false, fixedBeam: false },
};

const isMode = (v: unknown): v is OperatingMode =>
  typeof v === "string" && (OPERATING_MODES as string[]).includes(v);

// Switch modes: set the mode and merge its locked audio settings.
export function applyMode(config: DeviceConfig, mode: OperatingMode): DeviceConfig {
  return { ...config, mode, audio: { ...config.audio, ...MODE_PRESETS[mode] } };
}

export const defaultConfig = (): DeviceConfig => ({
  schema: CONFIG_SCHEMA,
  mode: "full_duplex",
  wifi: { ssid: "", password: "", hidden: false },
  livekit: {
    tokenServerUrl: "",
    deviceIdentity: "esp32-respeaker",
    room: "sebastian",
    agentName: "sebastian",
  },
  telemetry: { syslogIp: "", syslogPort: 514, otlpEndpoint: "", grafanaUrl: "" },
  audio: {
    micChannel: "right",
    fixedBeam: true,
    fixedBeamAzimuthDeg: 0,
    fullDuplex: true,
  },
  session: { silenceTimeoutMs: 12000, voiceLevel: 3000 },
});

// Fields the firmware stores in NVS + applies at boot. `mode` expands into
// audio.fullDuplex/fixedBeam (applied), plus the beam azimuth. Still compile-time
// (reflash), so NOT applied: audio.micChannel and session.* — see PROVISIONING.md.
export const APPLIED_FIELDS = [
  "mode",
  "wifi.ssid",
  "wifi.password",
  "livekit.tokenServerUrl",
  "telemetry.syslogIp",
  "telemetry.syslogPort",
  "session.silenceTimeoutMs",
  "session.voiceLevel",
  "adoption.orgSecret",
  "audio.fullDuplex",
  "audio.fixedBeam",
  "audio.fixedBeamAzimuthDeg",
];

export const serialize = (config: DeviceConfig): string => JSON.stringify(config, null, 2);

// What the firmware answers to `sebastian.config.get`: the stored config in the
// v1 shape (only the keys present in NVS) plus two facts the form needs. The
// WiFi password never leaves the device; `passwordSet` says whether there is one.
export interface DeviceDump {
  provisioned: boolean;
  passwordSet: boolean;
  config: DeviceConfig;
}

export function parseDump(input: unknown): DeviceDump {
  const src = (input && typeof input === "object" ? input : {}) as Record<string, unknown>;
  const wifi = (src.wifi && typeof src.wifi === "object" ? src.wifi : {}) as Record<string, unknown>;
  return {
    provisioned: src.provisioned === true,
    passwordSet: wifi.passwordSet === true,
    config: mergeConfig(src),
  };
}

// The line sent to the board. While the loaded password stays locked in the
// form, the key is omitted so the firmware keeps the stored one (an explicit ""
// would turn it into an open network).
export function payloadFor(config: DeviceConfig, keepStoredPassword: boolean): string {
  const out: DeviceConfig = keepStoredPassword
    ? { ...config, wifi: { ssid: config.wifi.ssid, hidden: config.wifi.hidden } as DeviceConfig["wifi"] }
    : config;
  return JSON.stringify(out);
}

// Deep-merge a partial (imported) config onto the defaults so missing keys are
// filled and unknown keys dropped — keeps the form + payload well-formed.
export function mergeConfig(input: unknown): DeviceConfig {
  const base = defaultConfig();
  if (!input || typeof input !== "object") return base;
  const src = input as Record<string, unknown>;
  const section = <T extends object>(key: keyof DeviceConfig, fallback: T): T => {
    const value = src[key as string];
    return value && typeof value === "object" ? { ...fallback, ...(value as object) } : fallback;
  };
  return {
    schema: CONFIG_SCHEMA,
    mode: isMode(src.mode) ? src.mode : base.mode,
    wifi: section("wifi", base.wifi),
    livekit: section("livekit", base.livekit),
    telemetry: section("telemetry", base.telemetry),
    audio: section("audio", base.audio),
    session: section("session", base.session),
    ...adoptionOf(src.adoption),
  };
}

// Only the secrets themselves travel back to the board; the dump's *Set flags
// stay out of the document.
function adoptionOf(value: unknown): Pick<DeviceConfig, "adoption"> {
  if (!value || typeof value !== "object") return {};
  const src = value as Record<string, unknown>;
  const adoption: NonNullable<DeviceConfig["adoption"]> = { orgSecret: typeof src.orgSecret === "string" ? src.orgSecret : "" };
  if (typeof src.deviceSecret === "string" && src.deviceSecret) adoption.deviceSecret = src.deviceSecret;
  if (!adoption.orgSecret && !adoption.deviceSecret) return {};
  return { adoption };
}

// Pre-fill served by a control room's dashboard next to the embedded installer
// (dashboard route /installer/control-room.json). Absent on GitHub Pages.
export interface ControlRoomPrefill {
  name: string;
  tokenServerUrl: string;
  syslogIp: string;
  syslogPort: number;
  orgSecret: string;
}

export function applyPrefill(config: DeviceConfig, room: ControlRoomPrefill): DeviceConfig {
  return {
    ...config,
    livekit: { ...config.livekit, tokenServerUrl: room.tokenServerUrl },
    telemetry: { ...config.telemetry, syslogIp: room.syslogIp, syslogPort: room.syslogPort || 514 },
    ...(room.orgSecret ? { adoption: { orgSecret: room.orgSecret } } : {}),
  };
}
