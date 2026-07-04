import type { DeviceConfig } from "../lib/config";
import type { FieldIssue } from "../lib/validate";
import { SelectField, TextField, Toggle, cx } from "./ui";

interface Props {
  config: DeviceConfig;
  onChange: (next: DeviceConfig) => void;
  issues: FieldIssue[];
}

function Group({ title, cols = 2, children }: { title: string; cols?: 1 | 2; children: React.ReactNode }) {
  return (
    <fieldset className="border-t border-line pt-5 first:border-t-0 first:pt-0">
      <legend className="mb-3 font-serif text-lg text-fg">{title}</legend>
      <div className={cx("grid gap-4", cols === 2 ? "sm:grid-cols-2" : "")}>{children}</div>
    </fieldset>
  );
}

export function ConfigForm({ config, onChange, issues }: Props) {
  const up = <S extends keyof DeviceConfig, K extends keyof DeviceConfig[S]>(
    section: S,
    key: K,
    value: DeviceConfig[S][K],
  ) => onChange({ ...config, [section]: { ...(config[section] as object), [key]: value } });

  const issue = (path: string) => issues.find((i) => i.path === path);
  const errText = (path: string) => {
    const found = issue(path);
    return found ? (
      <span className={found.severity === "error" ? "text-danger" : "text-warn"}>{found.message}</span>
    ) : undefined;
  };

  return (
    <div className="mt-7 grid gap-7">
      <Group title="Red">
        <TextField
          label="WiFi SSID"
          applied
          autoComplete="off"
          value={config.wifi.ssid}
          onChange={(e) => up("wifi", "ssid", e.target.value)}
          hint={errText("wifi.ssid")}
        />
        <TextField
          label="WiFi password"
          applied
          type="password"
          autoComplete="off"
          value={config.wifi.password}
          onChange={(e) => up("wifi", "password", e.target.value)}
        />
        <div className="sm:col-span-2">
          <Toggle
            label="Red oculta"
            checked={config.wifi.hidden}
            onChange={(v) => up("wifi", "hidden", v)}
          />
        </div>
      </Group>

      <Group title="LiveKit">
        <TextField
          label="Token server URL"
          applied
          type="url"
          inputMode="url"
          placeholder="http://192.168.1.10:8787/token"
          value={config.livekit.tokenServerUrl}
          onChange={(e) => up("livekit", "tokenServerUrl", e.target.value)}
          hint={errText("livekit.tokenServerUrl")}
        />
        <TextField
          label="Device identity"
          value={config.livekit.deviceIdentity}
          onChange={(e) => up("livekit", "deviceIdentity", e.target.value)}
        />
        <TextField
          label="Room"
          value={config.livekit.room}
          onChange={(e) => up("livekit", "room", e.target.value)}
        />
        <TextField
          label="Agent"
          value={config.livekit.agentName}
          onChange={(e) => up("livekit", "agentName", e.target.value)}
        />
      </Group>

      <Group title="Telemetría">
        <TextField
          label="OTLP endpoint"
          type="url"
          inputMode="url"
          placeholder="https://otel.example.com"
          value={config.telemetry.otlpEndpoint}
          onChange={(e) => up("telemetry", "otlpEndpoint", e.target.value)}
          hint={errText("telemetry.otlpEndpoint")}
        />
        <TextField
          label="Grafana URL"
          type="url"
          inputMode="url"
          placeholder="https://grafana.example.com/d/sebastian-device"
          value={config.telemetry.grafanaUrl}
          onChange={(e) => up("telemetry", "grafanaUrl", e.target.value)}
          hint={errText("telemetry.grafanaUrl")}
        />
      </Group>

      <Group title="Audio">
        <SelectField
          label="Mic channel"
          value={config.audio.micChannel}
          onChange={(e) => up("audio", "micChannel", e.target.value as "right" | "left")}
        >
          <option value="right">RIGHT · ASR beam</option>
          <option value="left">LEFT · comms beam</option>
        </SelectField>
        <TextField
          label="Fixed beam azimuth (°)"
          type="number"
          inputMode="decimal"
          value={String(config.audio.fixedBeamAzimuthDeg)}
          onChange={(e) => up("audio", "fixedBeamAzimuthDeg", Number(e.target.value) || 0)}
        />
        <Toggle
          label="Fixed beam"
          checked={config.audio.fixedBeam}
          onChange={(v) => up("audio", "fixedBeam", v)}
        />
        <Toggle
          label="Full-duplex"
          checked={config.audio.fullDuplex}
          onChange={(v) => up("audio", "fullDuplex", v)}
        />
        <Toggle
          label="Probe AEC on boot"
          checked={config.audio.probeAecOnBoot}
          onChange={(v) => up("audio", "probeAecOnBoot", v)}
        />
      </Group>

      <Group title="Sesión">
        <TextField
          label="Silence timeout (ms)"
          type="number"
          inputMode="numeric"
          value={String(config.session.silenceTimeoutMs)}
          onChange={(e) => up("session", "silenceTimeoutMs", Number(e.target.value) || 0)}
        />
        <TextField
          label="Voice level"
          type="number"
          inputMode="numeric"
          value={String(config.session.voiceLevel)}
          onChange={(e) => up("session", "voiceLevel", Number(e.target.value) || 0)}
        />
      </Group>
    </div>
  );
}
