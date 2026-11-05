import { describe, expect, it } from 'vitest'
import { errorMessage, formatBytes, formatDuration } from './format'

describe('dashboard formatting', () => {
  it('formats storage and duration compactly', () => {
    expect(formatBytes(1536)).toBe('1,5 KB')
    expect(formatDuration(125000)).toBe('2:05')
  })

  it('reads a route error whatever was thrown', () => {
    expect(errorMessage(new Error('El inventario no responde'))).toBe('El inventario no responde')
    expect(errorMessage('timeout')).toBe('timeout')
  })
})
