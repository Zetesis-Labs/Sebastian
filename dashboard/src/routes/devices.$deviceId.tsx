import { useEffect, useMemo, useState } from 'react'
import { Link, createFileRoute, useRouter } from '@tanstack/react-router'
import {
  clearDesiredConfig,
  getDevice,
  regenerateSecret,
  renameDevice,
  setDesiredConfig,
  setDeviceProfile,
  type ControlRoomPublic,
  type DeviceDetail,
  type Json,
} from '../lib/api'
import { formatDate } from '../lib/format'
import { GOVERNABLE_PATHS, STATE_HINT, STATE_LABEL, STATE_TONE, configSync, desiredDocument, differsFromRunning, fichaIssues, formGroups, runningValue, seedFromRoom, shortRoom, timeAgo, timeline } from '../lib/fleet'
import { applyMode, defaultConfig, mergeConfig, type DeviceConfig, type OperatingMode } from '@installer/config'
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

// The document the form edits: the desired one if this control room holds
// one, otherwise what the unit reports it runs (RF-42), otherwise what an
// adoption from here wrote. Never secrets, never a password.
function seedForm(detail: DeviceDetail, room: ControlRoomPublic): DeviceConfig {
  const source = detail.desiredConfig ?? detail.runningConfig
  const base = source ? mergeConfig(source) : defaultConfig()
  return seedFromRoom({ ...base, wifi: { ...base.wifi, password: '' } }, room)
}

const FIELD_BY_PATH = new Map<string, FieldMeta>(
  [...MODES.flatMap((m) => m.fields), ...SHARED.flatMap((g) => g.fields)].map((f) => [f.path, f] as const),
)

function DevicePage() {
  const { detail: loaded, room } = Route.useLoaderData() as { detail: DeviceDetail; room: ControlRoomPublic }
  const detail = loaded
  const router = useRouter()
  const [form, setForm] = useState<DeviceConfig>(() => seedForm(detail, room))
  const [dirty, setDirty] = useState(false)
  const [name, setName] = useState(detail.displayName)
  const [msg, setMsg] = useState<{ text: string; tone: 'ok' | 'warn' | 'info' } | null>(null)
  const [secret, setSecret] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const issues = useMemo(() => fichaIssues(validate(form), form), [form])
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
    if (!dirty) setForm(seedForm(detail, room))
  }, [detail, room, dirty])

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
          <span className="device-meta">
            {(detail.desiredProfile ?? detail.reportedProfile) === 'micro-usb'
              ? 'Micro USB: el altavoz es un micrófono para el ordenador; sigue en la red para gobernarlo desde aquí.'
              : 'Agente: conversa con Sebastian por LiveKit.'}
          </span>
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
            {detail.runningConfigAt && now
              ? ` · reportada ${timeAgo(detail.runningConfigAt, now)}`
              : ' · el altavoz aún no ha reportado su configuración'}
          </span>
        </div>
        {formGroups(detail.desiredProfile ?? detail.reportedProfile).map((group) => (
          <fieldset key={group.title} className="config-group">
            <legend>
              <span>{group.title}</span>
              <small>{group.hint}</small>
            </legend>
            <div className="config-form">
              {group.mode && (
                <label className="config-field">
                  <span>Modo</span>
                  <select
                    value={form.mode}
                    onChange={(e) => {
                      setDirty(true)
                      setForm((prev) => applyMode(prev, e.target.value as OperatingMode))
                    }}
                  >
                    {MODES.map((m) => (
                      <option key={m.id} value={m.id}>{m.title}</option>
                    ))}
                  </select>
                  <small>{MODES.find((m) => m.id === form.mode)?.sub}</small>
                </label>
              )}
              {group.paths.map((path) => {
                const f = FIELD_BY_PATH.get(path)
                return f ? (
                  <Field
                    key={f.path}
                    f={f}
                    form={form}
                    onChange={update}
                    issue={issues.find((i) => i.path === (f.issuePath ?? f.path))?.message}
                    running={differsFromRunning(detail.runningConfig, f.path, getField(form, f.path)) ? String(runningValue(detail.runningConfig, f.path)) : undefined}
                  />
                ) : null
              })}
            </div>
          </fieldset>
        ))}
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

function Field({ f, form, onChange, issue, running }: Readonly<{ f: FieldMeta; form: DeviceConfig; onChange: (path: string, value: unknown) => void; issue?: string; running?: string }>) {
  const value = getField(form, f.path)
  const hint = issue ?? (running !== undefined ? `En el altavoz ahora: ${running}` : undefined) ?? HINT[f.path] ?? f.help
  if (f.type === 'toggle') {
    return (
      <label className={`config-field config-toggle${running !== undefined ? ' differs' : ''}`}>
        <span>{f.label}</span>
        <input type="checkbox" checked={Boolean(value)} onChange={(e) => onChange(f.path, e.target.checked)} />
        {hint && <small>{hint}</small>}
      </label>
    )
  }
  if (f.type === 'enum') {
    return (
      <label className={`config-field${running !== undefined ? ' differs' : ''}`}>
        <span>{f.label}</span>
        <select value={String(value)} onChange={(e) => onChange(f.path, e.target.value)}>
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
    <label className={`config-field${running !== undefined ? ' differs' : ''}`}>
      <span>{f.label}</span>
      <input
        type={f.type === 'password' ? 'password' : isNumber ? 'number' : 'text'}
        value={value === undefined || value === null ? '' : String(value)}
        placeholder={PLACEHOLDER[f.path] ?? f.placeholder}
        autoComplete="off"
        onChange={(e) => onChange(f.path, isNumber ? Number(e.target.value) || 0 : e.target.value)}
      />
      {hint && <small className={issue ? 'warn' : running !== undefined ? 'differs' : ''}>{hint}</small>}
    </label>
  )
}

// What the operator reads under each field in the ficha (the installer's help
// is written for a unit in hand; here the unit is remote).
const HINT: Record<string, string> = {
  'wifi.ssid': 'Vacío: conserva la red guardada en el altavoz.',
  'wifi.password': 'Solo viaja si escribes una nueva. Un cambio de red se prueba 2 min y, si no obtiene IP, vuelve a la anterior.',
  'livekit.tokenServerUrl': 'Cambiarlo mueve el altavoz a otro control room.',
  'telemetry.syslogIp': 'Receptor UDP de los logs del altavoz. Vacío: se quedan en la placa.',
}

const PLACEHOLDER: Record<string, string> = {
  'wifi.ssid': 'sin cambios',
  'wifi.password': 'sin cambios',
}

function Detail({ label, value }: Readonly<{ label: string; value: string }>) {
  return (
    <div className="detail-item">
      <span>{label}</span>
      <strong>{value}</strong>
    </div>
  )
}
