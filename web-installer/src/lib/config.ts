export const CONFIG_SCHEMA = "sebastian.config.v1";

export interface DeviceConfig {
  schema: typeof CONFIG_SCHEMA;
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
    probeAecOnBoot: boolean;
  };
  session: { silenceTimeoutMs: number; voiceLevel: number };
}

export const defaultConfig = (): DeviceConfig => ({
  schema: CONFIG_SCHEMA,
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
    probeAecOnBoot: false,
  },
  session: { silenceTimeoutMs: 12000, voiceLevel: 3000 },
});

// Fields the current firmware actually stores in NVS + applies. The rest are
// carried in the contract for forward-compat but ignored on-device for now.
export const APPLIED_FIELDS = ["wifi.ssid", "wifi.password", "livekit.tokenServerUrl"];

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
    wifi: section("wifi", base.wifi),
    livekit: section("livekit", base.livekit),
    telemetry: section("telemetry", base.telemetry),
    audio: section("audio", base.audio),
    session: section("session", base.session),
  };
}
