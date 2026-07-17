import { Link, createFileRoute } from '@tanstack/react-router'
import { getRecording } from '../lib/api'
import { formatBytes, formatDate, formatDuration, kindLabel } from '../lib/format'

export const Route = createFileRoute('/recordings/$recordingId')({
  loader: ({ params }) => getRecording({ data: params.recordingId }),
  component: RecordingDetail,
})

function RecordingDetail() {
  const recording = Route.useLoaderData()

  return (
    <main className="page-shell detail-page">
      <Link to="/" className="back-link">← Volver al archivo</Link>
      <section className="detail-hero">
        <div>
          <p className="eyebrow">{kindLabel(recording.kind)}</p>
          <h1>{recording.fileName}</h1>
          <p className="detail-room">Sala {recording.room}</p>
        </div>
        <span className={`detail-kind kind-${recording.kind}`}>{recording.kind}</span>
      </section>

      <section className="player-card">
        <div className="player-visual" aria-hidden="true">
          {Array.from({ length: 48 }, (_, index) => (
            <i key={index} style={{ height: `${16 + ((index * 17) % 58)}%` }} />
          ))}
        </div>
        <audio controls preload="metadata" src={recording.objectUrl}>
          Tu navegador no puede reproducir este audio.
        </audio>
      </section>

      <section className="detail-grid">
        <Detail label="Capturada" value={formatDate(recording.capturedAt)} />
        <Detail label="Duración" value={formatDuration(recording.durationMs)} />
        <Detail label="Tamaño" value={formatBytes(recording.byteSize)} />
        <Detail label="Formato" value={recording.contentType} />
        <Detail label="Sesión" value={recording.sessionId} mono />
        <Detail label="Identificador" value={recording.id} mono />
      </section>

      <section className="transcript-card">
        <p className="eyebrow">Transcripción</p>
        <p>{recording.transcript || 'Esta grabación todavía no tiene una transcripción asociada.'}</p>
      </section>
    </main>
  )
}

function Detail({ label, value, mono = false }: { label: string; value: string; mono?: boolean }) {
  return <div className="detail-item"><span>{label}</span><strong className={mono ? 'mono' : undefined}>{value}</strong></div>
}
