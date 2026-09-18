import type { components } from './api-schema'
import type { Tone } from './fleet'

// Functional core of the meeting pages (docs/implementation/13, design 14
// block E): labels, the live clock, the synchronized transcript and what the
// ficha may offer. No fetches, no React.

export type Meeting = components['schemas']['Meeting']
export type MeetingState = components['schemas']['MeetingState']
export type MeetingSegment = components['schemas']['MeetingSegment']
export type MeetingTranscript = components['schemas']['MeetingTranscript']
export type MeetingSummary = components['schemas']['MeetingSummary']
export type EndReason = NonNullable<Meeting['endReason']>

export const MEETING_STATE_LABEL: Record<MeetingState, string> = {
  requested: 'Pidiendo al altavoz…',
  recording: 'Grabando',
  closing: 'Cerrando…',
  transcribing: 'Transcribiendo…',
  ready: 'Lista',
  no_transcript: 'Sin transcripción',
  cut: 'Cortada',
}

export const MEETING_STATE_TONE: Record<MeetingState, Tone> = {
  requested: 'wait',
  recording: 'warn',
  closing: 'wait',
  transcribing: 'wait',
  ready: 'ok',
  no_transcript: 'warn',
  cut: 'muted',
}

export const END_REASON_LABEL: Record<EndReason, string> = {
  gesture: 'parada desde el altavoz',
  dashboard: 'parada desde el control room',
  voice: 'parada por voz',
  silence: 'parada por silencio',
  max_duration: 'parada por duración máxima',
  device_lost: 'el altavoz dejó de enviar audio',
  room_lost: 'el altavoz perdió el control room',
}

const IN_PROGRESS = new Set<MeetingState>(['requested', 'recording', 'closing'])
const BUSY = new Set<MeetingState>(['requested', 'recording', 'closing', 'transcribing'])

export function inProgress(state: MeetingState): boolean {
  return IN_PROGRESS.has(state)
}

// While something is still happening the page refreshes itself.
export function isBusy(state: MeetingState): boolean {
  return BUSY.has(state)
}

// "Lista · parada por silencio", "Sin transcripción · 413 too big" (T-E1).
export function meetingLabel(m: Pick<Meeting, 'state' | 'endReason' | 'transcriptError'>): string {
  const parts = [MEETING_STATE_LABEL[m.state]]
  if (m.state === 'no_transcript' && m.transcriptError) parts.push(m.transcriptError)
  else if (m.endReason && !inProgress(m.state)) parts.push(END_REASON_LABEL[m.endReason])
  else if (m.state === 'closing' && m.endReason) parts.push(END_REASON_LABEL[m.endReason])
  return parts.join(' · ')
}

export function liveDuration(startedAt: string, now: Date): number {
  return Math.max(0, now.getTime() - new Date(startedAt).getTime())
}

export function clock(seconds: number): string {
  const total = Math.max(0, Math.floor(seconds))
  const h = Math.floor(total / 3600)
  const m = Math.floor((total % 3600) / 60)
  const s = total % 60
  const mm = h > 0 ? String(m).padStart(2, '0') : String(m)
  return `${h > 0 ? `${h}:` : ''}${mm}:${String(s).padStart(2, '0')}`
}

// "Grabando desde 10:02 (12:34)" (RM-14, T-E3).
export function recordingSince(startedAt: string, now: Date): string {
  const since = new Intl.DateTimeFormat('es-ES', { hour: '2-digit', minute: '2-digit', timeZone: 'Europe/Madrid' }).format(new Date(startedAt))
  return `Grabando desde ${since} (${clock(liveDuration(startedAt, now) / 1000)})`
}

// The segment the player is in: the last one that started at or before t
// (a pause between two lines keeps the previous line lit). -1 before the first.
export function activeSegment(segments: readonly MeetingSegment[], t: number): number {
  let active = -1
  for (let i = 0; i < segments.length; i++) {
    if (segments[i].start <= t) active = i
    else break
  }
  return active
}

export function speakerName(transcript: Pick<MeetingTranscript, 'speakers'>, id: string): string {
  const name = transcript.speakers?.[id]
  return name && name.trim() !== '' ? name : id
}

// Speakers in order of first appearance (RM-31).
export function speakersOf(transcript: Pick<MeetingTranscript, 'segments'>): string[] {
  const seen: string[] = []
  for (const s of transcript.segments) if (s.speaker && !seen.includes(s.speaker)) seen.push(s.speaker)
  return seen
}

// RM-05/44: whether the ficha offers "Grabar" and, if not, why.
export type RecordOffer = { ok: true } | { ok: false; reason: string }

export function canRecord(unit: { state: string; reportedProfile?: string; desiredProfile?: string }): RecordOffer {
  const profile = unit.desiredProfile ?? unit.reportedProfile
  if (profile === 'micro-usb') {
    return { ok: false, reason: 'Este altavoz está en Micro USB: en ese perfil no graba reuniones. Cambia su perfil a agente para poder grabar.' }
  }
  if (unit.state !== 'adopted') {
    return { ok: false, reason: 'Solo se graba con el altavoz adoptado aquí y en contacto.' }
  }
  return { ok: true }
}

export function activeMeeting(meetings: readonly Meeting[]): Meeting | undefined {
  return meetings.find((m) => inProgress(m.state))
}
