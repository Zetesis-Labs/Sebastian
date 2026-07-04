import { useEffect, useMemo, useRef, useState } from "react";
import { defaultConfig, mergeConfig, serialize, type DeviceConfig } from "./lib/config";
import { hasBlockingErrors, validate } from "./lib/validate";
import { sendConfig } from "./lib/serial";
import { ConfigForm } from "./components/ConfigForm";
import { Btn, Card, Eyebrow, SectionTitle, cx } from "./components/ui";
import { Bolt, Check, Chevron, Copy, Download, Github, Upload, Wifi } from "./components/icons";

type Tone = "" | "ok" | "warn";
const BASE = import.meta.env.BASE_URL;

export default function App() {
  const [config, setConfig] = useState<DeviceConfig>(defaultConfig);
  const [sendMsg, setSendMsg] = useState("Con la placa flasheada y conectada por USB, pulsa enviar y elige el puerto.");
  const [sendTone, setSendTone] = useState<Tone>("");
  const [sending, setSending] = useState(false);
  const [sourceMsg, setSourceMsg] = useState("Comprobando firmware publicado…");
  const [installReady, setInstallReady] = useState(false);
  const [log, setLog] = useState<string[]>([]);

  const fileRef = useRef<HTMLInputElement>(null);
  const issues = useMemo(() => validate(config), [config]);
  const blocked = hasBlockingErrors(issues);

  useEffect(() => {
    // Is a real merged image published next to the page? If not, ESP Web Tools
    // has nothing to flash — say so instead of a dead button.
    (async () => {
      try {
        const res = await fetch(`${BASE}manifest.json`, { cache: "no-store" });
        if (!res.ok) throw new Error();
        const manifest = await res.json();
        const part = manifest?.builds?.[0]?.parts?.[0]?.path;
        if (!part) throw new Error();
        // Validate it's a REAL ESP image, not an SPA-fallback index.html. A dev
        // server returns HTML (200) for a missing .bin; ESP Web Tools would then
        // flash that HTML over the bootloader and brick the chip. Require the
        // ESP32 image magic byte 0xE9 (HTML starts with '<' = 0x3C).
        const bin = await fetch(new URL(part, res.url), {
          cache: "no-store",
          headers: { Range: "bytes=0-3" },
        });
        const ct = bin.headers.get("content-type") ?? "";
        if (!bin.ok || ct.includes("text/html")) throw new Error("html-fallback");
        const firstByte = new Uint8Array(await bin.arrayBuffer())[0];
        if (firstByte !== 0xe9) throw new Error("not-an-esp-image");
        setInstallReady(true);
        setSourceMsg("Imagen de fábrica lista para flashear.");
      } catch {
        setInstallReady(false);
        setSourceMsg("No hay imagen de firmware válida publicada. Provisiona una placa ya flasheada (paso 3) o usa un .bin externo (Avanzado).");
      }
    })();
  }, []);

  function importJson(file: File | undefined) {
    if (!file) return;
    file
      .text()
      .then((text) => {
        setConfig(mergeConfig(JSON.parse(text)));
        setSendTone("ok");
        setSendMsg(`Config importada desde ${file.name}.`);
      })
      .catch((e) => {
        setSendTone("warn");
        setSendMsg(`No se pudo leer el JSON: ${e instanceof Error ? e.message : e}`);
      });
  }

  function downloadJson() {
    const blob = new Blob([serialize(config), "\n"], { type: "application/json" });
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = "sebastian-config.json";
    a.click();
    URL.revokeObjectURL(url);
  }

  async function copyJson() {
    await navigator.clipboard.writeText(serialize(config));
    setSendTone("ok");
    setSendMsg("Config copiada al portapapeles.");
  }

  async function send() {
    if (blocked) {
      setSendTone("warn");
      setSendMsg("Corrige los campos marcados antes de enviar.");
      return;
    }
    setSending(true);
    setSendTone("");
    setSendMsg("Enviando y esperando respuesta de la placa…");
    setLog([]);
    const result = await sendConfig(config, (line) => setLog((prev) => [...prev, line]));
    setSending(false);
    switch (result.status) {
      case "ok":
        setSendTone("ok");
        setSendMsg("✓ Config aceptada. La placa se reinicia y se conecta.");
        break;
      case "rejected":
        setSendTone("warn");
        setSendMsg(`La placa rechazó la config: ${result.reason}.`);
        break;
      case "no-reply":
        setSendTone("warn");
        setSendMsg("Enviada, pero la placa no respondió. ¿Ha terminado de arrancar? Reintenta.");
        break;
      case "unsupported":
        setSendTone("warn");
        setSendMsg("Este navegador no soporta Web Serial. Usa Chrome, Edge, Opera o Brave.");
        break;
      case "cancelled":
        setSendTone("");
        setSendMsg("Envío cancelado.");
        break;
      default:
        setSendTone("warn");
        setSendMsg(`No se pudo enviar: ${result.message}`);
    }
  }

  const toneCls = (t: Tone) => (t === "ok" ? "text-ok" : t === "warn" ? "text-warn" : "text-fg-muted");

  return (
    <div className="mx-auto w-[min(1120px,calc(100%-32px))] pb-20">
      <JsonToolbar
        onImport={() => fileRef.current?.click()}
        onDownload={downloadJson}
        onCopy={copyJson}
      />
      <input
        ref={fileRef}
        type="file"
        accept=".json,application/json"
        className="hidden"
        onChange={(e) => {
          importJson(e.target.files?.[0]);
          e.target.value = "";
        }}
      />

      <main className="grid gap-5">
        {/* Hero + install (step 1) */}
        <Card className="grid items-center gap-8 p-8 md:grid-cols-[1.15fr_0.85fr]">
          <div className="grid gap-5">
            <Eyebrow>ReSpeaker XVF3800 + XIAO ESP32-S3</Eyebrow>
            <h1 className="font-serif text-[clamp(34px,5vw,52px)] font-medium leading-[1.03] text-fg">
              Instala Sebastian desde el navegador
            </h1>
            <p className="max-w-xl text-[17px] leading-relaxed text-fg-muted">
              Conéctalo por USB en Chrome, Edge, Opera o Brave y sigue tres pasos. El binario
              no lleva credenciales: la config se inyecta por Web&nbsp;Serial.
            </p>
            <div className="mt-1 grid justify-items-start gap-3">
              {/* @ts-expect-error web component */}
              <esp-web-install-button manifest={`${BASE}manifest.json`}>
                <button
                  slot="activate"
                  disabled={!installReady}
                  className="inline-flex items-center gap-2.5 rounded-xl border border-brand/50 bg-gradient-to-br from-brand-deep via-brand-strong to-brand px-5 py-3 text-[15px] font-bold text-[#1c1204] shadow-[0_14px_34px_-12px_var(--color-brand)] transition hover:brightness-110 disabled:cursor-not-allowed disabled:opacity-45"
                >
                  <span className="grid size-6 place-items-center rounded-full bg-black/20 text-xs">1</span>
                  Conectar e instalar
                </button>
                <span slot="unsupported" className="text-sm font-semibold text-danger">
                  Navegador sin Web Serial. Usa Chromium de escritorio.
                </span>
                <span slot="not-allowed" className="text-sm font-semibold text-danger">
                  Web Serial exige HTTPS o localhost.
                </span>
                {/* @ts-expect-error web component */}
              </esp-web-install-button>
              <p className={cx("text-sm", toneCls(installReady ? "" : "warn"))}>{sourceMsg}</p>
              <p className="text-sm text-fg-muted">
                ¿La placa ya viene flasheada? Sáltate este paso y ve directo a{" "}
                <a href="#enviar" className="font-semibold text-brand hover:underline">
                  Enviar por serial
                </a>{" "}
                (paso 3).
              </p>
            </div>
          </div>
          <DeviceVisual />
        </Card>

        {/* Configure (step 2) */}
        <Card className="p-7 md:p-8">
          <SectionTitle
            step={2}
            eyebrow="Provisioning"
            title="Configura la unidad"
            hint={
              <>
                Los campos con <span className="font-mono text-brand">NVS</span> son los que el
                firmware aplica hoy.
              </>
            }
          />
          <ConfigForm config={config} onChange={setConfig} issues={issues} />

          <details className="group mt-7 rounded-xl border border-line bg-black/20">
            <summary className="flex cursor-pointer list-none items-center gap-2 px-4 py-3 text-sm font-semibold text-fg-soft">
              <Chevron className="size-4 transition group-open:rotate-180" />
              Ver el JSON que se enviará
            </summary>
            <pre className="max-h-72 overflow-auto border-t border-line px-4 py-3 font-mono text-[12.5px] leading-relaxed text-[#f4c78f]">
              {serialize(config)}
            </pre>
          </details>
        </Card>

        {/* Send (step 3) */}
        <Card id="enviar" className="scroll-mt-24 p-7 md:p-8">
          <SectionTitle
            step={3}
            eyebrow="Provisioning"
            title="Envía la config a la placa"
            hint="No necesita flashear: habla con el firmware que ya corre."
          />
          <div className="mt-6 flex flex-wrap items-center gap-4">
            <Btn variant="primary" onClick={send} disabled={sending} className="px-5 py-3 text-[15px]">
              {sending ? (
                <span className="size-4 animate-[spin-ring_0.8s_linear_infinite] rounded-full border-2 border-black/30 border-t-black/70" />
              ) : (
                <Bolt className="size-4" />
              )}
              Enviar por serial
            </Btn>
            <p className={cx("flex items-center gap-2 text-sm", toneCls(sendTone))}>
              {sendTone === "ok" && <Check className="size-4" />}
              {sendMsg}
            </p>
          </div>
          {log.length > 0 && (
            <pre className="mt-5 max-h-56 overflow-auto rounded-xl border border-line bg-black/40 px-4 py-3 font-mono text-[12.5px] leading-relaxed text-fg-soft">
              {log.join("\n")}
            </pre>
          )}
        </Card>

        <Advanced installReady={installReady} />
        <Notes />
      </main>
    </div>
  );
}

