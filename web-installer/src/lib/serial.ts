import { CONFIG_SCHEMA, parseDump, payloadFor, type DeviceConfig, type DeviceDump } from "./config";

export type SendResult =
  | { status: "ok" }
  | { status: "rejected"; reason: string }
  | { status: "no-reply" }
  | { status: "unsupported" }
  | { status: "cancelled" }
  | { status: "error"; message: string };

export type LoadResult =
  | { status: "ok"; dump: DeviceDump }
  | { status: "no-reply" }
  | { status: "unsupported" }
  | { status: "cancelled" }
  | { status: "error"; message: string };

export type LogFn = (line: string) => void;
const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

const GET_CMD = "sebastian.config.get";
const DUMP_PREFIX = "sebastian.config.dump ";

// The reply arrives in USB chunks, so a regex on the growing buffer would
// accept the first `}` it sees (the end of the `wifi` block) as the end of the
// JSON. Only a complete line (newline-terminated) that parses counts.
function findDump(buffer: string): unknown | undefined {
  for (const line of buffer.split(/\r?\n/).slice(0, -1)) {
    const at = line.indexOf(DUMP_PREFIX);
    if (at < 0) continue;
    try {
      return JSON.parse(line.slice(at + DUMP_PREFIX.length));
    } catch {
      continue;
    }
  }
  return undefined;
}

// The firmware keeps its USB-serial window open this long after a config.get
// (provisioning.c HOLD_AFTER_GET_US); past it the board hands the USB to the
// mic interface and the port is gone until re-plugged.
export const HOLD_AFTER_LOAD_MS = 120_000;

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

async function writeLine(port: SerialPort, line: string): Promise<void> {
  const writer = port.writable!.getWriter();
  try {
    await writer.write(new TextEncoder().encode(`${line}\n`));
  } finally {
    writer.releaseLock();
  }
}

function classify(error: unknown): SendResult & LoadResult {
  const message = error instanceof Error ? error.message : String(error);
  if (message.includes("No port selected") || message.includes("cancelled")) return { status: "cancelled" };
  if (message.includes("Failed to open")) {
    return { status: "error", message: "el puerto está ocupado. Cierra monitor/bridge/esptool y reintenta." };
  }
  if (message.includes("device has been lost") || message.includes("disconnected")) {
    return { status: "error", message: "la placa se desconectó (re-enumeró). Desenchufa/enchufa el USB y reintenta." };
  }
  return { status: "error", message };
}

// One serial port shared by "load from device" and "send": the load keeps it
// open (and the firmware keeps its window open) so the edited config goes back
// without re-plugging the board. A send always ends by closing it — the board
// restarts on success and the port disappears anyway.
let port: SerialPort | undefined;
let openedAt = 0;

export const linkOpen = () => port !== undefined;
export const linkAgeMs = () => (port ? Date.now() - openedAt : 0);

async function ensurePort(log: LogFn): Promise<SerialPort> {
  if (port) {
    log("· Reutilizando el puerto abierto.");
    return port;
  }
  log("Selecciona el puerto de la placa…");
  const p = await navigator.serial.requestPort();
  await p.open({ baudRate: 115200, bufferSize: 65536 });
  log("✓ Puerto abierto (115200).");
  await p.setSignals({ dataTerminalReady: false, requestToSend: false }).catch(() => {
    log("· setSignals no soportado (seguimos).");
  });
  await sleep(400);
  port = p;
  openedAt = Date.now();
  return p;
}

export async function closeLink(): Promise<void> {
  const p = port;
  port = undefined;
  if (p && (p.readable || p.writable)) await p.close().catch(() => {});
}

export async function loadConfig(log: LogFn = () => {}): Promise<LoadResult> {
  if (!("serial" in navigator)) return { status: "unsupported" };
  try {
    const p = await ensurePort(log);
    for (let attempt = 1; attempt <= 3; attempt++) {
      log(`Pidiendo la config guardada${attempt > 1 ? ` — reintento ${attempt}` : ""}…`);
      await writeLine(p, GET_CMD);
      const reply = await readFor(p, 3000, (b) => findDump(b) !== undefined || /sebastian\.config\.err/.test(b));
      const raw = findDump(reply);
      if (raw !== undefined) {
        const dump = parseDump(raw);
        log(`✓ Config recibida. La placa mantiene el puerto ${HOLD_AFTER_LOAD_MS / 1000} s.`);
        return { status: "ok", dump };
      }
      const err = reply.match(/sebastian\.config\.err\s*([^\r\n]*)/);
      if (err) {
        log(`✗ Error de la placa: ${err[1].trim()}`);
        await closeLink();
        return { status: "error", message: err[1].trim() || "error" };
      }
      log(reply.trim() ? `· Sin dump. Última línea: "${lastLine(reply).slice(0, 70)}"` : "· Sin respuesta.");
    }
    await closeLink();
    return { status: "no-reply" };
  } catch (error) {
    log(`✗ Excepción: ${error instanceof Error ? error.message : String(error)}`);
    await closeLink();
    return classify(error);
  }
}

export async function sendConfig(
  config: DeviceConfig,
  keepStoredPassword: boolean,
  log: LogFn = () => {},
): Promise<SendResult> {
  if (!("serial" in navigator)) return { status: "unsupported" };

  const payload = `${CONFIG_SCHEMA} ${payloadFor(config, keepStoredPassword)}`;
  const fresh = !port;
  try {
    const p = await ensurePort(log);
    if (fresh) {
      // Sniff: is the device alive and stable? If it's mid-reboot/re-enumeration
      // (native USB after a flash), this is where we'd see nothing or a drop.
      log("Escuchando la placa antes de escribir…");
      const sniff = await readFor(p, 1500, (b) => b.includes("waiting for wake") || b.includes("provisioning receiver"));
      if (sniff.trim()) {
        log(`· La placa dice: "${lastLine(sniff).slice(0, 70)}"`);
      } else {
        log("· ⚠ Silencio — la placa puede estar reiniciando o el puerto reseteó al abrir.");
      }
    }

    for (let attempt = 1; attempt <= 2; attempt++) {
      log(`Escribiendo config (${payload.length + 1} bytes)${attempt > 1 ? ` — reintento ${attempt}` : ""}…`);
      await writeLine(p, payload);
      log("Esperando respuesta del firmware…");
      const reply = await readFor(p, 4000, (b) => /sebastian\.config\.(ok|err)/.test(b));
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
  } catch (error) {
    log(`✗ Excepción: ${error instanceof Error ? error.message : String(error)}`);
    return classify(error);
  } finally {
    await closeLink();
  }
}
