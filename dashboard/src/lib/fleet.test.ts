import { describe, expect, it } from 'vitest'
import {
  canAdopt,
  canForget,
  configSync,
  desiredDocument,
  failureMessage,
  groupDevices,
  isValidIPv4,
  jobMessage,
  sectionOf,
  timeAgo,
  timeline,
  type AdoptionJob,
  type Device,
} from './fleet'

const device = (id: string, state: Device['state']): Device => ({ id, displayName: id, enabled: true, state })

describe('fleet sections', () => {
  it('groups ours, attention and others (RF-10)', () => {
    const groups = groupDevices([
      device('a', 'adopted'),
      device('b', 'absent'),
      device('c', 'orphan'),
      device('d', 'unadopted'),
      device('e', 'registered'),
      device('f', 'managed_elsewhere'),
      device('g', 'joining'),
      device('h', 'moved'),
      device('i', 'leaving'),
    ])
    expect(groups.mine.map((d) => d.id)).toEqual(['a', 'b', 'g', 'h'])
    expect(groups.attention.map((d) => d.id)).toEqual(['c', 'd', 'e', 'i'])
    expect(groups.others.map((d) => d.id)).toEqual(['f'])
  })

  it('transitions allow only what makes sense (spec §3.2)', () => {
    expect(canAdopt('joining')).toBe(false)
    expect(canForget('joining')).toBe(true)
    expect(canAdopt('moved')).toBe(true)
    expect(canForget('moved')).toBe(true)
    expect(canAdopt('leaving')).toBe(false)
    expect(canForget('leaving')).toBe(false)
  })

  it('managed elsewhere is read-only except adopt (RF-13)', () => {
    expect(sectionOf('managed_elsewhere')).toBe('others')
    expect(canAdopt('managed_elsewhere')).toBe(true)
    expect(canForget('managed_elsewhere')).toBe(false)
    expect(canAdopt('adopted')).toBe(false)
    expect(canForget('adopted')).toBe(true)
  })
})

describe('job messages (RF-35, spec §11)', () => {
  const base: AdoptionJob = { id: 'j', deviceId: 'd', kind: 'adopt', phase: 'starting', startedAt: '', updatedAt: '', ip: '10.0.0.77' }
  it('asks for the button while waiting for consent', () => {
    expect(jobMessage({ ...base, phase: 'waiting_consent' }).text).toMatch(/MUTE/)
  })
  it('explains a denied secret', () => {
    expect(jobMessage({ ...base, phase: 'failed', error: 'auth' }).text).toMatch(/secreto no coincide/)
  })
  it('explains a unit that hands no secret (old firmware)', () => {
    expect(failureMessage('no_secret', '10.0.0.77')).toMatch(/no entregó su secreto/)
  })
  it('explains an unreachable ip', () => {
    expect(failureMessage('no_reply', '10.0.0.77')).toMatch(/10\.0\.0\.77 no responde/)
  })
  it('distinguishes a forget that could not reach the device', () => {
    expect(jobMessage({ ...base, kind: 'forget', phase: 'forgotten', error: 'device_unreachable:no_address' }).tone).toBe('warn')
    expect(jobMessage({ ...base, kind: 'forget', phase: 'forgotten' }).tone).toBe('ok')
  })
})

describe('running vs desired (RF-42)', () => {
  const now = new Date('2026-09-18T12:00:00Z')
  it('is synced when versions match', () => {
    expect(configSync({ reportedConfigVersion: 'v1', desiredConfigVersion: 'v1' }, now)).toBe('synced')
  })
  it('is applying right after a change and stale after three polls', () => {
    expect(configSync({ reportedConfigVersion: 'v0', desiredConfigVersion: 'v1', profileReportedAt: '2026-09-18T11:59:40Z' }, now)).toBe('applying')
    expect(configSync({ reportedConfigVersion: 'v0', desiredConfigVersion: 'v1', profileReportedAt: '2026-09-18T11:57:00Z' }, now)).toBe('stale')
  })
  it('is none without a desired config', () => {
    expect(configSync({ reportedConfigVersion: 'v0' }, now)).toBe('none')
  })
})

describe('desired document (RF-44)', () => {
  it('drops an empty password and never carries secrets', () => {
    const doc = desiredDocument({ mode: 'half_duplex', wifi: { ssid: 'Pizarro', password: '' }, adoption: { orgSecret: 'x' }, configVersion: 'old' })
    expect(doc).toEqual({ schema: 'sebastian.config.v1', mode: 'half_duplex', wifi: { ssid: 'Pizarro' } })
  })
  it('keeps a typed password and drops wifi without ssid', () => {
    expect(desiredDocument({ wifi: { ssid: 'A', password: 'p' } }).wifi).toEqual({ ssid: 'A', password: 'p' })
    expect(desiredDocument({ wifi: { ssid: '', password: 'p' } }).wifi).toBeUndefined()
  })
})

describe('timeline', () => {
  const now = new Date('2026-09-18T12:00:00Z')
  it('reads the three clocks in the operator\'s words', () => {
    expect(timeAgo('2026-09-18T11:59:48Z', now)).toBe('hace 12 s')
    expect(timeAgo('2026-09-18T11:57:00Z', now)).toBe('hace 3 min')
    expect(
      timeline({ state: 'moved', profileReportedAt: '2026-09-18T11:59:00Z', adoptedAt: '2026-09-18T11:50:00Z', seenOnLanAt: '2026-09-18T11:59:30Z', controlRoom: 'http://10.0.0.188:8788' }, now),
    ).toEqual(['último contacto hace 1 min', 'adoptado hace 10 min', 'en la red hace 30 s (vinculado a 10.0.0.188:8788)'])
  })
  it('says when a unit never contacted and is not on the LAN', () => {
    expect(timeline({ state: 'joining', adoptedAt: '2026-09-18T11:59:40Z' }, now)).toEqual(['sin contacto todavía', 'adoptado hace 20 s', 'no se ve en la red'])
  })
})

describe('ip validation', () => {
  it('accepts dotted quads only', () => {
    expect(isValidIPv4('10.0.100.40')).toBe(true)
    expect(isValidIPv4('10.0.100')).toBe(false)
    expect(isValidIPv4('300.1.1.1')).toBe(false)
    expect(isValidIPv4('sebastian.local')).toBe(false)
  })
})
