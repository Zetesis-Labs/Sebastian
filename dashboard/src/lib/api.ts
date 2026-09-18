import { createServerFn } from '@tanstack/react-start'
import type { components } from './api-schema'

export type Recording = components['schemas']['Recording']
export type RecordingSummary = components['schemas']['RecordingSummary']
export type Device = components['schemas']['Device']
export type DeviceDetail = components['schemas']['DeviceDetail']
export type AdoptionJob = components['schemas']['AdoptionJob']
export type DeviceConfig = components['schemas']['DeviceConfig']
export type ControlRoomInfo = components['schemas']['ControlRoom']

type RecordingList = components['schemas']['RecordingList']
type DeviceList = components['schemas']['DeviceList']

// Server functions must return serializable values; the free-form config
// document is typed as JSON on the wire.
export type Json = string | number | boolean | null | Json[] | { [key: string]: Json }
export type DeviceDetailWire = Omit<DeviceDetail, 'desiredConfig' | 'runningConfig'> & {
  desiredConfig?: { [key: string]: Json }
  runningConfig?: { [key: string]: Json }
}

function apiConfiguration() {
  const baseURL = process.env.SEBASTIAN_API_URL
  const secret = process.env.SEBASTIAN_ADMIN_SECRET
  if (!baseURL || !secret) {
    throw new Error('SEBASTIAN_API_URL and SEBASTIAN_ADMIN_SECRET must be configured')
  }
  return { baseURL: baseURL.replace(/\/$/, ''), secret }
}

class ApiError extends Error {
  constructor(
    readonly status: number,
    readonly detail: string,
  ) {
    super(detail)
  }
}

async function problemDetail(response: Response): Promise<string> {
  try {
    const body = (await response.json()) as { detail?: string; title?: string }
    return body.detail ?? body.title ?? `Sebastian API returned ${response.status}`
  } catch {
    return `Sebastian API returned ${response.status}`
  }
}

async function apiGet<T>(path: string): Promise<T> {
  const { baseURL, secret } = apiConfiguration()
  const response = await fetch(`${baseURL}${path}`, {
    headers: { 'X-Admin-Secret': secret },
  })
  if (!response.ok) {
    throw new ApiError(response.status, await problemDetail(response))
  }
  return response.json() as Promise<T>
}

async function apiSend<T = void>(path: string, method: string, body?: unknown): Promise<T> {
  const { baseURL, secret } = apiConfiguration()
  const response = await fetch(`${baseURL}${path}`, {
    method,
    headers: { 'X-Admin-Secret': secret, ...(body === undefined ? {} : { 'Content-Type': 'application/json' }) },
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  if (!response.ok) {
    throw new ApiError(response.status, await problemDetail(response))
  }
  if (response.status === 204) return undefined as T
  return response.json() as Promise<T>
}

const id = (value: string) => encodeURIComponent(value)

export const getDashboard = createServerFn({ method: 'GET' }).handler(async () => {
  const [recordings, summary] = await Promise.all([
    apiGet<RecordingList>('/v1/admin/recordings?limit=50'),
    apiGet<RecordingSummary>('/v1/admin/recordings/summary'),
  ])
  return { recordings: recordings.items, summary }
})

export const getRecording = createServerFn({ method: 'GET' })
  .validator((recordingId: string) => recordingId)
  .handler(async ({ data }) => apiGet<Recording>(`/v1/admin/recordings/${id(data)}`))

// ── fleet ───────────────────────────────────────────────────────────────────

export const getDevices = createServerFn({ method: 'GET' }).handler(async () => {
  const [devices, room] = await Promise.all([
    apiGet<DeviceList>('/v1/admin/devices'),
    apiGet<ControlRoomInfo>('/v1/admin/control-room'),
  ])
  // The organization secret never reaches the browser (RF-54).
  const { orgSecret: _omit, ...publicRoom } = room
  return { devices: devices.items, room: publicRoom }
})

export type ControlRoomPublic = Omit<ControlRoomInfo, 'orgSecret'>

// The ficha needs the control room's own defaults (token server, syslog) to
// show what an adoption wrote into the unit.
export const getDevice = createServerFn({ method: 'GET' })
  .validator((deviceId: string) => deviceId)
  .handler(async ({ data }) => {
    const [detail, room] = await Promise.all([
      apiGet<DeviceDetailWire>(`/v1/admin/devices/${id(data)}`),
      apiGet<ControlRoomInfo>('/v1/admin/control-room'),
    ])
    const { orgSecret: _omit, ...publicRoom } = room
    return { detail, room: publicRoom as ControlRoomPublic }
  })

// Desired-state: the device reconciles on its next poll (~30 s) and reboots
// into the chosen profile. An empty name clears the desired state.
export const setDeviceProfile = createServerFn({ method: 'POST' })
  .validator((input: { deviceId: string; name: string }) => input)
  .handler(async ({ data }) =>
    apiSend(`/v1/admin/devices/${id(data.deviceId)}/desired-profile`, 'PUT', { name: data.name }),
  )

export const renameDevice = createServerFn({ method: 'POST' })
  .validator((input: { deviceId: string; displayName: string }) => input)
  .handler(async ({ data }) =>
    apiSend(`/v1/admin/devices/${id(data.deviceId)}`, 'PATCH', { displayName: data.displayName }),
  )

export const adoptDevice = createServerFn({ method: 'POST' })
  .validator((input: { deviceId: string; ip?: string; deviceSecret?: string }) => input)
  .handler(async ({ data }) =>
    apiSend<AdoptionJob>(`/v1/admin/devices/${id(data.deviceId)}/adopt`, 'POST', {
      ...(data.ip ? { ip: data.ip } : {}),
      ...(data.deviceSecret ? { deviceSecret: data.deviceSecret } : {}),
    }),
  )

export const forgetDevice = createServerFn({ method: 'POST' })
  .validator((input: { deviceId: string }) => input)
  .handler(async ({ data }) => apiSend<AdoptionJob>(`/v1/admin/devices/${id(data.deviceId)}/forget`, 'POST', {}))

export const getAdoptionJob = createServerFn({ method: 'GET' })
  .validator((jobId: string) => jobId)
  .handler(async ({ data }) => apiGet<AdoptionJob>(`/v1/admin/adoptions/${id(data)}`))

export const setDesiredConfig = createServerFn({ method: 'POST' })
  .validator((input: { deviceId: string; config: { [key: string]: Json } }) => input)
  .handler(async ({ data }) =>
    apiSend<{ version: string }>(`/v1/admin/devices/${id(data.deviceId)}/desired-config`, 'PUT', data.config),
  )

export const clearDesiredConfig = createServerFn({ method: 'POST' })
  .validator((input: { deviceId: string }) => input)
  .handler(async ({ data }) => apiSend(`/v1/admin/devices/${id(data.deviceId)}/desired-config`, 'DELETE'))

export const regenerateSecret = createServerFn({ method: 'POST' })
  .validator((input: { deviceId: string }) => input)
  .handler(async ({ data }) =>
    apiSend<{ deviceSecret: string }>(`/v1/admin/devices/${id(data.deviceId)}/secret`, 'POST'),
  )

// Server-only: the embedded installer's pre-fill (includes the organization
// secret; served by the /installer/control-room.json route, never bundled).
export async function controlRoomForInstaller(): Promise<ControlRoomInfo> {
  return apiGet<ControlRoomInfo>('/v1/admin/control-room')
}
