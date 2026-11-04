import type { components } from './api-schema'

export type Announcement = components['schemas']['Announcement']
export type AnnouncementDelivery = Announcement['delivery']

export const ANNOUNCEMENT_MAX = 500

export type AnnounceOffer = { ok: true } | { ok: false; reason: string }

export function canAnnounce(unit: { state: string; reportedProfile?: string; desiredProfile?: string }): AnnounceOffer {
  const profile = unit.desiredProfile ?? unit.reportedProfile
  if (profile === 'micro-usb') {
    return { ok: false, reason: 'En Micro USB el altavoz no tiene agente que hable. Cambia su perfil a agente para avisar por él.' }
  }
  if (unit.state !== 'adopted') {
    return { ok: false, reason: 'Solo se avisa por un altavoz adoptado aquí y en contacto.' }
  }
  return { ok: true }
}

export function announcementIssue(text: string): string {
  const trimmed = text.trim()
  if (!trimmed) return 'Escribe lo que tiene que decir.'
  if ([...trimmed].length > ANNOUNCEMENT_MAX) return `Como mucho ${ANNOUNCEMENT_MAX} caracteres.`
  return ''
}

const DELIVERY_MESSAGE: Record<AnnouncementDelivery, string> = {
  conversation: 'Está en una conversación: lo dirá en la próxima pausa.',
  lan: 'Orden enviada: debería sonar en unos segundos; si no, en la próxima consulta (hasta 30 s).',
  poll: 'En cola: lo dirá cuando consulte la sala de control (hasta 30 s).',
}

export function deliveryMessage(delivery: AnnouncementDelivery): string {
  return DELIVERY_MESSAGE[delivery]
}
