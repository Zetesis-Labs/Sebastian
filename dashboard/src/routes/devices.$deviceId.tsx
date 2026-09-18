import { useEffect, useMemo, useState } from 'react'
import { Link, createFileRoute, useRouter } from '@tanstack/react-router'
import {
  clearDesiredConfig,
  getDevice,
  regenerateSecret,
  renameDevice,
  setDesiredConfig,
  setDeviceProfile,
  type DeviceDetail,
  type Json,
} from '../lib/api'
import { formatDate } from '../lib/format'
import { GOVERNABLE_PATHS, REFLASH_ONLY, STATE_HINT, STATE_LABEL, STATE_TONE, configSync, desiredDocument, shortRoom, timeline } from '../lib/fleet'
import { defaultConfig, mergeConfig, type DeviceConfig } from '@installer/config'
import { MODES, SHARED, getField, setField, type FieldMeta } from '@installer/modes'
import { validate } from '@installer/validate'
import { SecretDialog } from '../components/SecretDialog'

export const Route = createFileRoute('/devices/$deviceId')({
  loader: ({ params }) => getDevice({ data: params.deviceId }),
  component: DevicePage,
  errorComponent: ({ error }) => (
    <main className="page-shell empty-page">
      <p className="eyebrow">Dispositivo · Error</p>
      <h1>No se pudo cargar la ficha.</h1>
      <p className="hero-copy">{error.message}</p>
      <Link to="/devices" className="button-link">Volver a la flota</Link>
    </main>
  ),
})

const QUICK_PROFILES = ['agente', 'micro-usb']

// The desired document the form edits, seeded from what the control room
// holds (or the installer defaults) — never from secrets.
function seedForm(detail: DeviceDetail): DeviceConfig {
  const base = detail.desiredConfig ? mergeConfig(detail.desiredConfig) : defaultConfig()
  return { ...base, wifi: { ...base.wifi, password: '' } }
}

