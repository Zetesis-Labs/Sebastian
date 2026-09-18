import { useEffect, useRef, useState } from 'react'
import { Link, createFileRoute, useNavigate, useRouter } from '@tanstack/react-router'
import { deleteMeeting, getMeeting, resummarizeMeeting, retranscribeMeeting, stopMeeting, updateMeeting } from '../lib/api'
import { formatBytes, formatDate, formatDuration } from '../lib/format'
import { MEETING_STATE_LABEL, MEETING_STATE_TONE, activeSegment, clock, inProgress, isBusy, liveDuration, meetingLabel, speakerName, speakersOf, type MeetingTranscript } from '../lib/meetings'

export const Route = createFileRoute('/meetings/$meetingId')({
  loader: ({ params }) => getMeeting({ data: params.meetingId }),
  component: MeetingPage,
  errorComponent: ({ error }) => (
    <main className="page-shell empty-page">
      <p className="eyebrow">Reunión · Error</p>
      <h1>No se pudo cargar la reunión.</h1>
      <p className="hero-copy">{error.message}</p>
      <Link to="/meetings" className="button-link">Volver a las reuniones</Link>
    </main>
  ),
})

function MeetingPage() {
  const m = Route.useLoaderData()
  const router = useRouter()
  const navigate = useNavigate()
  const audioRef = useRef<HTMLAudioElement>(null)
  const [t, setT] = useState(0)
  // The player's source is fetched as a blob: the dashboard's proxy carries
  // the admin secret (RM-47) and a plain fetch is what reaches it in every
  // environment (the media loader's own request does not in dev).
  const [audioSrc, setAudioSrc] = useState<string | null>(null)
  const [audioError, setAudioError] = useState<string | null>(null)
  const [now, setNow] = useState<Date | null>(null)
  const [busy, setBusy] = useState(false)
  const [msg, setMsg] = useState<{ text: string; tone: 'ok' | 'warn' | 'info' } | null>(null)
  const [confirmDelete, setConfirmDelete] = useState(false)
  const live = isBusy(m.state)

  useEffect(() => {
    setNow(new Date())
    const tick = setInterval(() => setNow(new Date()), 1_000)
    const timer = live ? setInterval(() => void router.invalidate(), 5_000) : undefined
    return () => {
      clearInterval(tick)
      if (timer) clearInterval(timer)
    }
  }, [router, live])

  const wantsAudio = m.hasAudio && !inProgress(m.state)
  useEffect(() => {
    if (!wantsAudio) return
    let url: string | null = null
    let cancelled = false
    setAudioError(null)
    fetch(`/meetings/${m.id}/audio`)
      .then(async (res) => {
        if (!res.ok) throw new Error(`El audio no se pudo leer (${res.status}).`)
        const blob = await res.blob()
        if (cancelled) return
        url = URL.createObjectURL(blob)
        setAudioSrc(url)
      })
      .catch((e: unknown) => { if (!cancelled) setAudioError(e instanceof Error ? e.message : String(e)) })
    return () => {
      cancelled = true
      if (url) URL.revokeObjectURL(url)
      setAudioSrc(null)
    }
  }, [m.id, wantsAudio, m.audioBytes])

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

  const transcript = m.transcript
  const active = transcript ? activeSegment(transcript.segments, t) : -1
  const duration = m.state === 'recording' && m.startedAt && now ? liveDuration(m.startedAt, now) : m.durationMs

  return (
    <main className="page-shell detail-page">
      <Link to="/meetings" className="back-link">← Volver a las reuniones</Link>
      <section className="detail-hero device-hero">
        <div>
          <p className={`eyebrow tone-${MEETING_STATE_TONE[m.state]}`}>{meetingLabel(m)}</p>
          <h1>Reunión del {formatDate(m.startedAt ?? m.requestedAt)}</h1>
          <p className="detail-room mono">{m.id}</p>
          <p className="hero-copy">
            Grabada con el altavoz <Link to="/devices/$deviceId" params={{ deviceId: m.deviceId }} className="device-link mono">{m.deviceId}</Link>
            {m.requestedBy === 'gesture' ? ' desde su botón' : m.requestedBy === 'voice' ? ' por voz' : ' desde el control room'}.
          </p>
        </div>
        <span className={`detail-kind tone-${MEETING_STATE_TONE[m.state]}`}>{MEETING_STATE_LABEL[m.state]}</span>
      </section>

      <section className="detail-grid">
        <Detail label="Duración" value={formatDuration(duration)} />
        <Detail label="Audio" value={m.hasAudio ? formatBytes(m.audioBytes) : m.deletedAt ? 'borrado' : 'sin audio'} />
        <Detail label="Fin" value={m.endedAt ? formatDate(m.endedAt) : '—'} />
      </section>

      {msg && <p className={`device-notice ${msg.tone}`}>{msg.text}</p>}

      <section className="device-actions meeting-toolbar">
        {inProgress(m.state) && (
          <button type="button" className="chip-button danger" disabled={busy} onClick={() => void act('Parando…', () => stopMeeting({ data: { meetingId: m.id } }), 'Parada pedida al altavoz.')}>Parar</button>
        )}
        {m.hasAudio && !inProgress(m.state) && m.state !== 'transcribing' && (
          <button type="button" className="chip-button" disabled={busy} onClick={() => void act('Encolando…', () => retranscribeMeeting({ data: { meetingId: m.id } }), 'Transcribiendo de nuevo.')}>
            {m.state === 'no_transcript' ? 'Transcribir de nuevo' : 'Volver a transcribir'}
          </button>
        )}
        {transcript && transcript.text.trim() !== '' && (
          <button type="button" className="chip-button" disabled={busy} onClick={() => void act('Encolando…', () => resummarizeMeeting({ data: { meetingId: m.id } }), 'Resumen en camino.')}>
            {m.summary ? 'Regenerar resumen' : 'Generar resumen'}
          </button>
        )}
        {!inProgress(m.state) && !m.deletedAt && (
          <button type="button" className={`chip-button${m.keep ? ' primary' : ''}`} disabled={busy} onClick={() => void act('Guardando…', () => updateMeeting({ data: { meetingId: m.id, keep: !m.keep } }), m.keep ? 'Vuelve a entrar en la retención.' : 'Conservada: la retención no la borrará.')}>
            {m.keep ? 'Conservada ✓' : 'Conservar'}
          </button>
        )}
        {transcript && (
          <>
            <a className="chip-button" href={`/meetings/${m.id}/transcript?format=txt`}>Descargar .txt</a>
            <a className="chip-button" href={`/meetings/${m.id}/transcript?format=srt`}>Descargar .srt</a>
          </>
        )}
      </section>

      {wantsAudio && (
        <section className="player-card">
          <div className="player-visual" aria-hidden="true">
            {Array.from({ length: 48 }, (_, index) => (
              <i key={index} style={{ height: `${16 + ((index * 17) % 58)}%`, opacity: duration > 0 && (index / 48) * duration / 1000 <= t ? 1 : 0.4 }} />
            ))}
          </div>
          {audioSrc ? (
            <audio ref={audioRef} controls preload="metadata" src={audioSrc} onTimeUpdate={(e) => setT(e.currentTarget.currentTime)}>
              Tu navegador no puede reproducir este audio.
            </audio>
          ) : (
            <p className="device-meta">{audioError ?? `Cargando el audio (${formatBytes(m.audioBytes)})…`}</p>
          )}
        </section>
      )}

      {m.summary && (
        <section className="transcript-card meeting-summary">
          <p className="eyebrow">Resumen</p>
          <p>{m.summary.text}</p>
          {m.summary.agreements.length > 0 && (
            <>
              <h3>Acuerdos</h3>
              <ul>{m.summary.agreements.map((a, i) => <li key={i}>{a}</li>)}</ul>
            </>
          )}
          {m.summary.actions.length > 0 && (
            <>
              <h3>Acciones</h3>
              <ul>{m.summary.actions.map((a, i) => <li key={i}>{a}</li>)}</ul>
            </>
          )}
          <small className="device-meta">Generado automáticamente por {m.summary.model} el {formatDate(m.summary.generatedAt)}.</small>
        </section>
      )}

      {transcript ? (
        <TranscriptPanel meetingId={m.id} transcript={transcript} active={active} busy={busy} onSeek={(s) => { if (audioRef.current) { audioRef.current.currentTime = s; void audioRef.current.play() } }} onRename={(speakers) => act('Renombrando…', () => updateMeeting({ data: { meetingId: m.id, speakers } }), 'Nombres guardados.')} />
      ) : (
        <section className="transcript-card">
          <p className="eyebrow">Transcripción</p>
          <p>
            {m.state === 'transcribing' && 'Transcribiendo… la página se actualiza sola.'}
            {m.state === 'no_transcript' && `No se pudo transcribir: ${m.transcriptError ?? 'sin motivo'}. El audio sigue aquí; prueba «Transcribir de nuevo».`}
            {inProgress(m.state) && 'La transcripción llega al terminar la grabación.'}
            {m.state === 'cut' && !m.transcriptError && 'Cortada antes de transcribir; prueba «Volver a transcribir».'}
            {m.state === 'ready' && 'Sin transcripción.'}
          </p>
        </section>
      )}

      {!inProgress(m.state) && !m.deletedAt && (
        <section className="device-panel danger-zone">
          <div className="section-heading">
            <div>
              <p className="eyebrow">Borrar</p>
              <h2>Eliminar audio y transcripción</h2>
            </div>
          </div>
          <p className="device-meta">Irreversible. Queda constancia de que existió (fecha y duración), sin contenido.</p>
          <div className="device-actions">
            {!confirmDelete ? (
              <button type="button" className="chip-button danger" disabled={busy} onClick={() => setConfirmDelete(true)}>Borrar esta reunión…</button>
            ) : (
              <>
                <span className="device-meta">¿Seguro? No se puede deshacer.</span>
                <button type="button" className="chip-button danger" disabled={busy} onClick={() => void act('Borrando…', () => deleteMeeting({ data: { meetingId: m.id } }), 'Borrada.').then(() => navigate({ to: '/meetings' }))}>Sí, borrar</button>
                <button type="button" className="chip-button" onClick={() => setConfirmDelete(false)}>Cancelar</button>
              </>
            )}
          </div>
        </section>
      )}
    </main>
  )
}

