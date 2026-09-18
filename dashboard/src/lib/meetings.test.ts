import { describe, expect, it } from 'vitest'
import { activeMeeting, activeSegment, canRecord, clock, liveDuration, meetingLabel, recordingSince, speakerName, speakersOf, type Meeting } from './meetings'

const base: Meeting = { id: 'm1', deviceId: '68ee', state: 'ready', requestedBy: 'dashboard', requestedAt: '2026-09-19T10:00:00Z', durationMs: 0, audioBytes: 0, hasAudio: true, keep: false }

describe('meeting labels (T-E1)', () => {
  it('names every state and the reason it ended', () => {
    expect(meetingLabel({ state: 'requested' })).toBe('Pidiendo al altavoz…')
    expect(meetingLabel({ state: 'recording' })).toBe('Grabando')
    expect(meetingLabel({ state: 'closing', endReason: 'dashboard' })).toBe('Cerrando… · parada desde el control room')
    expect(meetingLabel({ state: 'transcribing', endReason: 'gesture' })).toBe('Transcribiendo… · parada desde el altavoz')
    expect(meetingLabel({ state: 'ready', endReason: 'silence' })).toBe('Lista · parada por silencio')
    expect(meetingLabel({ state: 'ready', endReason: 'max_duration' })).toBe('Lista · parada por duración máxima')
    expect(meetingLabel({ state: 'cut', endReason: 'device_lost' })).toBe('Cortada · el altavoz dejó de enviar audio')
    expect(meetingLabel({ state: 'no_transcript', endReason: 'voice', transcriptError: '413 too big' })).toBe('Sin transcripción · 413 too big')
  })
})

describe('synchronized transcript (T-E2)', () => {
  const segments = [
    { start: 0, end: 2, speaker: 'Hablante 1', text: 'a' },
    { start: 2.5, end: 4, speaker: 'Hablante 2', text: 'b' },
    { start: 9, end: 12, speaker: 'Hablante 1', text: 'c' },
  ]
  it('lights the segment the player is in and keeps it lit through a pause', () => {
    expect(activeSegment(segments, 0)).toBe(0)
    expect(activeSegment(segments, 2.2)).toBe(0)
    expect(activeSegment(segments, 3)).toBe(1)
    expect(activeSegment(segments, 7)).toBe(1)
    expect(activeSegment(segments, 20)).toBe(2)
    expect(activeSegment([], 5)).toBe(-1)
  })
  it('lists speakers in order and applies the operator names (T-E5)', () => {
    expect(speakersOf({ segments })).toEqual(['Hablante 1', 'Hablante 2'])
    expect(speakerName({ speakers: { 'Hablante 1': 'Ana' } }, 'Hablante 1')).toBe('Ana')
    expect(speakerName({ speakers: { 'Hablante 1': ' ' } }, 'Hablante 1')).toBe('Hablante 1')
    expect(speakerName({}, 'Hablante 2')).toBe('Hablante 2')
  })
})

describe('live clock (T-E3)', () => {
  it('counts from the confirmation and prints the local start', () => {
    const now = new Date('2026-09-19T10:12:34Z')
    expect(liveDuration('2026-09-19T10:00:00Z', now)).toBe(754_000)
    expect(recordingSince('2026-09-19T10:00:00Z', now)).toBe('Grabando desde 12:00 (12:34)')
    expect(clock(3725)).toBe('1:02:05')
    expect(clock(59)).toBe('0:59')
  })
})

describe('the ficha (T-E4)', () => {
  it('hides Grabar in micro-usb and explains, refuses when not adopted here', () => {
    expect(canRecord({ state: 'adopted', reportedProfile: 'micro-usb' })).toMatchObject({ ok: false })
    expect((canRecord({ state: 'adopted', reportedProfile: 'agente', desiredProfile: 'micro-usb' }) as { reason: string }).reason).toContain('Micro USB')
    expect(canRecord({ state: 'absent', reportedProfile: 'agente' })).toMatchObject({ ok: false })
    expect(canRecord({ state: 'adopted', reportedProfile: 'agente' })).toEqual({ ok: true })
  })
  it('finds the meeting in progress to offer Parar (RM-05)', () => {
    expect(activeMeeting([base, { ...base, id: 'm2', state: 'recording' }])?.id).toBe('m2')
    expect(activeMeeting([base, { ...base, id: 'm3', state: 'transcribing' }])).toBeUndefined()
  })
})
