import type { components } from './api-schema'
import { timeAgo } from './fleet'

export type Playback = components['schemas']['Playback']

export function playbackLabel(playback: Playback, now: Date | null): string {
  return now ? `Sonando desde ${timeAgo(playback.startedAt, now)}` : 'Sonando'
}
