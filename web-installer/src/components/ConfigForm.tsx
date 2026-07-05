import { useState } from "react";
import { applyMode, type DeviceConfig } from "../lib/config";
import type { FieldIssue } from "../lib/validate";
import {
  MODES,
  SHARED,
  getField,
  setField,
  type FieldMeta,
  type Lock,
  type SharedIcon,
} from "../lib/modes";
import { SelectField, Switch, TextField, ToggleField, cx, type HelpMeta } from "./ui";
import { Check } from "./icons";

interface Props {
  config: DeviceConfig;
  onChange: (next: DeviceConfig) => void;
  issues: FieldIssue[];
}

const GROUP_ICON: Record<SharedIcon, React.ReactNode> = {
  wifi: (
    <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round">
      <path d="M5 12.5a10 10 0 0 1 14 0" />
      <path d="M8.5 16a5 5 0 0 1 7 0" />
      <circle cx="12" cy="19" r="1" fill="currentColor" stroke="none" />
    </svg>
  ),
  cloud: (
    <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
      <path d="M17 18a4 4 0 0 0 .5-8 6 6 0 0 0-11.5 1.5A3.5 3.5 0 0 0 6.5 18Z" />
    </svg>
  ),
  chart: (
    <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
      <path d="M4 20V10M10 20V4M16 20v-7M22 20H2" />
    </svg>
  ),
};

function helpFor(f: FieldMeta): HelpMeta {
  const range = f.min !== undefined ? `  ·  ${f.min}…${f.max}` : "";
  return { label: f.label, body: f.help, meta: `${f.path}${range}` };
}

function Callout({ tone, children }: { tone: "info" | "warn"; children: React.ReactNode }) {
  return (
    <div
      className={cx(
        "mb-4 flex gap-2.5 rounded-xl border p-3 text-[12.5px] leading-relaxed text-fg-soft",
        tone === "warn" ? "border-warn/40 bg-warn/[0.08]" : "border-brand/30 bg-brand/[0.06]",
      )}
    >
      {children}
    </div>
  );
}

function Locks({ locks }: { locks: Lock[] }) {
  return (
    <div className="mb-4 overflow-hidden rounded-xl border border-dashed border-line-strong bg-white/[0.02]">
      {locks.map((l, i) => (
        <div key={l.label} className={cx("flex gap-3 px-3 py-2.5", i > 0 && "border-t border-line")}>
          <span className="w-24 shrink-0 pt-0.5 font-mono text-[11px] text-fg-muted">{l.label}</span>
          <span className="text-[12.5px] text-fg-soft">
            <b className="font-semibold text-brand">{l.value}</b>
            <span className="mt-0.5 block text-[11.5px] leading-snug text-fg-muted">{l.why}</span>
          </span>
        </div>
      ))}
    </div>
  );
}

