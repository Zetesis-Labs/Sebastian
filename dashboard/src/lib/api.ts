import { createServerFn } from '@tanstack/react-start'
import type { components } from './api-schema'

export type Recording = components['schemas']['Recording']
export type RecordingSummary = components['schemas']['RecordingSummary']

type RecordingList = components['schemas']['RecordingList']

function apiConfiguration() {
  const baseURL = process.env.SEBASTIAN_API_URL
  const secret = process.env.SEBASTIAN_ADMIN_SECRET
  if (!baseURL || !secret) {
    throw new Error('SEBASTIAN_API_URL and SEBASTIAN_ADMIN_SECRET must be configured')
  }
  return { baseURL: baseURL.replace(/\/$/, ''), secret }
}

async function apiGet<T>(path: string): Promise<T> {
  const { baseURL, secret } = apiConfiguration()
  const response = await fetch(`${baseURL}${path}`, {
    headers: { 'X-Admin-Secret': secret },
  })
  if (!response.ok) {
    throw new Error(`Sebastian API returned ${response.status}`)
  }
  return response.json() as Promise<T>
}

export const getDashboard = createServerFn({ method: 'GET' }).handler(async () => {
  const [recordings, summary] = await Promise.all([
    apiGet<RecordingList>('/v1/admin/recordings?limit=50'),
    apiGet<RecordingSummary>('/v1/admin/recordings/summary'),
  ])
  return { recordings: recordings.items, summary }
})

export const getRecording = createServerFn({ method: 'GET' })
  .validator((recordingId: string) => recordingId)
  .handler(async ({ data }) => apiGet<Recording>(`/v1/admin/recordings/${encodeURIComponent(data)}`))