function JsonToolbar({
  onImport,
  onDownload,
  onCopy,
}: {
  onImport: () => void;
  onDownload: () => void;
  onCopy: () => void;
}) {
  return (
    <header className="sticky top-0 z-20 -mx-4 mb-5 flex items-center gap-3 px-4 py-3 backdrop-blur-md">
      <div className="pointer-events-none absolute inset-0 -z-10 border-b border-line bg-ink/70" />
      <a href="./" className="flex items-center gap-2.5 text-fg" aria-label="Zetesis">
        <img src={`${BASE}brand/zetesis-mark.svg`} alt="" className="size-7" />
        <span className="hidden text-sm font-semibold tracking-tight text-fg-soft sm:block">
          Sebastian Installer
        </span>
      </a>
      <div className="ml-auto flex items-center gap-1.5 rounded-xl border border-line bg-white/[0.03] p-1">
        <ToolbarBtn onClick={onImport} icon={<Upload className="size-4" />} label="Importar" />
        <ToolbarBtn onClick={onDownload} icon={<Download className="size-4" />} label="Descargar" />
        <ToolbarBtn onClick={onCopy} icon={<Copy className="size-4" />} label="Copiar" />
      </div>
      <a
        href="https://github.com/Zetesis-Labs/Sebastian"
        className="grid size-9 place-items-center rounded-xl border border-line bg-white/[0.03] text-fg-soft transition hover:bg-white/[0.08]"
        aria-label="GitHub"
      >
        <Github className="size-4" />
      </a>
    </header>
  );
}

