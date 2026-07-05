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
  telemetry: { otlpEndpoint: string; grafanaUrl: string };
  audio: {
    micChannel: "right" | "left";
    fixedBeam: boolean;
    fixedBeamAzimuthDeg: number;
    fullDuplex: boolean;
  };
  session: { silenceTimeoutMs: number; voiceLevel: number };
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
  telemetry: { otlpEndpoint: "", grafanaUrl: "" },
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
  "audio.fullDuplex",
  "audio.fixedBeam",
  "audio.fixedBeamAzimuthDeg",
];

export const serialize = (config: DeviceConfig): string => JSON.stringify(config, null, 2);

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
  };
}
