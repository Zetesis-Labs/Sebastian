import { describe, expect, it } from 'vitest'
import { playbackLabel } from './playback'

describe('playback', () => {
  it('says since when the unit is playing', () => {
    const now = new Date('2026-11-05T12:10:00Z')
    expect(playbackLabel({ id: 'p', startedAt: '2026-11-05T12:07:00Z' }, now)).toBe('Sonando desde hace 3 min')
    expect(playbackLabel({ id: 'p', startedAt: '2026-11-05T12:07:00Z' }, null)).toBe('Sonando')
  })
})
