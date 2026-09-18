import type { DeviceConfig } from "./config";

export interface FieldIssue {
  path: string;
  message: string;
  severity: "error" | "warning";
}

function isUrl(value: string): boolean {
  try {
    new URL(value);
    return true;
  } catch {
    return false;
  }
}

// Validate what actually matters for provisioning. Errors block sending; warnings
// (e.g. no token server) are informational — the device still connects to WiFi.
export function validate(config: DeviceConfig): FieldIssue[] {
  const issues: FieldIssue[] = [];

  if (!config.wifi.ssid.trim()) {
    issues.push({ path: "wifi.ssid", message: "El SSID no puede estar vacío.", severity: "error" });
  }
  const url = config.livekit.tokenServerUrl.trim();
  if (!url) {
    issues.push({
      path: "livekit.tokenServerUrl",
      message: "Sin token server la placa conectará al WiFi pero no abrirá sesiones.",
      severity: "warning",
    });
  } else if (!isUrl(url)) {
    issues.push({
      path: "livekit.tokenServerUrl",
      message: "El token server URL no es una URL válida.",
      severity: "error",
    });
  }
  const ip = config.telemetry.syslogIp.trim();
  if (ip && !/^\d{1,3}(\.\d{1,3}){3}$/.test(ip)) {
    issues.push({ path: "telemetry.syslogIp", message: "Debe ser una IPv4 (el firmware no resuelve nombres).", severity: "error" });
  }
  const port = config.telemetry.syslogPort;
  if (!Number.isInteger(port) || port < 1 || port > 65535) {
    issues.push({ path: "telemetry.syslogPort", message: "Puerto fuera de rango.", severity: "error" });
  }
  if (config.session.silenceTimeoutMs < 5000) {
    issues.push({ path: "session.silenceTimeoutMs", message: "Mínimo 5000 ms.", severity: "error" });
  }
  if (config.session.voiceLevel <= 0) {
    issues.push({ path: "session.voiceLevel", message: "Debe ser mayor que 0.", severity: "error" });
  }
  for (const [name, value] of Object.entries({
    otlpEndpoint: config.telemetry.otlpEndpoint,
    grafanaUrl: config.telemetry.grafanaUrl,
  })) {
    if (value.trim() && !isUrl(value.trim())) {
      issues.push({ path: `telemetry.${name}`, message: "URL no válida.", severity: "error" });
    }
  }

  return issues;
}

export const hasBlockingErrors = (issues: FieldIssue[]): boolean =>
  issues.some((issue) => issue.severity === "error");
