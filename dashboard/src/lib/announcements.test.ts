import { describe, expect, it } from 'vitest'
import { ANNOUNCEMENT_MAX, announcementIssue, canAnnounce, deliveryMessage } from './announcements'

describe('announcements', () => {
  it('only an adopted, contacting unit in the agente profile can speak', () => {
    expect(canAnnounce({ state: 'adopted', reportedProfile: 'agente' })).toEqual({ ok: true })
    expect(canAnnounce({ state: 'adopted', reportedProfile: 'micro-usb' }).ok).toBe(false)
    expect(canAnnounce({ state: 'adopted', reportedProfile: 'agente', desiredProfile: 'micro-usb' }).ok).toBe(false)
    expect(canAnnounce({ state: 'absent', reportedProfile: 'agente' }).ok).toBe(false)
  })

  it('the text must be sayable', () => {
    expect(announcementIssue('La lavadora ha terminado')).toBe('')
    expect(announcementIssue('   ')).not.toBe('')
    expect(announcementIssue('a'.repeat(ANNOUNCEMENT_MAX + 1))).not.toBe('')
    expect(announcementIssue('ñ'.repeat(ANNOUNCEMENT_MAX))).toBe('')
  })

  it('tells the operator how the announcement will reach the unit', () => {
    expect(deliveryMessage('conversation')).toMatch(/conversación/)
    expect(deliveryMessage('lan')).toMatch(/segundos/)
    expect(deliveryMessage('poll')).toMatch(/30 s/)
  })
})
