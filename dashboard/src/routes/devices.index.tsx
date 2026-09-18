import { useEffect, useState } from 'react'
import { Link, createFileRoute, useRouter } from '@tanstack/react-router'
import { adoptDevice, forgetDevice, getAdoptionJob, getDevices, type AdoptionJob, type Device } from '../lib/api'
import { SecretDialog } from '../components/SecretDialog'
import { formatDate } from '../lib/format'
import {
  canAdopt,
  canForget,
  eventMessage,
  groupDevices,
  isValidIPv4,
  JOB_DONE,
  jobMessage,
  SECTION_TITLE,
  STATE_HINT,
  STATE_LABEL,
  STATE_TONE,
  timeline,
  TRANSITIONAL,
  type Section,
} from '../lib/fleet'

export const Route = createFileRoute('/devices/')({
  loader: () => getDevices(),
  component: Devices,
  errorComponent: ({ error }) => (
    <main className="page-shell empty-page">
      <p className="eyebrow">Dispositivos · Error</p>
      <h1>El inventario no responde.</h1>
      <p className="hero-copy">{error.message}</p>
    </main>
  ),
})

// One adoption/forget job per device on screen; polled until it finishes.
type Jobs = Record<string, AdoptionJob>

function Devices() {
  const { devices, room } = Route.useLoaderData()
  const router = useRouter()
  const [jobs, setJobs] = useState<Jobs>({})
  const [adoptByIp, setAdoptByIp] = useState(false)
  const [ip, setIp] = useState('')
  const [ipId, setIpId] = useState('')
  const [ipSecret, setIpSecret] = useState('')
  // Handover (RF-38): a unit bound to another organization is adopted with
  // its own device secret; asked inline when "Adoptar aquí" is pressed.
  const [handover, setHandover] = useState<{ deviceId: string; secret: string } | null>(null)
  const [error, setError] = useState<string | null>(null)
  // Relative times are computed on the client only (no SSR/hydration drift).
  const [now, setNow] = useState<Date | null>(null)

  // Reconciliation is device-paced (30 s polls) and the LAN view refreshes on
  // its own: refetch so states resolve on screen by themselves.
  useEffect(() => {
    setNow(new Date())
    const tick = setInterval(() => setNow(new Date()), 1_000)
    const timer = setInterval(() => void router.invalidate(), 5_000)
    return () => {
      clearInterval(tick)
      clearInterval(timer)
    }
  }, [router])

  useEffect(() => {
    const pending = Object.values(jobs).filter((job) => !JOB_DONE.has(job.phase))
    if (pending.length === 0) return
    const timer = setInterval(async () => {
      for (const job of pending) {
        try {
          const next = await getAdoptionJob({ data: job.id })
          setJobs((prev) => ({ ...prev, [job.deviceId]: next }))
          if (JOB_DONE.has(next.phase)) void router.invalidate()
        } catch {
          // job expired server-side: leave the last state on screen
        }
      }
    }, 2_000)
    return () => clearInterval(timer)
  }, [jobs, router])

  async function run(deviceId: string, start: () => Promise<AdoptionJob>) {
    setError(null)
    try {
      const job = await start()
      setJobs((prev) => ({ ...prev, [deviceId]: job }))
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }

  function adopt(device: Device) {
    const needsSecret = device.state === 'managed_elsewhere' || device.state === 'moved' || jobs[device.id]?.error === 'auth'
    if (needsSecret && handover?.deviceId !== device.id) {
      setHandover({ deviceId: device.id, secret: '' })
      return
    }
    const secret = handover?.deviceId === device.id ? handover.secret.trim() : ''
    setHandover(null)
    void run(device.id, () => adoptDevice({ data: { deviceId: device.id, ...(secret ? { deviceSecret: secret } : {}) } }))
  }

  function forget(device: Device) {
    const typed = window.prompt(`Olvidar ${device.displayName}: el altavoz vuelve a fábrica y desaparece del inventario (su historial se conserva). Escribe su id para confirmar:`)
    if (typed?.trim() !== device.id) return
    void run(device.id, () => forgetDevice({ data: { deviceId: device.id } }))
  }

  function submitAdoptByIp(event: React.FormEvent) {
    event.preventDefault()
    if (!isValidIPv4(ip) || !ipId.trim()) {
      setError('Indica la MAC del altavoz (12 hex) y una IPv4 válida.')
      return
    }
    const deviceId = ipId.trim().toLowerCase().replace(/[^0-9a-f]/g, '')
    const deviceSecret = ipSecret.trim()
    void run(deviceId, () => adoptDevice({ data: { deviceId, ip: ip.trim(), ...(deviceSecret ? { deviceSecret } : {}) } }))
    setAdoptByIp(false)
  }

  const groups = groupDevices(devices)
  const sections: Section[] = ['mine', 'attention', 'others']

  return (
    <main className="page-shell">
      <section className="archive-section">
        <div className="section-heading">
          <div>
            <p className="eyebrow">Flota · {room.name}</p>
            <h2>Dispositivos</h2>
          </div>
          <div className="device-toolbar">
            <span className="result-count">{devices.length} en total</span>
            <button type="button" className="chip-button" onClick={() => setAdoptByIp((v) => !v)}>
              Adoptar por IP
            </button>
            <a className="chip-button" href="/installer" target="_blank" rel="noreferrer">
              Instalador USB
            </a>
          </div>
        </div>

        {!room.discoveryEnabled && (
          <p className="device-notice">
            Este control room no puede escuchar la red local; solo se muestra el inventario. Usa <em>Adoptar por IP</em>.
          </p>
        )}
        {room.discoveryEnabled && (
          <p className="device-notice muted">
            Se descubren los altavoces de la subred de este control room. Para otras redes, <em>Adoptar por IP</em>.
          </p>
        )}
        {error && <p className="device-notice warn">{error}</p>}

        {adoptByIp && (
          <form className="adopt-ip" onSubmit={submitAdoptByIp}>
            <label>
              <span>MAC del altavoz</span>
              <input value={ipId} onChange={(e) => setIpId(e.target.value)} placeholder="68ee8f4d8dd4" autoComplete="off" />
            </label>
            <label>
              <span>IP</span>
              <input value={ip} onChange={(e) => setIp(e.target.value)} placeholder="10.0.100.40" inputMode="decimal" autoComplete="off" />
            </label>
            <label>
              <span>Secreto del altavoz (solo si es de otra organización)</span>
              <input value={ipSecret} onChange={(e) => setIpSecret(e.target.value)} type="password" autoComplete="off" />
            </label>
            <button type="submit" className="chip-button">Adoptar</button>
            <button type="button" className="chip-button" onClick={() => setAdoptByIp(false)}>Cancelar</button>
            {jobs[ipId.trim().toLowerCase()] && <JobLine job={jobs[ipId.trim().toLowerCase()]} />}
          </form>
        )}

        {devices.length === 0 ? (
          <div className="empty-state">
            <h3>Ningún altavoz a la vista</h3>
            <p>Aparecerán aquí al anunciarse en la red o al contactar con este control room.</p>
          </div>
        ) : (
          sections.map((section) =>
            groups[section].length === 0 ? null : (
              <div key={section} className="device-section">
                <h3 className="device-section-title">{SECTION_TITLE[section]}</h3>
                <div className="device-list">
                  {groups[section].map((item) => (
                    <DeviceRow
                      key={item.id}
                      device={item}
                      job={jobs[item.id]}
                      now={now}
                      handover={handover?.deviceId === item.id ? handover.secret : null}
                      onHandoverChange={(secret) => setHandover({ deviceId: item.id, secret })}
                      onHandoverCancel={() => setHandover(null)}
                      onAdopt={adopt}
                      onForget={forget}
                    />
                  ))}
                </div>
              </div>
            ),
          )
        )}
      </section>
    </main>
  )
}

// The unit's last event (RF-36/42), with the moment this control room saw it.
function EventLine({ device }: Readonly<{ device: Device }>) {
  const message = eventMessage(device, device.lastEventAt ? formatDate(device.lastEventAt) : '')
  if (!message) return null
  return <p className={`device-meta ${message.tone}`}>{message.text}</p>
}

function JobLine({ job }: Readonly<{ job: AdoptionJob }>) {
  const message = jobMessage(job)
  const [showSecret, setShowSecret] = useState(false)
  return (
    <p className={`device-job ${message.tone}`}>
      {message.text}
      {job.phase === 'adopted' && job.deviceSecret && (
        <>
          {' '}
          <Link to="/devices/$deviceId" params={{ deviceId: job.deviceId }} className="device-link">
            Ver la ficha
          </Link>
          <br />
          <span className="device-secret-once">
            Secreto del altavoz: <code>{job.deviceSecret}</code>{' '}
            <button type="button" className="chip-button" onClick={() => setShowSecret(true)}>Copiar o QR</button>
            <br />
            Es fijo: nace con la placa y no cambia al adoptarla. Se vuelve a leer por USB con «Load from device»; solo cambia si lo regeneras en la ficha.
          </span>
          {showSecret && <SecretDialog secret={job.deviceSecret} onClose={() => setShowSecret(false)} />}
        </>
      )}
    </p>
  )
}

function DeviceRow({
  device,
  job,
  now,
  handover,
  onHandoverChange,
  onHandoverCancel,
  onAdopt,
  onForget,
}: Readonly<{
  device: Device
  job?: AdoptionJob
  now: Date | null
  handover: string | null
  onHandoverChange: (secret: string) => void
  onHandoverCancel: () => void
  onAdopt: (device: Device) => void
  onForget: (device: Device) => void
}>) {
  const busy = job !== undefined && !JOB_DONE.has(job.phase)
  const mine = device.state === 'adopted' || device.state === 'absent' || device.state === 'joining' || device.state === 'moved'
  const tone = STATE_TONE[device.state]
  const facts = [device.ip, device.firmware ? `fw ${device.firmware}` : null, device.reportedProfile].filter(Boolean)

  return (
    <article className={`device-row state-${device.state} tone-${tone}`}>
      <div className="device-identity">
        <div className="device-head">
          {mine ? (
            <Link to="/devices/$deviceId" params={{ deviceId: device.id }} className="device-name mono">
              {device.displayName}
            </Link>
          ) : (
            <h3 className="mono device-name">{device.displayName}</h3>
          )}
          <span className={`device-state tone-${tone}`} title={STATE_HINT[device.state]}>
            {STATE_LABEL[device.state]}
          </span>
        </div>
        {facts.length > 0 && (
          <p className="device-facts">
            {facts.map((f) => (
              <span key={String(f)} className="device-fact">{f}</span>
            ))}
          </p>
        )}
        <p className="device-timeline">
          {now
            ? timeline(device, now).join(' · ')
            : device.profileReportedAt
              ? `último contacto ${formatDate(device.profileReportedAt)}`
              : 'sin contacto todavía'}
        </p>
        {(TRANSITIONAL.has(device.state) || device.state === 'orphan' || device.state === 'registered') && (
          <p className={`device-meta tone-${tone}`}>{STATE_HINT[device.state]}</p>
        )}
        {device.lastError && device.lastError !== 'ok' && (
          <p className="device-meta warn">Último contacto: {device.lastError}</p>
        )}
        <EventLine device={device} />
        {job && <JobLine job={job} />}
        {job?.phase === 'failed' && job.error === 'auth' && handover === null && (
          <button type="button" className="chip-button primary device-retry" onClick={() => onHandoverChange('')}>
            Reintentar con el secreto del altavoz
          </button>
        )}
        {handover !== null && (
          <form
            className="adopt-ip"
            onSubmit={(e) => {
              e.preventDefault()
              onAdopt(device)
            }}
          >
            <label>
              <span>{device.state === 'moved' ? 'Secreto del altavoz (si su nuevo dueño no es de tu organización)' : 'Secreto del altavoz (déjalo vacío si es de tu organización)'}</span>
              <input value={handover} onChange={(e) => onHandoverChange(e.target.value)} type="password" autoComplete="off" autoFocus />
            </label>
            <button type="submit" className="chip-button">{device.state === 'moved' ? 'Recuperar' : 'Adoptar aquí'}</button>
            <button type="button" className="chip-button" onClick={onHandoverCancel}>Cancelar</button>
          </form>
        )}
      </div>
      <div className="device-actions">
        {canAdopt(device.state) && handover === null && (
          <button type="button" className="chip-button" disabled={busy} onClick={() => onAdopt(device)}>
            {busy ? 'Adoptando…' : device.state === 'managed_elsewhere' ? 'Adoptar aquí' : device.state === 'moved' ? 'Recuperar' : 'Adoptar'}
          </button>
        )}
        {canForget(device.state) && (
          <button type="button" className="chip-button danger" disabled={busy} onClick={() => onForget(device)}>
            Olvidar
          </button>
        )}
      </div>
    </article>
  )
}