function DevicePage() {
  const detail = Route.useLoaderData() as DeviceDetail
  const router = useRouter()
  const [form, setForm] = useState<DeviceConfig>(() => seedForm(detail))
  const [dirty, setDirty] = useState(false)
  const [name, setName] = useState(detail.displayName)
  const [msg, setMsg] = useState<{ text: string; tone: 'ok' | 'warn' | 'info' } | null>(null)
  const [secret, setSecret] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const issues = useMemo(() => validate(form), [form])
  const [now, setNow] = useState<Date | null>(null)

  useEffect(() => {
    setNow(new Date())
    const tick = setInterval(() => setNow(new Date()), 1_000)
    const timer = setInterval(() => void router.invalidate(), 5_000)
    return () => {
      clearInterval(tick)
      clearInterval(timer)
    }
  }, [router])

  // A fresh load (after the device applied) reseeds the form unless the
  // operator is mid-edit.
  useEffect(() => {
    if (!dirty) setForm(seedForm(detail))
  }, [detail, dirty])

  const sync = configSync(detail, new Date())
  const running = detail.reportedConfigVersion ?? '—'
  const desired = detail.desiredConfigVersion ?? '—'

  async function act(label: string, fn: () => Promise<unknown>, done: string) {
    setBusy(true)
    setMsg({ text: label, tone: 'info' })
    try {
      await fn()
      setMsg({ text: done, tone: 'ok' })
      await router.invalidate()
    } catch (e) {
      setMsg({ text: e instanceof Error ? e.message : String(e), tone: 'warn' })
    } finally {
      setBusy(false)
    }
  }

  function update(path: string, value: unknown) {
    setDirty(true)
    setForm((prev) => setField(prev, path, value))
  }

  function save() {
    if (issues.some((i) => i.severity === 'error' && GOVERNABLE_PATHS.has(i.path))) {
      setMsg({ text: 'Corrige los campos marcados antes de guardar.', tone: 'warn' })
      return
    }
    const doc = desiredDocument(form as unknown as Record<string, unknown>) as { [key: string]: Json }
    void act('Guardando la configuración deseada…', () => setDesiredConfig({ data: { deviceId: detail.id, config: doc } }), 'Guardada. El altavoz la aplicará en su próximo poll (30 s) y se reiniciará.').then(() => setDirty(false))
  }

  const mode = MODES.find((m) => m.id === form.mode) ?? MODES[0]
  const fields: FieldMeta[] = [...mode.fields, ...SHARED.flatMap((g) => g.fields)]

  return (
    <main className="page-shell detail-page">
      <Link to="/devices" className="back-link">← Volver a la flota</Link>
      <section className="detail-hero device-hero">
        <div>
          <p className={`eyebrow tone-${STATE_TONE[detail.state]}`}>{STATE_LABEL[detail.state]}</p>
          <form
            className="device-rename"
            onSubmit={(e) => {
              e.preventDefault()
              void act('Renombrando…', () => renameDevice({ data: { deviceId: detail.id, displayName: name } }), 'Nombre guardado.')
            }}
          >
            <input className="device-name-input" value={name} onChange={(e) => setName(e.target.value)} aria-label="Nombre" />
            {name !== detail.displayName && (
              <button type="submit" className="chip-button" disabled={busy}>Guardar nombre</button>
            )}
          </form>
          <p className="detail-room mono">{detail.id}</p>
          <p className="hero-copy">{STATE_HINT[detail.state]}</p>
          {now && <p className="device-timeline">{timeline(detail, now).join(' · ')}</p>}
        </div>
        <span className={`detail-kind tone-${STATE_TONE[detail.state]}`}>{detail.state}</span>
      </section>

      <section className="detail-grid">
        <Detail label="Firmware" value={detail.firmware ?? '—'} />
        <Detail label="IP" value={detail.ip ?? '—'} />
        <Detail label="Perfil" value={detail.reportedProfile ?? '—'} />
        <Detail label="Último contacto" value={detail.profileReportedAt ? formatDate(detail.profileReportedAt) : 'nunca'} />
        <Detail label="Adoptado" value={detail.adoptedAt ? formatDate(detail.adoptedAt) : 'no'} />
        <Detail label="En la red" value={detail.seenOnLanAt ? `${formatDate(detail.seenOnLanAt)}${detail.controlRoom ? ` · ${shortRoom(detail.controlRoom)}` : ' · sin control room'}` : 'no se ve'} />
        <Detail label="Último error" value={detail.lastError && detail.lastError !== 'ok' ? detail.lastError : 'ninguno'} />
      </section>

      {msg && <p className={`device-notice ${msg.tone}`}>{msg.text}</p>}

      <section className="device-panel">
        <div className="section-heading">
          <div>
            <p className="eyebrow">Perfil</p>
            <h2>Personalidad del firmware</h2>
          </div>
        </div>
        <div className="device-actions">
          {QUICK_PROFILES.map((profile) => (
            <button
              key={profile}
              type="button"
              className="chip-button"
              disabled={busy || profile === (detail.desiredProfile ?? detail.reportedProfile)}
              onClick={() => void act(`Pidiendo el perfil ${profile}…`, () => setDeviceProfile({ data: { deviceId: detail.id, name: profile } }), `Perfil ${profile} pedido; se aplica en el próximo poll.`)}
            >
              {profile}
            </button>
          ))}
          {detail.desiredProfile && detail.desiredProfile !== detail.reportedProfile && (
            <span className="device-pending">aplicando “{detail.desiredProfile}” en el próximo poll…</span>
          )}
        </div>
      </section>

      <section className="device-panel">
        <div className="section-heading">
          <div>
            <p className="eyebrow">Configuración</p>
            <h2>En ejecución frente a deseada</h2>
          </div>
          <span className={`device-sync sync-${sync}`}>
            {sync === 'synced' && `sincronizado · ${running}`}
            {sync === 'applying' && `aplicando ${desired} (ejecuta ${running})…`}
            {sync === 'stale' && `no aplicada: pide ${desired}, ejecuta ${running}`}
            {sync === 'none' && `sin configuración deseada · ejecuta ${running}`}
          </span>
        </div>
        <div className="config-form">
          <label className="config-field">
            <span>Modo</span>
            <select value={form.mode} onChange={(e) => update('mode', e.target.value)}>
              {MODES.map((m) => (
                <option key={m.id} value={m.id}>{m.title}</option>
              ))}
            </select>
          </label>
          {fields.map((f) => (
            <Field key={f.path} f={f} form={form} onChange={update} issue={issues.find((i) => i.path === f.issuePath)?.message} />
          ))}
        </div>
        <p className="device-meta">
          La contraseña WiFi solo viaja si escribes una nueva. Un cambio de WiFi se aplica como prueba: si el altavoz no obtiene IP en 2 minutos, vuelve a la red anterior.
        </p>
        <div className="device-actions">
          <button type="button" className="chip-button primary" disabled={busy} onClick={save}>Guardar como deseada</button>
          {detail.desiredConfigVersion && (
            <button type="button" className="chip-button" disabled={busy} onClick={() => void act('Descartando…', () => clearDesiredConfig({ data: { deviceId: detail.id } }), 'Configuración deseada descartada.').then(() => setDirty(false))}>
              Descartar la deseada
            </button>
          )}
        </div>
      </section>

      <section className="device-panel">
        <div className="section-heading">
          <div>
            <p className="eyebrow">Secreto del altavoz</p>
            <h2>Credenciales</h2>
          </div>
        </div>
        <p className="device-meta">
          {detail.hasDeviceSecret
            ? 'Este altavoz abre sesiones con un secreto propio. Regenerarlo emite otro que le llega en su próxima configuración; el antiguo vale hasta que use el nuevo.'
            : 'Sin secreto de altavoz: abre sesiones por el endpoint legado. Adóptalo para emitir uno.'}
        </p>
        <div className="device-actions">
          <button type="button" className="chip-button" disabled={busy || !detail.hasDeviceSecret} onClick={() => void act('Generando…', async () => { const r = await regenerateSecret({ data: { deviceId: detail.id } }); setSecret(r.deviceSecret) }, 'Secreto regenerado.')}>
            Regenerar secreto
          </button>
        </div>
        {secret && <SecretDialog secret={secret} onClose={() => setSecret(null)} />}
      </section>

      <section className="device-panel">
        <div className="section-heading">
          <div>
            <p className="eyebrow">Historial</p>
            <h2>Sesiones de este altavoz</h2>
          </div>
          <span className="result-count">{detail.sessions.length} recientes</span>
        </div>
        {detail.sessions.length === 0 ? (
          <p className="device-meta">Aún no ha abierto ninguna sesión atribuida a su id.</p>
        ) : (
          <div className="device-list">
            {detail.sessions.map((s) => (
              <article key={s.id} className="device-row">
                <div>
                  <p className="mono device-name">{s.room}</p>
                  <p className="device-meta">{formatDate(s.createdAt)} · {s.recordingCount} grabaciones</p>
                </div>
              </article>
            ))}
          </div>
        )}
      </section>
    </main>
  )
}