function ToolbarBtn({ onClick, icon, label }: { onClick: () => void; icon: React.ReactNode; label: string }) {
  return (
    <button
      onClick={onClick}
      className="inline-flex items-center gap-1.5 rounded-lg px-2.5 py-1.5 text-[13px] font-semibold text-fg-soft transition hover:bg-white/[0.07] hover:text-fg focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-brand/30"
    >
      {icon}
      <span className="hidden sm:inline">{label}</span>
    </button>
  );
}

function DeviceVisual() {
  return (
    <div className="relative hidden aspect-[4/3] overflow-hidden rounded-2xl border border-line bg-gradient-to-br from-brand/[0.14] via-transparent to-transparent md:block">
      <div className="absolute inset-x-5 top-6 h-px bg-gradient-to-r from-transparent via-brand/50 to-transparent" />
      <div className="absolute inset-x-5 bottom-6 h-px bg-gradient-to-r from-transparent via-brand/50 to-transparent" />
      <div className="absolute right-8 top-1/2 grid size-28 -translate-y-1/2 place-items-center">
        <div className="absolute inset-0 animate-[spin-ring_6s_linear_infinite] rounded-full border-[9px] border-white/8 border-t-brand border-r-brand-strong" />
        <div className="size-14 rounded-full border border-line-strong bg-ink/80" />
      </div>
      <div className="absolute left-6 top-6 grid gap-2">
        {["ESP32-S3", "Web Serial", "Provisioning"].map((t) => (
          <span
            key={t}
            className="rounded-full border border-line bg-ink/50 px-2.5 py-1 font-mono text-[10.5px] text-fg-soft"
          >
            {t}
          </span>
        ))}
      </div>
      <div className="absolute bottom-6 left-6 grid w-20 gap-2 rounded-lg border border-line bg-ink/80 p-3">
        <span className="h-1.5 rounded bg-gradient-to-r from-brand-deep to-brand" />
        <span className="h-1.5 rounded bg-gradient-to-r from-brand-deep to-brand" />
        <span className="h-1.5 rounded bg-gradient-to-r from-brand-deep to-brand" />
      </div>
    </div>
  );
}