export function ConfigForm({ config, onChange, issues }: Props) {
  const [advanced, setAdvanced] = useState(false);

  const issueFor = (path?: string) => (path ? issues.find((i) => i.path === path) : undefined);

  function Field({ f }: { f: FieldMeta }) {
    const value = getField(config, f.path);
    const set = (v: unknown) => onChange(setField(config, f.path, v));
    const common = {
      label: f.label,
      applied: f.applied,
      pending: !f.applied,
      advanced: f.advanced,
      help: helpFor(f),
    };

    if (f.type === "toggle") {
      return <ToggleField {...common} checked={Boolean(value)} onChange={set} />;
    }
    if (f.type === "enum") {
      return (
        <SelectField {...common} value={String(value)} onChange={(e) => set(e.target.value)}>
          {f.options?.map((o) => (
            <option key={o.value} value={o.value}>
              {o.label}
            </option>
          ))}
        </SelectField>
      );
    }

    const issue = issueFor(f.issuePath);
    const hint = issue ? (
      <span className={issue.severity === "error" ? "text-danger" : "text-warn"}>{issue.message}</span>
    ) : undefined;
    const isNumber = f.type === "number";
    return (
      <TextField
        {...common}
        type={f.type === "password" ? "password" : f.type === "url" ? "url" : isNumber ? "number" : "text"}
        inputMode={isNumber ? "numeric" : f.type === "url" ? "url" : undefined}
        autoComplete="off"
        placeholder={f.placeholder}
        value={value === undefined || value === null ? "" : String(value)}
        onChange={(e) => set(isNumber ? Number(e.target.value) || 0 : e.target.value)}
        hint={hint}
      />
    );
  }

  const visible = (fields: FieldMeta[]) => fields.filter((f) => advanced || !f.advanced);
  const mode = MODES.find((m) => m.id === config.mode) ?? MODES[0];

  return (
    <div className="mt-7 grid gap-7">
      {/* Mode picker — mutually exclusive */}
      <div>
        <p className="mb-3 font-mono text-[11px] font-semibold uppercase tracking-[0.13em] text-fg-muted">
          Operating mode
        </p>
        <div className="grid gap-3 sm:grid-cols-3">
          {MODES.map((m) => {
            const selected = m.id === config.mode;
            return (
              <button
                key={m.id}
                type="button"
                aria-pressed={selected}
                onClick={() => onChange(applyMode(config, m.id))}
                className={cx(
                  "relative flex flex-col gap-1.5 rounded-2xl border p-4 text-left transition",
                  selected
                    ? "border-brand bg-brand/[0.06] shadow-[0_0_0_3px_rgba(255,144,0,0.12)]"
                    : "border-line-strong bg-white/[0.02] hover:border-brand/50 hover:bg-white/[0.04]",
                )}
              >
                <span
                  className={cx(
                    "w-fit rounded-full border px-2 py-0.5 font-mono text-[9px] uppercase tracking-wider",
                    selected ? "border-brand/40 bg-brand/10 text-brand" : "border-line-strong text-fg-muted",
                  )}
                >
                  {m.pill}
                </span>
                <h4 className="text-[15px] font-semibold text-fg">{m.title}</h4>
                <p className="-mt-1 text-[12px] font-semibold text-brand">{m.sub}</p>
                <p className="text-[12.5px] leading-snug text-fg-muted">{m.blurb}</p>
                <p className="mt-auto flex items-center gap-1.5 pt-1 text-[11.5px] text-fg-soft">
                  <span className="size-1.5 shrink-0 rounded-full bg-brand" />
                  {m.best}
                </p>
                {selected && (
                  <span className="absolute right-3 top-3 grid size-5 place-items-center rounded-full bg-brand text-[#1c1204]">
                    <Check className="size-3" />
                  </span>
                )}
              </button>
            );
          })}
        </div>
      </div>

      {/* Selected mode's settings */}
      <fieldset className="border-t border-line pt-5">
        <legend className="mb-3 font-serif text-lg text-fg">{mode.title} settings</legend>
        {mode.callout && <Callout tone={mode.callout.tone}>{mode.callout.text}</Callout>}
        {mode.locks.length > 0 && <Locks locks={mode.locks} />}
        <div className="grid gap-4 sm:grid-cols-2">
          {visible(mode.fields).map((f) => (
            <Field key={f.path} f={f} />
          ))}
        </div>
      </fieldset>

      {/* Shared sections */}
      {SHARED.map((group) => {
        const fields = visible(group.fields);
        if (fields.length === 0) return null;
        return (
          <fieldset key={group.id} className="border-t border-line pt-5">
            <legend className="mb-3 flex items-center gap-2.5">
              <span className="grid size-7 place-items-center rounded-lg bg-white/[0.05] text-brand">
                {GROUP_ICON[group.icon]}
              </span>
              <span className="font-serif text-lg text-fg">{group.title}</span>
              <span className="text-[11.5px] text-fg-muted">{group.sub}</span>
            </legend>
            <div className="grid gap-4 sm:grid-cols-2">
              {fields.map((f) => (
                <Field key={f.path} f={f} />
              ))}
            </div>
          </fieldset>
        );
      })}

      {/* Advanced disclosure */}
      <div className="flex items-center justify-between border-t border-line pt-4">
        <span className="text-[13px] text-fg-muted">
          Show advanced options
          <span className="ml-2 rounded border border-line px-1 py-px font-mono text-[9px] uppercase tracking-wider text-fg-muted/60">
            experts
          </span>
        </span>
        <Switch checked={advanced} onChange={setAdvanced} label="Show advanced options" />
      </div>
    </div>
  );
}
