import type { components } from './api-schema'

export type AgentProfile = components['schemas']['AgentProfile']
export type AgentProvider = components['schemas']['AgentProvider']
export type AgentProfileInput = components['schemas']['AgentProfileInput']

type ProviderInfo = { id: AgentProvider; label: string; keyEnv: string; voiceHint: string; modelHint: string }

export const PROVIDERS: readonly ProviderInfo[] = [
  { id: 'gemini', label: 'Gemini Live', keyEnv: 'GOOGLE_API_KEY', voiceHint: 'Puck, Kore, Charon…', modelHint: 'gemini-2.5-flash-native-audio-preview-12-2025' },
  { id: 'openai', label: 'OpenAI Realtime', keyEnv: 'OPENAI_API_KEY', voiceHint: 'alloy, marin, cedar…', modelHint: 'gpt-realtime-mini' },
  { id: 'phonic', label: 'Phonic', keyEnv: 'PHONIC_API_KEY', voiceHint: 'eduardo, valeria, roberto…', modelHint: 'phonic-1' },
]

export function provider(id: AgentProvider): ProviderInfo {
  return PROVIDERS.find((p) => p.id === id) ?? PROVIDERS[0]
}

export const ENVIRONMENT_PROVIDER = 'Proveedor del entorno del agente'

export function agentSummary(agent: AgentProfile): string {
  const label = agent.provider ? provider(agent.provider).label : ENVIRONMENT_PROVIDER
  return [label, agent.model, agent.voice, agent.language].filter(Boolean).join(' · ')
}

export function keyStatus(agent: AgentProfile): { text: string; tone: 'ok' | 'info' } {
  if (!agent.provider) return { text: 'Usa la clave del entorno; elige un proveedor para poder guardarle una', tone: 'info' }
  if (agent.apiKeySet) return { text: 'Clave guardada en el panel', tone: 'ok' }
  return { text: `Sin clave en el panel: usa ${provider(agent.provider).keyEnv} del agente`, tone: 'info' }
}

export function emptyInput(): AgentProfileInput {
  return { name: '', provider: 'phonic', model: '', voice: '', language: '' }
}

export function inputFrom(agent: AgentProfile): AgentProfileInput {
  return { name: agent.name, provider: agent.provider ?? 'gemini', model: agent.model ?? '', voice: agent.voice ?? '', language: agent.language ?? '' }
}

const LANGUAGE = /^[a-z]{2,3}(-[a-z0-9]{2,8})?$/

export function inputIssues(input: AgentProfileInput): string[] {
  const issues: string[] = []
  const name = input.name.trim()
  if (!name) issues.push('Ponle un nombre.')
  if (name.length > 64) issues.push('El nombre tiene más de 64 caracteres.')
  if ((input.model ?? '').trim().length > 128) issues.push('El modelo tiene más de 128 caracteres.')
  if ((input.voice ?? '').trim().length > 64) issues.push('La voz tiene más de 64 caracteres.')
  const language = (input.language ?? '').trim().toLowerCase()
  if (language && !LANGUAGE.test(language)) issues.push('El idioma va como código ISO: es, en, eu…')
  return issues
}

export function cleanInput(input: AgentProfileInput): AgentProfileInput {
  const out: AgentProfileInput = { name: input.name.trim(), provider: input.provider }
  const model = (input.model ?? '').trim()
  const voice = (input.voice ?? '').trim()
  const language = (input.language ?? '').trim().toLowerCase()
  if (model) out.model = model
  if (voice) out.voice = voice
  if (language) out.language = language
  return out
}
