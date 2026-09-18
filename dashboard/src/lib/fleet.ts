import type { components } from './api-schema'

// Functional core of the fleet pages: labels, grouping and the operator
// messages of the functional spec (docs/implementation/12-fleet-adoption-functional-spec.md §3.2, §11).
// No fetches, no React.

export type Device = components['schemas']['Device']
export type DeviceDetail = components['schemas']['DeviceDetail']
export type DeviceState = components['schemas']['DeviceState']
export type AdoptionJob = components['schemas']['AdoptionJob']
export type DeviceConfig = components['schemas']['DeviceConfig']
export type ControlRoomInfo = components['schemas']['ControlRoom']

export const STATE_LABEL: Record<DeviceState, string> = {
  adopted: 'Adoptado aquí',
  absent: 'Adoptado aquí · ausente',
  managed_elsewhere: 'Gestionado por otro control room',
  unadopted: 'Sin adoptar',
  orphan: 'Huérfano',
  registered: 'Registrado · sin adoptar',
}

export const STATE_HINT: Record<DeviceState, string> = {
  adopted: 'Vinculado a este control room y contactando.',
  absent: 'Vinculado aquí, pero lleva más de 90 s sin contactar y no se ve en la red.',
  managed_elsewhere: 'Se ve en la red, vinculado a otro control room. Solo lectura.',
  unadopted: 'Se ve en la red y no tiene control room.',
  orphan: 'Se ve en la red; su control room le está fallando.',
  registered: 'Ha contactado con este control room sin secreto de altavoz: firmware antiguo, o aún no ha podido darse de alta (¿falta el secreto de organización aquí?).',
}

export type Section = 'mine' | 'attention' | 'others'

export const SECTION_TITLE: Record<Section, string> = {
  mine: 'Adoptados aquí',
  attention: 'En la red · sin adoptar o huérfanos',
  others: 'Gestionados por otros control rooms',
}

export function sectionOf(state: DeviceState): Section {
  switch (state) {
    case 'adopted':
    case 'absent':
      return 'mine'
    case 'managed_elsewhere':
      return 'others'
    default:
      return 'attention'
  }
}

export function groupDevices(devices: Device[]): Record<Section, Device[]> {
  const groups: Record<Section, Device[]> = { mine: [], attention: [], others: [] }
  for (const device of devices) groups[sectionOf(device.state)].push(device)
  return groups
}

// What the operator may do with a unit in each state (RF-13, RF-30, RF-37).
export function canAdopt(state: DeviceState): boolean {
  return state !== 'adopted' && state !== 'absent'
}

export function canForget(state: DeviceState): boolean {
  return state === 'adopted' || state === 'absent' || state === 'registered'
}

// The one-line message for an adoption job (RF-35, spec §11).
export function jobMessage(job: AdoptionJob): { text: string; tone: 'ok' | 'warn' | 'info' } {
  const ip = job.ip ?? 'la placa'
  switch (job.phase) {
    case 'starting':
      return { text: `Contactando con ${ip}…`, tone: 'info' }
    case 'waiting_consent':
      return { text: 'Pulsa MUTE en el altavoz antes de 30 s. El anillo parpadea en ámbar.', tone: 'info' }
    case 'queued':
      return { text: 'En cola: el altavoz está en una conversación. Se aplicará al terminar (máximo 2 min).', tone: 'info' }
    case 'adopted':
      return { text: 'Adoptado. El altavoz se reinicia con este control room.', tone: 'ok' }
    case 'forgotten':
      return job.error
        ? { text: 'Retirado del inventario. No se pudo avisar al altavoz: hazlo por USB cuando lo tengas a mano.', tone: 'warn' }
        : { text: 'Olvidado: el altavoz vuelve a fábrica y desaparece del inventario.', tone: 'ok' }
    case 'failed':
      return { text: failureMessage(job.error ?? '', ip), tone: 'warn' }
  }
}

export function failureMessage(error: string, ip: string): string {
  if (error === 'auth') {
    return 'El altavoz ha rechazado la adopción: el secreto no coincide. Necesitas el secreto de organización de su dueño o el secreto de ese altavoz.'
  }
  if (error === 'consent_timeout') return 'No se pulsó MUTE a tiempo. Vuelve a adoptar cuando tengas el altavoz a mano.'
  if (error === 'no_reply' || error === 'transport') {
    return `${ip} no responde. ¿Está encendido y en una red alcanzable desde este control room?`
  }
  if (error === 'no_address') return 'No se sabe dónde está el altavoz: no se ve en la red. Usa "Adoptar por IP".'
  if (error === 'nonce') return 'La adopción ha caducado antes de completarse. Inténtalo de nuevo.'
  if (error.startsWith('rejected:')) return `El altavoz ha rechazado la configuración (${error.slice('rejected:'.length)}).`
  return `La adopción ha fallado (${error || 'error desconocido'}).`
}

export const JOB_DONE = new Set(['adopted', 'forgotten', 'failed'])

// Running vs desired (RF-42): "sincronizado" when the device runs the version
// we want, "aplicando" while it differs, "no aplicada" after ~3 polls.
export function configSync(device: Pick<Device, 'reportedConfigVersion' | 'desiredConfigVersion' | 'profileReportedAt'>, now: Date): 'none' | 'synced' | 'applying' | 'stale' {
  if (!device.desiredConfigVersion) return 'none'
  if (device.reportedConfigVersion === device.desiredConfigVersion) return 'synced'
  const lastPoll = device.profileReportedAt ? new Date(device.profileReportedAt).getTime() : 0
  return now.getTime() - lastPoll > 100_000 ? 'stale' : 'applying'
}

// Fields the control room governs (spec §3.4); the rest is read-only in the form.
export const GOVERNABLE_PATHS = new Set([
  'mode',
  'wifi.ssid',
  'wifi.password',
  'wifi.hidden',
  'livekit.tokenServerUrl',
  'telemetry.syslogIp',
  'telemetry.syslogPort',
  'audio.fixedBeam',
  'audio.fixedBeamAzimuthDeg',
  'audio.fullDuplex',
  'session.silenceTimeoutMs',
  'session.voiceLevel',
])

export const REFLASH_ONLY: Record<string, string> = {
  'audio.micChannel': 'Canal del micro: requiere reflashear (se fija al compilar el firmware).',
}

// A pushed config never carries the WiFi password unless the operator typed one
// (RF-44); the desired document is the form minus empty secrets.
export function desiredDocument(form: Record<string, unknown>): Record<string, unknown> {
  const doc: Record<string, unknown> = { ...form, schema: 'sebastian.config.v1' }
  const wifi = { ...((form.wifi as Record<string, unknown>) ?? {}) }
  if (!wifi.password) delete wifi.password
  if (!wifi.ssid) delete doc.wifi
  else doc.wifi = wifi
  delete doc.adoption
  delete doc.configVersion
  return doc
}

export function isValidIPv4(value: string): boolean {
  const parts = value.trim().split('.')
  return parts.length === 4 && parts.every((p) => /^\d{1,3}$/.test(p) && Number(p) <= 255)
}