function Advanced({ installReady }: { installReady: boolean }) {
  return (
    <details
      className="group overflow-hidden rounded-[var(--radius-xl)] border border-line bg-gradient-to-b from-white/[0.055] to-white/[0.015] shadow-[0_28px_90px_-30px_rgba(0,0,0,0.75)]"
      open={!installReady}
    >
      <summary className="flex cursor-pointer list-none items-center gap-3 p-6">
        <Wifi className="size-5 text-brand" />
        <div>
          <Eyebrow>Avanzado</Eyebrow>
          <h2 className="font-serif text-xl text-fg">Fuente de firmware</h2>
        </div>
        <Chevron className="ml-auto size-5 text-fg-muted transition group-open:rotate-180" />
      </summary>
      <div className="border-t border-line px-6 pb-6 pt-5 text-sm text-fg-muted">
        <p>
          Si no hay imagen publicada, pega la URL de un <code className="text-fg-soft">manifest.json</code> o
          de un <code className="text-fg-soft">.bin</code> fusionado (offset 0) en la URL con
          <code className="text-fg-soft"> ?manifest=</code> o <code className="text-fg-soft">?bin=</code>.
          El binario factory lo construye la CI en <code className="text-fg-soft">docs/installer/firmware/</code>.
        </p>
      </div>
    </details>
  );
}

function Notes() {
  const items = [
    "El firmware es un factory image: WiFi y token-server viven en NVS, no en el binario. Se inyectan por Web Serial en el paso 3.",
    "Al aceptar la config el firmware responde sebastian.config.ok y se reinicia solo para conectarse.",
    "La imagen debe estar fusionada a offset 0 para ESP Web Tools.",
    "Si falla la conexión serial, cierra cualquier monitor/bridge/esptool abierto y reintenta.",
  ];
  return (
    <Card className="p-7">
      <Eyebrow>Cómo funciona</Eyebrow>
      <h2 className="mt-1 font-serif text-2xl text-fg">El binario no lleva credenciales</h2>
      <ul className="mt-4 grid gap-2.5">
        {items.map((t) => (
          <li key={t} className="flex gap-3 text-[15px] leading-relaxed text-fg-muted">
            <Check className="mt-1 size-4 shrink-0 text-brand" />
            {t}
          </li>
        ))}
      </ul>
    </Card>
  );
}