function Field({ f, form, onChange, issue }: Readonly<{ f: FieldMeta; form: DeviceConfig; onChange: (path: string, value: unknown) => void; issue?: string }>) {
  const value = getField(form, f.path)
  const locked = REFLASH_ONLY[f.path]
  const governable = GOVERNABLE_PATHS.has(f.path)
  const disabled = Boolean(locked) || !governable
  const hint = locked ?? (!governable ? 'Este campo no lo aplica el firmware (solo el instalador / token server).' : issue)
  if (f.type === 'toggle') {
    return (
      <label className="config-field">
        <span>{f.label}</span>
        <input type="checkbox" checked={Boolean(value)} disabled={disabled} onChange={(e) => onChange(f.path, e.target.checked)} />
        {hint && <small>{hint}</small>}
      </label>
    )
  }
  if (f.type === 'enum') {
    return (
      <label className="config-field">
        <span>{f.label}</span>
        <select value={String(value)} disabled={disabled} onChange={(e) => onChange(f.path, e.target.value)}>
          {f.options?.map((o) => (
            <option key={o.value} value={o.value}>{o.label}</option>
          ))}
        </select>
        {hint && <small>{hint}</small>}
      </label>
    )
  }
  const isNumber = f.type === 'number'
  return (
    <label className="config-field">
      <span>{f.label}</span>
      <input
        type={f.type === 'password' ? 'password' : isNumber ? 'number' : 'text'}
        value={value === undefined || value === null ? '' : String(value)}
        placeholder={f.path === 'wifi.password' ? 'sin cambios' : f.placeholder}
        disabled={disabled}
        autoComplete="off"
        onChange={(e) => onChange(f.path, isNumber ? Number(e.target.value) || 0 : e.target.value)}
      />
      {hint && <small className={issue ? 'warn' : ''}>{hint}</small>}
    </label>
  )
}

function Detail({ label, value }: Readonly<{ label: string; value: string }>) {
  return (
    <div className="detail-item">
      <span>{label}</span>
      <strong>{value}</strong>
    </div>
  )
}
