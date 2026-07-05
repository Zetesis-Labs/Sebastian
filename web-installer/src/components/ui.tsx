import type { ReactNode, InputHTMLAttributes, SelectHTMLAttributes } from "react";
import { useEffect, useRef, useState } from "react";

export interface HelpMeta {
  label: string;
  body: string;
  meta?: string;
}

export function cx(...parts: Array<string | false | undefined>): string {
  return parts.filter(Boolean).join(" ");
}

export function Card({ id, className, children }: { id?: string; className?: string; children: ReactNode }) {
  return (
    <div
      id={id}
      className={cx(
        "rounded-[var(--radius-xl)] border border-line bg-gradient-to-b from-white/[0.055] to-white/[0.015] shadow-[0_28px_90px_-30px_rgba(0,0,0,0.75)] backdrop-blur-sm",
        className,
      )}
    >
      {children}
    </div>
  );
}

export function StepBadge({ n }: { n: number }) {
  return (
    <span className="grid size-8 shrink-0 place-items-center rounded-full bg-gradient-to-br from-brand-strong to-brand text-[15px] font-bold text-[#1c1204] shadow-[0_0_18px_-2px_var(--color-brand)]">
      {n}
    </span>
  );
}

export function Eyebrow({ children }: { children: ReactNode }) {
  return (
    <p className="font-mono text-[11px] font-bold uppercase tracking-[0.14em] text-brand">
      {children}
    </p>
  );
}

export function SectionTitle({
  step,
  eyebrow,
  title,
  hint,
}: {
  step?: number;
  eyebrow: string;
  title: string;
  hint?: ReactNode;
}) {
  return (
    <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
      {step !== undefined && <StepBadge n={step} />}
      <div className="min-w-0">
        <Eyebrow>{eyebrow}</Eyebrow>
        <h2 className="font-serif text-[26px] font-medium leading-tight text-fg">{title}</h2>
      </div>
      {hint && <p className="ml-auto max-w-xs text-sm text-fg-muted">{hint}</p>}
    </div>
  );
}

type BtnVariant = "primary" | "ghost" | "solid";
export function Btn({
  variant = "ghost",
  className,
  children,
  ...rest
}: { variant?: BtnVariant } & React.ButtonHTMLAttributes<HTMLButtonElement>) {
  const styles: Record<BtnVariant, string> = {
    primary:
      "bg-gradient-to-br from-brand-deep via-brand-strong to-brand text-[#1c1204] border-brand/50 shadow-[0_10px_30px_-10px_var(--color-brand)] hover:brightness-108",
    solid: "bg-white/[0.06] border-line-strong text-fg hover:bg-white/[0.1]",
    ghost: "bg-white/[0.03] border-line text-fg-soft hover:bg-white/[0.07] hover:text-fg",
  };
  return (
    <button
      {...rest}
      className={cx(
        "inline-flex items-center justify-center gap-2 rounded-xl border px-4 py-2.5 text-sm font-semibold transition disabled:cursor-not-allowed disabled:opacity-45",
        "focus-visible:outline-none focus-visible:ring-4 focus-visible:ring-brand/25",
        styles[variant],
        className,
      )}
    >
      {children}
    </button>
  );
}

export function AppliedBadge() {
  return (
    <span className="rounded-full border border-brand/30 bg-brand/10 px-1.5 py-0.5 align-middle font-mono text-[9px] font-bold uppercase tracking-wide text-brand">
      NVS
    </span>
  );
}

// Carried in the payload but not applied by the current firmware — lights up once
// the firmware reads it from NVS (see PROVISIONING.md).
export function PendingBadge() {
  return (
    <span className="rounded-full border border-line-strong bg-white/[0.04] px-1.5 py-0.5 align-middle font-mono text-[9px] font-bold uppercase tracking-wide text-fg-muted">
      v2
    </span>
  );
}

export function HelpTip({ label, body, meta }: HelpMeta) {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLSpanElement>(null);
  useEffect(() => {
    if (!open) return;
    const onDoc = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false);
    };
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && setOpen(false);
    document.addEventListener("mousedown", onDoc);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("mousedown", onDoc);
      document.removeEventListener("keydown", onKey);
    };
  }, [open]);
  return (
    <span ref={ref} className="relative inline-flex align-middle">
      <button
        type="button"
        aria-label={`What “${label}” does`}
        aria-expanded={open}
        onClick={() => setOpen((o) => !o)}
        className="grid size-[17px] place-items-center rounded-full border border-line-strong bg-white/[0.04] font-mono text-[11px] font-bold leading-none text-fg-muted transition hover:border-brand/60 hover:text-brand focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-brand/40"
      >
        ?
      </button>
      {open && (
        <span className="absolute left-0 top-6 z-30 w-[270px] rounded-xl border border-line-strong bg-surface-2 p-3 text-[12.5px] font-normal leading-relaxed text-fg-soft shadow-[0_18px_44px_-20px_rgba(0,0,0,0.85)]">
          <span className="mb-1.5 block font-semibold text-fg">{label}</span>
          {body}
          {meta && (
            <span className="mt-2 block border-t border-line pt-1.5 font-mono text-[11px] text-fg-muted">
              {meta}
            </span>
          )}
        </span>
      )}
    </span>
  );
}

