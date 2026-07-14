import { describe, expect, it } from 'vitest'
import { formatBytes, formatDuration, kindLabel } from './format'

describe('dashboard formatting', () => {
  it('formats storage and duration compactly', () => {
    expect(formatBytes(1536)).toBe('1,5 KB')
    expect(formatDuration(125000)).toBe('2:05')
  })

  it('labels known recording kinds', () => {
    expect(kindLabel('model')).toBe('Entrada al modelo')
  })
})
