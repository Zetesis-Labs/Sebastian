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
  joining: 'Adoptado aquí · esperando arranque',
  absent: 'Adoptado aquí · ausente',
  moved: 'Se lo llevó otro control room',
  leaving: 'Olvidado · reiniciando',
  managed_elsewhere: 'Gestionado por otro control room',
  unadopted: 'Sin adoptar',
  orphan: 'Huérfano',
  registered: 'Registrado · sin adoptar',
}

export const STATE_HINT: Record<DeviceState, string> = {
  adopted: 'Vinculado a este control room y contactando.',
  joining: 'Adoptado desde aquí; la placa se reinicia y contactará en menos de un minuto.',
  absent: 'Vinculado aquí, pero lleva más de 90 s sin contactar y no se ve en la red.',
  moved: 'Sigue en el inventario, pero la red lo anuncia vinculado a otro control room desde después de su último contacto. Olvídalo para limpiar el inventario, o vuelve a adoptarlo.',
  leaving: 'Olvidado desde aquí; la red aún lleva su anuncio antiguo. Desaparece solo cuando la placa arranca sin control room.',
  managed_elsewhere: 'Se ve en la red, vinculado a otro control room. Solo lectura.',
  unadopted: 'Se ve en la red y no tiene control room.',
  orphan: 'Se ve en la red; su control room le está fallando.',
  registered: 'Ha contactado con este control room sin secreto de altavoz: firmware antiguo, o aún no ha podido darse de alta (¿falta el secreto de organización aquí?).',
}

// Colour of the state badge and the row border.
export type Tone = 'ok' | 'wait' | 'warn' | 'muted' | 'other' | 'info'
export const STATE_TONE: Record<DeviceState, Tone> = {
  adopted: 'ok',
  joining: 'wait',
  absent: 'muted',
  moved: 'warn',
  leaving: 'muted',
  managed_elsewhere: 'other',
  unadopted: 'warn',
  orphan: 'warn',
  registered: 'info',
}

// States in transition: the row says what is happening and what comes next.
export const TRANSITIONAL = new Set<DeviceState>(['joining', 'moved', 'leaving'])

export type Section = 'mine' | 'attention' | 'others'

export const SECTION_TITLE: Record<Section, string> = {
  mine: 'Adoptados aquí',
  attention: 'En la red · sin adoptar o huérfanos',
  others: 'Gestionados por otros control rooms',
}

export function sectionOf(state: DeviceState): Section {
  switch (state) {
    case 'adopted':
    case 'joining':
    case 'absent':
    case 'moved':
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
  return state !== 'adopted' && state !== 'absent' && state !== 'joining' && state !== 'leaving'
}

export function canForget(state: DeviceState): boolean {
  return state === 'adopted' || state === 'absent' || state === 'registered' || state === 'moved' || state === 'joining'
}

// "hace 12 s" / "hace 3 min" / "hace 2 h", or the day when older.
export function timeAgo(iso: string, now: Date): string {
  const seconds = Math.max(0, Math.round((now.getTime() - new Date(iso).getTime()) / 1000))
  if (seconds < 60) return `hace ${seconds} s`
  if (seconds < 3600) return `hace ${Math.round(seconds / 60)} min`
  if (seconds < 86_400) return `hace ${Math.round(seconds / 3600)} h`
  return new Intl.DateTimeFormat('es-ES', { dateStyle: 'medium', timeStyle: 'short', timeZone: 'Europe/Madrid' }).format(new Date(iso))
}

// A control room origin as the operator reads it: host:port.
export function shortRoom(origin: string): string {
  return origin.replace(/^[a-z]+:\/\//i, '').replace(/\/$/, '')
}

// The row's chronology (spec §3.2 transitions): what the unit told us, when we
// adopted it and what the LAN says — the three clocks a transition depends on.
export function timeline(
  device: Pick<Device, 'profileReportedAt' | 'adoptedAt' | 'seenOnLanAt' | 'controlRoom' | 'state'>,
  now: Date,
): string[] {
  const items: string[] = []
  items.push(device.profileReportedAt ? `último contacto ${timeAgo(device.profileReportedAt, now)}` : 'sin contacto todavía')
  if (device.adoptedAt) items.push(`adoptado ${timeAgo(device.adoptedAt, now)}`)
  if (device.seenOnLanAt) {
    const bound = device.controlRoom ? `vinculado a ${shortRoom(device.controlRoom)}` : 'sin control room'
    items.push(`en la red ${timeAgo(device.seenOnLanAt, now)} (${bound})`)
  } else if (device.state !== 'registered') {
    items.push('no se ve en la red')
  }
  return items
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
  if (error === 'no_secret') return 'El altavoz aceptó la adopción pero no entregó su secreto: firmware anterior al contrato actual. Reflashéalo desde el instalador.'
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

// The ficha's form: only what the control room governs (spec §3.4), grouped
// the way the operator thinks. `mode` renders the mode selector first.
export const FORM_GROUPS: { title: string; hint: string; mode?: boolean; paths: string[] }[] = [
  { title: 'Red WiFi', hint: 'Déjalo vacío para no tocar la red del altavoz.', paths: ['wifi.ssid', 'wifi.password', 'wifi.hidden'] },
  { title: 'Control room', hint: 'Lo que la adopción escribió en el altavoz.', paths: ['livekit.tokenServerUrl', 'telemetry.syslogIp', 'telemetry.syslogPort'] },
  { title: 'Audio', hint: 'Modo de conversación y haz del micro.', mode: true, paths: ['audio.fixedBeam', 'audio.fixedBeamAzimuthDeg'] },
  { title: 'Conversación', hint: 'Cuándo se cierra una sesión y cuánta voz hace falta para abrirla.', paths: ['session.silenceTimeoutMs', 'session.voiceLevel'] },
]

// An empty SSID in the ficha means "keep the stored network", not an error.
export function fichaIssues<T extends { path: string }>(issues: T[], form: { wifi: { ssid: string } }): T[] {
  return issues.filter((i) => !(i.path === 'wifi.ssid' && !form.wifi.ssid))
}

// What an adoption from this control room wrote into the unit: shown when the
// desired document does not say otherwise.
export function seedFromRoom<T extends { livekit: { tokenServerUrl: string }; telemetry: { syslogIp: string; syslogPort: number } }>(
  form: T,
  room: { apiUrl: string; syslogIp?: string; syslogPort?: number },
): T {
  const origin = room.apiUrl.replace(/\/$/, '')
  return {
    ...form,
    livekit: { ...form.livekit, tokenServerUrl: form.livekit.tokenServerUrl || (origin ? `${origin}/token` : '') },
    telemetry: {
      ...form.telemetry,
      syslogIp: form.telemetry.syslogIp || room.syslogIp || '',
      syslogPort: form.telemetry.syslogPort || room.syslogPort || 514,
    },
  }
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
