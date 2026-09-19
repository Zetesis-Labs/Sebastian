import { useEffect, useState } from 'react'
import { Link, createFileRoute, useNavigate, useRouter } from '@tanstack/react-router'
import { getMeetings, stopMeeting, type Meeting } from '../lib/api'
import { formatDate, formatDuration } from '../lib/format'
import { MEETING_STATE_LABEL, MEETING_STATE_TONE, inProgress, isBusy, liveDuration, meetingLabel, type MeetingState } from '../lib/meetings'

type Search = { state?: MeetingState; q?: string; deviceId?: string }

const STATES: MeetingState[] = ['recording', 'transcribing', 'ready', 'no_transcript', 'cut']

export const Route = createFileRoute('/meetings/')({
  validateSearch: (search: Record<string, unknown>): Search => ({
    ...(typeof search.state === 'string' && (STATES as string[]).includes(search.state) ? { state: search.state as MeetingState } : {}),
    ...(typeof search.q === 'string' && search.q.trim() ? { q: search.q.trim() } : {}),
    ...(typeof search.deviceId === 'string' && search.deviceId ? { deviceId: search.deviceId } : {}),
  }),
  loaderDeps: ({ search }) => search,
  loader: ({ deps }) => getMeetings({ data: deps }),
  component: Meetings,
  errorComponent: ({ error }) => (
    <main className="page-shell empty-page">
      <p className="eyebrow">Reuniones · Error</p>
      <h1>No se pueden leer las reuniones.</h1>
      <p className="hero-copy">{error.message}</p>
    </main>
  ),
})

function Meetings() {
  const meetings = Route.useLoaderData()
  const search = Route.useSearch()
  const navigate = useNavigate()
  const router = useRouter()
  const [q, setQ] = useState(search.q ?? '')
  const [now, setNow] = useState<Date | null>(null)
  const [msg, setMsg] = useState<string | null>(null)
  const busy = meetings.some((m) => isBusy(m.state))

  useEffect(() => {
    setNow(new Date())
    const tick = setInterval(() => setNow(new Date()), 1_000)
    const timer = busy ? setInterval(() => void router.invalidate(), 5_000) : undefined
    return () => {
      clearInterval(tick)
      if (timer) clearInterval(timer)
    }
  }, [router, busy])

  async function stop(m: Meeting) {
    setMsg(null)
    try {
      await stopMeeting({ data: { meetingId: m.id } })
      await router.invalidate()
    } catch (e) {
      setMsg(e instanceof Error ? e.message : String(e))
    }
  }

  return (
    <main className="page-shell detail-page">
      <section className="section-heading meetings-heading">
        <div>
          <p className="eyebrow">Reuniones grabadas con el altavoz</p>
          <h2>Reuniones</h2>
        </div>
        <span className="result-count">{meetings.length} mostradas</span>
      </section>

      <form
        className="meeting-filters"
        onSubmit={(e) => {
          e.preventDefault()
          void navigate({ to: '/meetings', search: { ...search, q: q.trim() || undefined } })
        }}
      >
        <label>
          <span>Estado</span>
          <select value={search.state ?? ''} onChange={(e) => void navigate({ to: '/meetings', search: { ...search, state: (e.target.value || undefined) as MeetingState | undefined } })}>
            <option value="">todos</option>
            {STATES.map((s) => (
              <option key={s} value={s}>{MEETING_STATE_LABEL[s]}</option>
            ))}
          </select>
        </label>
        <label>
          <span>Buscar en transcripciones y resúmenes</span>
          <input value={q} onChange={(e) => setQ(e.target.value)} placeholder="una palabra, un nombre…" />
        </label>
        <button type="submit" className="chip-button">Buscar</button>
        {(search.q || search.state || search.deviceId) && (
          <Link to="/meetings" search={{}} className="chip-button">Quitar filtros</Link>
        )}
        {search.deviceId && <span className="device-meta">Solo el altavoz <code>{search.deviceId}</code></span>}
      </form>

      {msg && <p className="device-notice warn">{msg}</p>}

      {meetings.length === 0 ? (
        <div className="empty-state">
          <div className="empty-wave" aria-hidden="true"><i /><i /><i /><i /><i /></div>
          <h3>No hay reuniones{search.q ? ` con “${search.q}”` : ''}</h3>
          <p>Se graba desde la ficha del altavoz, con el botón MUTE (corta + larga) o pidiéndoselo a Sebastián.</p>
        </div>
      ) : (
        <div className="recording-list">
          {meetings.map((m) => (
            <div key={m.id} className={`meeting-row tone-${MEETING_STATE_TONE[m.state]}`}>
              <Link to="/meetings/$meetingId" params={{ meetingId: m.id }} className="meeting-main">
                <span className={`device-state tone-${MEETING_STATE_TONE[m.state]}`}>{MEETING_STATE_LABEL[m.state]}</span>
                <strong>{formatDate(m.startedAt ?? m.requestedAt)}</strong>
                <small>{meetingLabel(m)}</small>
                {m.summary && <small className="meeting-summary-preview">{m.summary.text}</small>}
              </Link>
              <span className="recording-date"><Link to="/devices/$deviceId" params={{ deviceId: m.deviceId }} className="mono">{m.deviceId}</Link></span>
              <span className="recording-duration">
                {m.state === 'recording' && m.startedAt && now ? formatDuration(liveDuration(m.startedAt, now)) : formatDuration(m.durationMs)}
              </span>
              <span className="meeting-actions">
                {inProgress(m.state) && (
                  <button type="button" className="chip-button danger" onClick={() => void stop(m)}>Parar</button>
                )}
              </span>
            </div>
          ))}
        </div>
      )}
    </main>
  )
}
