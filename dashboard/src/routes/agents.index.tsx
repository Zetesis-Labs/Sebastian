import { useState } from 'react'
import { createFileRoute, useRouter } from '@tanstack/react-router'
import { clearAgentKey, createAgent, deleteAgent, getAgents, setAgentKey, updateAgent } from '../lib/api'
import {
  PROVIDERS,
  agentSummary,
  cleanInput,
  emptyInput,
  inputFrom,
  inputIssues,
  keyStatus,
  provider,
  type AgentProfile,
  type AgentProfileInput,
  type AgentProvider,
} from '../lib/agents'
import { formatDate } from '../lib/format'

export const Route = createFileRoute('/agents/')({
  loader: () => getAgents(),
  component: Agents,
  errorComponent: ({ error }) => (
    <main className="page-shell empty-page">
      <p className="eyebrow">Agentes · Error</p>
      <h1>Los agentes no responden.</h1>
      <p className="hero-copy">{error.message}</p>
    </main>
  ),
})

type Notice = { text: string; tone: 'ok' | 'warn' | 'info' }
type Act = (label: string, fn: () => Promise<unknown>, done: string) => Promise<boolean>

function Agents() {
  const { items, secretsKeyConfigured } = Route.useLoaderData()
  const router = useRouter()
  const [msg, setMsg] = useState<Notice | null>(null)
  const [busy, setBusy] = useState(false)

  const act: Act = async (label, fn, done) => {
    setBusy(true)
    setMsg({ text: label, tone: 'info' })
    try {
      await fn()
      setMsg({ text: done, tone: 'ok' })
      await router.invalidate()
      return true
    } catch (e) {
      setMsg({ text: e instanceof Error ? e.message : String(e), tone: 'warn' })
      return false
    } finally {
      setBusy(false)
    }
  }

  return (
    <main className="page-shell detail-page">
      <section className="detail-hero">
        <div>
          <p className="eyebrow">Agentes</p>
          <h1>Con qué modelo habla cada altavoz</h1>
          <p className="hero-copy">
            Cada agente elige proveedor, modelo, voz e idioma. Los declarados en Kubernetes solo se cambian allí; aquí puedes
            crear otros y guardar la clave de cualquiera. Cada altavoz elige su agente en su ficha, y el cambio vale desde la
            siguiente conversación.
          </p>
        </div>
      </section>

      {!secretsKeyConfigured && (
        <p className="device-notice warn">
          El servidor no tiene SEBASTIAN_SECRETS_KEY, así que no puede guardar claves: cada agente usa la clave de su
          proveedor que tenga el agente en su entorno.
        </p>
      )}
      {msg && <p className={`device-notice ${msg.tone}`}>{msg.text}</p>}

      <section className="device-panel">
        <div className="section-heading">
          <div>
            <p className="eyebrow">Agentes</p>
            <h2>Disponibles</h2>
          </div>
          <span className="result-count">{items.length}</span>
        </div>
        <div className="device-list">
          {items.map((agent) => (
            <AgentCard key={agent.id} agent={agent} busy={busy} act={act} canStoreKeys={secretsKeyConfigured} />
          ))}
        </div>
      </section>

      <section className="device-panel">
        <div className="section-heading">
          <div>
            <p className="eyebrow">Nuevo</p>
            <h2>Crear un agente</h2>
          </div>
        </div>
        <NewAgent busy={busy} act={act} />
      </section>
    </main>
  )
}

