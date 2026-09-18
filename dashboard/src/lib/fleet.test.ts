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
    ])
    expect(groups.mine.map((d) => d.id)).toEqual(['a', 'b'])
    expect(groups.attention.map((d) => d.id)).toEqual(['c', 'd', 'e'])
    expect(groups.others.map((d) => d.id)).toEqual(['f'])
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

describe('ip validation', () => {
  it('accepts dotted quads only', () => {
    expect(isValidIPv4('10.0.100.40')).toBe(true)
    expect(isValidIPv4('10.0.100')).toBe(false)
    expect(isValidIPv4('300.1.1.1')).toBe(false)
    expect(isValidIPv4('sebastian.local')).toBe(false)
  })
})
