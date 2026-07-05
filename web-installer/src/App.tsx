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
  const [sendMsg, setSendMsg] = useState("With the board flashed and connected via USB, click send and choose the port.");
  const [sendTone, setSendTone] = useState<Tone>("");
  const [sending, setSending] = useState(false);
  const [sourceMsg, setSourceMsg] = useState("Checking published firmware…");
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
        setSourceMsg("Factory image ready to flash.");
      } catch {
        setInstallReady(false);
        setSourceMsg("No valid firmware image published. Provision an already flashed board (step 3) or use an external .bin (Advanced).");
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
    setSendMsg("Config copied to clipboard.");
  }

  async function send() {
    if (blocked) {
      setSendTone("warn");
      setSendMsg("Correct the marked fields before sending.");
      return;
    }
    setSending(true);
    setSendTone("");
    setSendMsg("Sending and waiting for board reply…");
    setLog([]);
    const result = await sendConfig(config, (line) => setLog((prev) => [...prev, line]));
    setSending(false);
    switch (result.status) {
      case "ok":
        setSendTone("ok");
        setSendMsg("✓ Config accepted. The board will restart and connect.");
        break;
      case "rejected":
        setSendTone("warn");
        setSendMsg(`La placa rechazó la config: ${result.reason}.`);
        break;
      case "no-reply":
        setSendTone("warn");
        setSendMsg("Sent, but no reply from the board. Has it finished booting? Retry.");
        break;
      case "unsupported":
        setSendTone("warn");
        setSendMsg("This browser does not support Web Serial. Use Chrome, Edge, Opera, or Brave.");
        break;
      case "cancelled":
        setSendTone("");
        setSendMsg("Send cancelled.");
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
              Install Sebastian from your browser
            </h1>
            <p className="max-w-xl text-[17px] leading-relaxed text-fg-muted">
              Connect via USB in Chrome, Edge, Opera, or Brave and follow three steps. The binary
              does not contain credentials: the config is injected via Web&nbsp;Serial.
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
                  Connect and install
                </button>
                <span slot="unsupported" className="text-sm font-semibold text-danger">
                  Browser without Web Serial. Use desktop Chromium.
                </span>
                <span slot="not-allowed" className="text-sm font-semibold text-danger">
                  Web Serial requires HTTPS or localhost.
                </span>
                {/* @ts-expect-error web component */}
              </esp-web-install-button>
              <p className={cx("text-sm", toneCls(installReady ? "" : "warn"))}>{sourceMsg}</p>
              <p className="text-sm text-fg-muted">
                Is the board already flashed? Skip this step and go directly to{" "}
                <a href="#enviar" className="font-semibold text-brand hover:underline">
                  Send via serial
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
            title="Configure the unit"
            hint={
              <>
                The fields with <span className="font-mono text-brand">NVS</span> are the ones the
                firmware applies today.
              </>
            }
          />
          <ConfigForm config={config} onChange={setConfig} issues={issues} />

          <details className="group mt-7 rounded-xl border border-line bg-black/20">
            <summary className="flex cursor-pointer list-none items-center gap-2 px-4 py-3 text-sm font-semibold text-fg-soft">
              <Chevron className="size-4 transition group-open:rotate-180" />
              View the JSON that will be sent
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
            title="Send the config to the board"
            hint="No flashing required: it talks to the firmware already running."
          />
          <div className="mt-6 flex flex-wrap items-center gap-4">
            <Btn variant="primary" onClick={send} disabled={sending} className="px-5 py-3 text-[15px]">
              {sending ? (
                <span className="size-4 animate-[spin-ring_0.8s_linear_infinite] rounded-full border-2 border-black/30 border-t-black/70" />
              ) : (
                <Bolt className="size-4" />
              )}
              Send via serial
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
        <ToolbarBtn onClick={onImport} icon={<Upload className="size-4" />} label="Import" />
        <ToolbarBtn onClick={onDownload} icon={<Download className="size-4" />} label="Download" />
        <ToolbarBtn onClick={onCopy} icon={<Copy className="size-4" />} label="Copy" />
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
  const tags = ["ESP32-S3", "Web Serial", "Provisioning"];

  return (
    <aside className="hidden md:grid gap-4">
      <div className="relative grid aspect-[4/3] place-items-center overflow-hidden rounded-2xl border border-line bg-gradient-to-br from-white/[0.07] via-white/[0.025] to-brand/[0.08]">
        <div className="absolute inset-x-8 top-7 h-px bg-gradient-to-r from-transparent via-brand/45 to-transparent" />
        <div className="absolute inset-x-8 bottom-7 h-px bg-gradient-to-r from-transparent via-brand/35 to-transparent" />
        <img
          src={`${BASE}brand/zetesis-mark.svg`}
          alt=""
          className="relative z-10 w-[42%] max-w-44 drop-shadow-[0_22px_44px_rgba(255,125,0,0.24)]"
        />
      </div>
      <div className="flex flex-wrap gap-2">
        {tags.map((tag) => (
          <span
            key={tag}
            className="rounded-full border border-line bg-white/[0.035] px-3 py-1.5 font-mono text-[11px] font-semibold text-fg-muted"
          >
            {tag}
          </span>
        ))}
      </div>
    </aside>
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
          <Eyebrow>Advanced</Eyebrow>
          <h2 className="font-serif text-xl text-fg">Firmware source</h2>
        </div>
        <Chevron className="ml-auto size-5 text-fg-muted transition group-open:rotate-180" />
      </summary>
      <div className="border-t border-line px-6 pb-6 pt-5 text-sm text-fg-muted">
        <p>
          If there is no published image, paste the URL of a <code className="text-fg-soft">manifest.json</code>
          or a <code className="text-fg-soft">.bin</code> merged (offset 0) in the URL with
          <code className="text-fg-soft"> ?manifest=</code> or <code className="text-fg-soft">?bin=</code>.
          The factory binary is built by CI in <code className="text-fg-soft">docs/installer/firmware/</code>.
        </p>
      </div>
    </details>
  );
}

function Notes() {
  const items = [
    "The firmware is a factory image: WiFi and token-server live in NVS, not in the binary. They are injected via Web Serial in step 3.",
    "When accepting the config, the firmware responds sebastian.config.ok and restarts itself to connect.",
    "The image must be merged at offset 0 for ESP Web Tools.",
    "If the serial connection fails, close any open monitor/bridge/esptool and retry.",
  ];
  return (
    <Card className="p-7">
      <Eyebrow>How it works</Eyebrow>
      <h2 className="mt-1 font-serif text-2xl text-fg">The binary carries no credentials</h2>
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
