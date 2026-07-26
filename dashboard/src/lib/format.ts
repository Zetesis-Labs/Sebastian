export function formatBytes(bytes: number): string {
  if (bytes === 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  const index = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1)
  return `${new Intl.NumberFormat('es-ES', { maximumFractionDigits: 1 }).format(bytes / 1024 ** index)} ${units[index]}`
}

export function formatDuration(milliseconds: number): string {
  const totalSeconds = Math.round(milliseconds / 1000)
  const hours = Math.floor(totalSeconds / 3600)
  const minutes = Math.floor((totalSeconds % 3600) / 60)
  const seconds = totalSeconds % 60
  return hours > 0
    ? `${hours} h ${minutes.toString().padStart(2, '0')} min`
    : `${minutes}:${seconds.toString().padStart(2, '0')}`
}

export function formatDate(value: string): string {
  return new Intl.DateTimeFormat('es-ES', {
    dateStyle: 'medium', timeStyle: 'short', timeZone: 'Europe/Madrid',
  }).format(new Date(value))
}

export function kindLabel(kind: string): string {
  return ({ model: 'Entrada al modelo', microphone: 'Micrófono', agent: 'Voz de Sebastian', composite: 'Conversación' } as Record<string, string>)[kind] ?? kind
}
