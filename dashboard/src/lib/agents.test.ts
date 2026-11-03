import { describe, expect, it } from 'vitest'
import { agentSummary, cleanInput, inputFrom, inputIssues, keyStatus, type AgentProfile } from './agents'

const phonic: AgentProfile = {
  id: '018f08d8-3f5d-7d5d-bd61-9b2ba12b58b8',
  name: 'phonic',
  source: 'kubernetes',
  provider: 'phonic',
  voice: 'eduardo',
  language: 'es',
  apiKeySet: false,
  deviceCount: 1,
  createdAt: '2026-11-03T19:00:00Z',
  updatedAt: '2026-11-03T19:00:00Z',
}

describe('agents', () => {
  it('summarises provider, model, voice and language, skipping what is unset', () => {
    expect(agentSummary(phonic)).toBe('Phonic · eduardo · es')
    expect(agentSummary({ ...phonic, provider: 'openai', model: 'gpt-realtime', voice: undefined, language: undefined })).toBe('OpenAI Realtime · gpt-realtime')
  })

  it('names a legacy agent without provider after the environment and refuses it a key', () => {
    const legacy = { ...phonic, provider: undefined, voice: undefined, language: undefined }
    expect(agentSummary(legacy)).toBe('Proveedor del entorno del agente')
    expect(keyStatus(legacy).text).toContain('elige un proveedor')
    expect(inputFrom(legacy).provider).toBe('gemini')
  })

  it('says where the key comes from', () => {
    expect(keyStatus(phonic)).toEqual({ text: 'Sin clave en el panel: usa PHONIC_API_KEY del agente', tone: 'info' })
    expect(keyStatus({ ...phonic, apiKeySet: true }).tone).toBe('ok')
  })

  it('flags a missing name and a language that is not an ISO code', () => {
    expect(inputIssues({ name: ' ', provider: 'gemini' })).toEqual(['Ponle un nombre.'])
    expect(inputIssues({ name: 'casa', provider: 'phonic', language: 'es_ES' })).toEqual(['El idioma va como código ISO: es, en, eu…'])
    expect(inputIssues({ name: 'casa', provider: 'phonic', language: 'ES' })).toEqual([])
  })

  it('sends only what was filled in, trimmed', () => {
    expect(cleanInput({ name: ' casa ', provider: 'phonic', model: ' ', voice: ' eduardo ', language: 'ES' })).toEqual({
      name: 'casa', provider: 'phonic', voice: 'eduardo', language: 'es',
    })
    expect(cleanInput(inputFrom(phonic))).toEqual({ name: 'phonic', provider: 'phonic', voice: 'eduardo', language: 'es' })
  })
})
