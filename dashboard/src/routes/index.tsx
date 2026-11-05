import { Link, createFileRoute } from '@tanstack/react-router'
import { getDashboard } from '../lib/api'
import { errorMessage, formatBytes, formatDate, formatDuration } from '../lib/format'
import { MEETING_STATE_LABEL, MEETING_STATE_TONE, meetingLabel } from '../lib/meetings'

export const Route = createFileRoute('/')({
  loader: () => getDashboard(),
  component: Dashboard,
  errorComponent: ({ error }) => <DashboardError message={errorMessage(error)} />,
})

function Dashboard() {
  const { meetings } = Route.useLoaderData()

  return (
    <main className="page-shell">
      <section className="hero">
        <div>
          <p className="eyebrow">Sala de control</p>
          <h1>Lo que Sebastian<br /><em>escucha y responde.</em></h1>
          <p className="hero-copy">
            Los altavoces adoptados, sus reuniones y los agentes con los que hablan.
          </p>
        </div>
        <div className="signal-orbit" aria-hidden="true">
          <div className="orbit orbit-one" />
          <div className="orbit orbit-two" />
          <div className="signal-core">
            {Array.from({ length: 11 }, (_, index) => <i key={index} />)}
          </div>
        </div>
      </section>

      <section className="archive-section">
        <div className="section-heading">
          <div>
            <p className="eyebrow">Con el altavoz · RM-40</p>
            <h2>Reuniones</h2>
          </div>
          <Link to="/meetings" className="button-link">Todas las reuniones →</Link>
        </div>
        {meetings.length === 0 ? (
          <p className="device-meta">Ninguna todavía. Se graban desde la ficha del altavoz o con su botón MUTE (corta + larga).</p>
        ) : (
          <div className="recording-list">
            {meetings.map((m) => (
              <Link key={m.id} to="/meetings/$meetingId" params={{ meetingId: m.id }} className="recording-row">
                <span className="row-index">{MEETING_STATE_LABEL[m.state].slice(0, 2).toUpperCase()}</span>
                <span className={`kind-dot tone-${MEETING_STATE_TONE[m.state]}`} style={{ background: 'currentColor', boxShadow: 'none' }} />
                <span className="recording-primary">
                  <strong>{m.summary?.text ? m.summary.text.slice(0, 90) : `Reunión · ${MEETING_STATE_LABEL[m.state]}`}</strong>
                  <small>{meetingLabel(m)} · {m.deviceId}</small>
                </span>
                <span className="recording-date">{formatDate(m.startedAt ?? m.requestedAt)}</span>
                <span className="recording-duration">{formatDuration(m.durationMs)}</span>
                <span className="recording-size">{formatBytes(m.audioBytes)}</span>
                <span className="row-arrow" aria-hidden="true">↗</span>
              </Link>
            ))}
          </div>
        )}
      </section>
    </main>
  )
}

function DashboardError({ message }: { message: string }) {
  return (
    <main className="page-shell empty-page">
      <p className="eyebrow">Conexión interrumpida</p>
      <h1>No podemos leer la sala de control.</h1>
      <p className="hero-copy">{message}</p>
    </main>
  )
}
