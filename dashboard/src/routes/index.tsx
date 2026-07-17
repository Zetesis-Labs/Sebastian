import { Link, createFileRoute } from '@tanstack/react-router'
import { getDashboard } from '../lib/api'
import { formatBytes, formatDate, formatDuration, kindLabel } from '../lib/format'

export const Route = createFileRoute('/')({
  loader: () => getDashboard(),
  component: Dashboard,
  errorComponent: ({ error }) => <DashboardError message={error.message} />,
})

function Dashboard() {
  const { recordings, summary } = Route.useLoaderData()

  return (
    <main className="page-shell">
      <section className="hero">
        <div>
          <p className="eyebrow">Archivo de voz · Tiempo real</p>
          <h1>Lo que Sebastian<br /><em>escucha y responde.</em></h1>
          <p className="hero-copy">
            Un registro legible de cada conversación: entrada al modelo, voz del agente
            y contexto técnico de la sesión.
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

      <section className="metrics" aria-label="Resumen de grabaciones">
        <Metric label="Grabaciones" value={new Intl.NumberFormat('es-ES').format(summary.count)} note="archivos catalogados" />
        <Metric label="Tiempo de voz" value={formatDuration(summary.totalDurationMs)} note="duración acumulada" />
        <Metric label="Almacenamiento" value={formatBytes(summary.totalBytes)} note="audio conservado" />
        <Metric
          label="Última captura"
          value={summary.lastCapturedAt ? formatDate(summary.lastCapturedAt) : 'Sin actividad'}
          note="hora local de Madrid"
          compact
        />
      </section>

      <section className="archive-section">
        <div className="section-heading">
          <div>
            <p className="eyebrow">Sesiones recientes</p>
            <h2>Grabaciones</h2>
          </div>
          <span className="result-count">{recordings.length} mostradas</span>
        </div>

        {recordings.length === 0 ? (
          <div className="empty-state">
            <div className="empty-wave" aria-hidden="true"><i /><i /><i /><i /><i /></div>
            <h3>Aún no hay grabaciones</h3>
            <p>El catálogo se llenará cuando el agente registre su primer archivo de audio.</p>
          </div>
        ) : (
          <div className="recording-list">
            {recordings.map((recording, index) => (
              <Link
                key={recording.id}
                to="/recordings/$recordingId"
                params={{ recordingId: recording.id }}
                className="recording-row"
              >
                <span className="row-index">{String(index + 1).padStart(2, '0')}</span>
                <span className={`kind-dot kind-${recording.kind}`} />
                <span className="recording-primary">
                  <strong>{kindLabel(recording.kind)}</strong>
                  <small>{recording.room}</small>
                </span>
                <span className="recording-date">{formatDate(recording.capturedAt)}</span>
                <span className="recording-duration">{formatDuration(recording.durationMs)}</span>
                <span className="recording-size">{formatBytes(recording.byteSize)}</span>
                <span className="row-arrow" aria-hidden="true">↗</span>
              </Link>
            ))}
          </div>
        )}
      </section>
    </main>
  )
}

function Metric({ label, value, note, compact = false }: { label: string; value: string; note: string; compact?: boolean }) {
  return (
    <article className="metric-card">
      <span>{label}</span>
      <strong className={compact ? 'compact-value' : undefined}>{value}</strong>
      <small>{note}</small>
    </article>
  )
}

function DashboardError({ message }: { message: string }) {
  return (
    <main className="page-shell empty-page">
      <p className="eyebrow">Conexión interrumpida</p>
      <h1>No podemos leer el archivo de voz.</h1>
      <p className="hero-copy">{message}</p>
    </main>
  )
}
