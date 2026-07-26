import { createServerFn } from '@tanstack/react-start'
import type { components } from './api-schema'

export type Recording = components['schemas']['Recording']
export type RecordingSummary = components['schemas']['RecordingSummary']
export type Device = components['schemas']['Device']

type RecordingList = components['schemas']['RecordingList']
type DeviceList = components['schemas']['DeviceList']

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

async function apiSend(path: string, method: string, body: unknown): Promise<void> {
  const { baseURL, secret } = apiConfiguration()
  const response = await fetch(`${baseURL}${path}`, {
    method,
    headers: { 'X-Admin-Secret': secret, 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
  if (!response.ok) {
    throw new Error(`Sebastian API returned ${response.status}`)
  }
}

export const getDevices = createServerFn({ method: 'GET' }).handler(async () => {
  const devices = await apiGet<DeviceList>('/v1/admin/devices')
  return devices.items
})

// Desired-state: the device reconciles on its next poll (~30 s) and reboots
// into the chosen profile. An empty name clears the desired state.
export const setDeviceProfile = createServerFn({ method: 'POST' })
  .validator((input: { deviceId: string; name: string }) => input)
  .handler(async ({ data }) =>
    apiSend(`/v1/admin/devices/${encodeURIComponent(data.deviceId)}/desired-profile`, 'PUT', {
      name: data.name,
    }),
  )