function TranscriptPanel({ meetingId, transcript, active, busy, onSeek, onRename }: Readonly<{ meetingId: string; transcript: MeetingTranscript; active: number; busy: boolean; onSeek: (seconds: number) => void; onRename: (speakers: Record<string, string>) => Promise<unknown> }>) {
  const speakers = speakersOf(transcript)
  const [names, setNames] = useState<Record<string, string>>({})
  const dirty = Object.entries(names).some(([id, name]) => name !== (transcript.speakers?.[id] ?? ''))
  useEffect(() => { setNames({}) }, [meetingId, transcript.speakers])
  return (
    <section className="transcript-card meeting-transcript">
      <div className="section-heading">
        <div>
          <p className="eyebrow">Transcripción{transcript.language ? ` · ${transcript.language}` : ''}</p>
        </div>
        <span className="result-count">{transcript.segments.length} intervenciones · {transcript.model ?? ''}</span>
      </div>
      {transcript.diarized && speakers.length > 0 && (
        <form
          className="speaker-names"
          onSubmit={(e) => {
            e.preventDefault()
            void onRename(names).then(() => setNames({}))
          }}
        >
          {speakers.map((id) => (
            <label key={id} className="config-field">
              <span>{id}</span>
              <input value={names[id] ?? transcript.speakers?.[id] ?? ''} placeholder={id} onChange={(e) => setNames((prev) => ({ ...prev, [id]: e.target.value }))} />
            </label>
          ))}
          {dirty && <button type="submit" className="chip-button primary" disabled={busy}>Guardar nombres</button>}
        </form>
      )}
      {transcript.segments.length === 0 ? (
        <p>No se detectó voz en la grabación.</p>
      ) : (
        <ol className="segments">
          {transcript.segments.map((s, i) => (
            <li key={i} className={i === active ? 'active' : undefined}>
              <button type="button" className="segment-time mono" onClick={() => onSeek(s.start)}>{clock(s.start)}</button>
              {transcript.diarized && <span className="segment-speaker">{speakerName(transcript, s.speaker)}</span>}
              <span className="segment-text">{s.text}</span>
            </li>
          ))}
        </ol>
      )}
    </section>
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