// Control-only switch, so a field row can put the label (with help + badges) above it.
export function Switch({
  checked,
  onChange,
  label,
}: {
  checked: boolean;
  onChange: (v: boolean) => void;
  label?: string;
}) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      aria-label={label}
      onClick={() => onChange(!checked)}
      className="inline-flex w-fit items-center gap-3 rounded-xl border border-line bg-white/[0.03] px-3.5 py-2.5 text-left transition hover:bg-white/[0.06] focus-visible:outline-none focus-visible:ring-4 focus-visible:ring-brand/20"
    >
      <span className={cx("relative h-5 w-9 shrink-0 rounded-full transition", checked ? "bg-brand" : "bg-white/15")}>
        <span
          className={cx(
            "absolute top-0.5 size-4 rounded-full bg-white shadow transition-all",
            checked ? "left-[18px]" : "left-0.5",
          )}
        />
      </span>
      <span className="text-[13px] font-medium text-fg-muted">{checked ? "On" : "Off"}</span>
    </button>
  );
}

function Label({
  children,
  applied,
  pending,
  advanced,
  help,
}: {
  children: ReactNode;
  applied?: boolean;
  pending?: boolean;
  advanced?: boolean;
  help?: HelpMeta;
}) {
  return (
    <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-[13px] font-semibold text-fg-muted">
      <span>{children}</span>
      {applied && <AppliedBadge />}
      {pending && <PendingBadge />}
      {advanced && (
        <span className="rounded border border-line px-1 py-px font-mono text-[9px] uppercase tracking-wider text-fg-muted/60">
          adv
        </span>
      )}
      {help && <HelpTip {...help} />}
    </div>
  );
}

const inputCls =
  "w-full rounded-xl border border-line bg-white/[0.04] px-3.5 py-2.5 text-[15px] text-fg placeholder:text-white/25 transition focus:border-brand/50 focus:bg-white/[0.06] focus:outline-none focus:ring-4 focus:ring-brand/15";

interface FieldLabelProps {
  label: string;
  applied?: boolean;
  pending?: boolean;
  advanced?: boolean;
  help?: HelpMeta;
  hint?: ReactNode;
}

export function TextField({
  label,
  applied,
  pending,
  advanced,
  help,
  hint,
  ...rest
}: FieldLabelProps & InputHTMLAttributes<HTMLInputElement>) {
  return (
    <div className="grid gap-1.5">
      <Label applied={applied} pending={pending} advanced={advanced} help={help}>
        {label}
      </Label>
      <input {...rest} className={inputCls} />
      {hint && <p className="text-xs text-fg-muted">{hint}</p>}
    </div>
  );
}

export function SelectField({
  label,
  applied,
  pending,
  advanced,
  help,
  children,
  ...rest
}: FieldLabelProps & { children: ReactNode } & SelectHTMLAttributes<HTMLSelectElement>) {
  return (
    <div className="grid gap-1.5">
      <Label applied={applied} pending={pending} advanced={advanced} help={help}>
        {label}
      </Label>
      <select {...rest} className={cx(inputCls, "[color-scheme:dark]")}>
        {children}
      </select>
    </div>
  );
}

export function ToggleField({
  label,
  applied,
  pending,
  advanced,
  help,
  checked,
  onChange,
}: FieldLabelProps & { checked: boolean; onChange: (v: boolean) => void }) {
  return (
    <div className="grid gap-1.5">
      <Label applied={applied} pending={pending} advanced={advanced} help={help}>
        {label}
      </Label>
      <Switch checked={checked} onChange={onChange} label={label} />
    </div>
  );
}

export function Toggle({
  label,
  checked,
  onChange,
}: {
  label: string;
  checked: boolean;
  onChange: (v: boolean) => void;
}) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      onClick={() => onChange(!checked)}
      className="flex items-center gap-3 rounded-xl border border-line bg-white/[0.03] px-3.5 py-2.5 text-left transition hover:bg-white/[0.06] focus-visible:outline-none focus-visible:ring-4 focus-visible:ring-brand/20"
    >
      <span
        className={cx(
          "relative h-5 w-9 shrink-0 rounded-full transition",
          checked ? "bg-brand" : "bg-white/15",
        )}
      >
        <span
          className={cx(
            "absolute top-0.5 size-4 rounded-full bg-white shadow transition-all",
            checked ? "left-[18px]" : "left-0.5",
          )}
        />
      </span>
      <span className="text-[14px] font-semibold text-fg-soft">{label}</span>
    </button>
  );
}