function AgentCard({ agent, busy, act, canStoreKeys }: Readonly<{ agent: AgentProfile; busy: boolean; act: Act; canStoreKeys: boolean }>) {
  const [editing, setEditing] = useState(false)
  const [key, setKey] = useState('')
  const managed = agent.source === 'kubernetes'
  const status = keyStatus(agent)
  const keyable = canStoreKeys && Boolean(agent.provider)

  return (
    <article className="device-row agent-row">
      <div className="device-identity">
        <div className="device-head">
          <p className="device-name">{agent.name}</p>
          <span className={`device-state ${managed ? 'tone-info' : 'tone-other'}`}>{managed ? 'Kubernetes' : 'panel'}</span>
        </div>
        <p className="device-meta">{agentSummary(agent)}</p>
        <p className="device-meta">
          {agent.deviceCount === 0 ? 'Ningún altavoz lo usa' : agent.deviceCount === 1 ? '1 altavoz lo usa' : `${agent.deviceCount} altavoces lo usan`}
          {' · '}
          <span className={`tone-${status.tone}`}>{status.text}</span>
          {agent.apiKeyUpdatedAt && <> · {formatDate(agent.apiKeyUpdatedAt)}</>}
        </p>

        <form
          className="agent-key"
          onSubmit={(e) => {
            e.preventDefault()
            void act('Guardando la clave…', () => setAgentKey({ data: { profileId: agent.id, apiKey: key.trim() } }), 'Clave guardada. Vale desde la siguiente conversación.').then((ok) => ok && setKey(''))
          }}
        >
          <label className="config-field">
            <span>Clave{agent.provider ? ` de ${provider(agent.provider).label}` : ''}</span>
            <input
              type="password"
              autoComplete="off"
              value={key}
              disabled={!keyable}
              placeholder={agent.apiKeySet ? 'Guardada · escribe otra para cambiarla' : agent.provider ? provider(agent.provider).keyEnv : 'Elige antes un proveedor'}
              onChange={(e) => setKey(e.target.value)}
            />
          </label>
          <button type="submit" className="chip-button primary" disabled={busy || !keyable || key.trim().length < 8}>Guardar clave</button>
          {agent.apiKeySet && (
            <button type="button" className="chip-button" disabled={busy} onClick={() => void act('Quitando la clave…', () => clearAgentKey({ data: { profileId: agent.id } }), 'Clave quitada: usará la del entorno del agente.')}>
              Quitar clave
            </button>
          )}
        </form>

        {editing && !managed && (
          <AgentForm
            initial={inputFrom(agent)}
            submitLabel="Guardar cambios"
            busy={busy}
            onCancel={() => setEditing(false)}
            onSubmit={(input) => act('Guardando…', () => updateAgent({ data: { profileId: agent.id, agent: input } }), 'Agente guardado.').then((ok) => ok && setEditing(false))}
          />
        )}
      </div>
      {!managed && !editing && (
        <div className="device-actions">
          <button type="button" className="chip-button" disabled={busy} onClick={() => setEditing(true)}>Editar</button>
          <button
            type="button"
            className="chip-button danger"
            disabled={busy || agent.deviceCount > 0}
            title={agent.deviceCount > 0 ? 'Cambia antes de agente los altavoces que lo usan' : undefined}
            onClick={() => void act('Borrando…', () => deleteAgent({ data: { profileId: agent.id } }), 'Agente borrado.')}
          >
            Borrar
          </button>
        </div>
      )}
    </article>
  )
}

function NewAgent({ busy, act }: Readonly<{ busy: boolean; act: Act }>) {
  const [round, setRound] = useState(0)
  return (
    <AgentForm
      key={round}
      initial={emptyInput()}
      submitLabel="Crear agente"
      busy={busy}
      onSubmit={(input) => act('Creando…', () => createAgent({ data: input }), `Agente «${input.name}» creado.`).then((ok) => ok && setRound((r) => r + 1))}
    />
  )
}

function AgentForm({
  initial,
  submitLabel,
  busy,
  onSubmit,
  onCancel,
}: Readonly<{ initial: AgentProfileInput; submitLabel: string; busy: boolean; onSubmit: (input: AgentProfileInput) => Promise<unknown>; onCancel?: () => void }>) {
  const [input, setInput] = useState<AgentProfileInput>(initial)
  const [tried, setTried] = useState(false)
  const issues = inputIssues(input)
  const info = provider(input.provider)
  const set = (patch: Partial<AgentProfileInput>) => setInput((prev) => ({ ...prev, ...patch }))

  return (
    <form
      className="agent-form"
      onSubmit={(e) => {
        e.preventDefault()
        setTried(true)
        if (issues.length === 0) void onSubmit(cleanInput(input))
      }}
    >
      <div className="config-form">
        <label className="config-field">
          <span>Nombre</span>
          <input value={input.name} onChange={(e) => set({ name: e.target.value })} placeholder="Phonic salón" />
        </label>
        <label className="config-field">
          <span>Proveedor</span>
          <select value={input.provider} onChange={(e) => set({ provider: e.target.value as AgentProvider })}>
            {PROVIDERS.map((p) => (
              <option key={p.id} value={p.id}>{p.label}</option>
            ))}
          </select>
        </label>
        <label className="config-field">
          <span>Modelo</span>
          <input value={input.model ?? ''} onChange={(e) => set({ model: e.target.value })} placeholder={info.modelHint} />
          <small>Vacío: el que el agente tenga por defecto.</small>
        </label>
        <label className="config-field">
          <span>Voz</span>
          <input value={input.voice ?? ''} onChange={(e) => set({ voice: e.target.value })} placeholder={info.voiceHint} />
          <small>Vacía: la que el agente tenga por defecto.</small>
        </label>
        <label className="config-field">
          <span>Idioma</span>
          <input value={input.language ?? ''} onChange={(e) => set({ language: e.target.value })} placeholder="es" />
          <small>Código ISO. Vacío: lo decide el proveedor.</small>
        </label>
      </div>
      {tried && issues.length > 0 && <p className="device-notice warn">{issues.join(' ')}</p>}
      <div className="device-actions agent-form-actions">
        {onCancel && <button type="button" className="chip-button" disabled={busy} onClick={onCancel}>Cancelar</button>}
        <button type="submit" className="chip-button primary" disabled={busy}>{submitLabel}</button>
      </div>
    </form>
  )
}
