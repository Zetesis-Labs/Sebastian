import { CONFIG_SCHEMA, type DeviceConfig } from "./config";

export type SendResult =
  | { status: "ok" }
  | { status: "rejected"; reason: string }
  | { status: "no-reply" }
  | { status: "unsupported" }
  | { status: "cancelled" }
  | { status: "error"; message: string };

export type LogFn = (line: string) => void;
const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

// Read from the port for up to `ms`, returning the decoded text and stopping
// early if `stopOn` is seen. Used both to sniff whether the device is alive
// before writing and to catch the provisioning reply.
async function readFor(
  port: SerialPort,
  ms: number,
  stopOn?: (buffer: string) => boolean,
): Promise<string> {
  const reader = port.readable!.getReader();
  const decoder = new TextDecoder();
  const timer = setTimeout(() => reader.cancel().catch(() => {}), ms);
  let buffer = "";
  try {
    for (;;) {
      const { value, done } = await reader.read();
      if (done) break;
      buffer += decoder.decode(value, { stream: true });
      if (stopOn?.(buffer)) break;
    }
  } finally {
    clearTimeout(timer);
    reader.releaseLock();
  }
  return buffer;
}

const lastLine = (text: string) =>
  text.split(/[\r\n]+/).filter(Boolean).slice(-1)[0] ?? "";

export async function sendConfig(config: DeviceConfig, log: LogFn = () => {}): Promise<SendResult> {
  if (!("serial" in navigator)) return { status: "unsupported" };

  const payload = new TextEncoder().encode(`${CONFIG_SCHEMA} ${JSON.stringify(config)}\n`);
  let port: SerialPort | undefined;
  try {
    log("Selecciona el puerto de la placa…");
    port = await navigator.serial.requestPort();
    await port.open({ baudRate: 115200 });
    log("✓ Puerto abierto (115200).");

    await port.setSignals({ dataTerminalReady: false, requestToSend: false }).catch(() => {
      log("· setSignals no soportado (seguimos).");
    });
    await sleep(400);

    // Sniff: is the device alive and stable? If it's mid-reboot/re-enumeration
    // (native USB after a flash), this is where we'd see nothing or a drop.
    log("Escuchando la placa antes de escribir…");
    const sniff = await readFor(port, 1500, (b) => b.includes("waiting for wake") || b.includes("provisioning receiver"));
    if (sniff.trim()) {
      log(`· La placa dice: "${lastLine(sniff).slice(0, 70)}"`);
    } else {
      log("· ⚠ Silencio — la placa puede estar reiniciando o el puerto reseteó al abrir.");
    }

    const writer = port.writable!.getWriter();
    try {
      for (let attempt = 1; attempt <= 2; attempt++) {
        log(`Escribiendo config (${payload.length} bytes)${attempt > 1 ? ` — reintento ${attempt}` : ""}…`);
        await writer.write(payload);
        log("Esperando respuesta del firmware…");
        const reply = await readFor(port, 4000, (b) => /sebastian\.config\.(ok|err)/.test(b));
        if (reply.includes("sebastian.config.ok")) {
          log("✓ Recibido: sebastian.config.ok");
          return { status: "ok" };
        }
        const err = reply.match(/sebastian\.config\.err\s*([^\r\n]*)/);
        if (err) {
          log(`✗ Rechazado: ${err[1].trim() || "error"}`);
          return { status: "rejected", reason: err[1].trim() || "error" };
        }
        log(reply.trim() ? `· Sin ok/err. Última línea: "${lastLine(reply).slice(0, 70)}"` : "· Sin respuesta.");
      }
      return { status: "no-reply" };
    } finally {
      writer.releaseLock();
    }
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error);
    log(`✗ Excepción: ${message}`);
    if (message.includes("No port selected") || message.includes("cancelled")) return { status: "cancelled" };
    if (message.includes("Failed to open")) {
      return { status: "error", message: "el puerto está ocupado. Cierra monitor/bridge/esptool y reintenta." };
    }
    if (message.includes("device has been lost") || message.includes("disconnected")) {
      return { status: "error", message: "la placa se desconectó (re-enumeró). Desenchufa/enchufa el USB y reintenta." };
    }
    return { status: "error", message };
  } finally {
    if (port?.readable || port?.writable) await port.close().catch(() => {});
  }
}
