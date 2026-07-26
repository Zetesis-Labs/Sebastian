import { useState } from 'react'
import { createFileRoute, useRouter } from '@tanstack/react-router'
import { getDevices, setDeviceProfile, type Device } from '../lib/api'
import { formatDate } from '../lib/format'

export const Route = createFileRoute('/devices')({
  loader: () => getDevices(),
  component: Devices,
  errorComponent: ({ error }) => (
    <main className="page-shell empty-page">
      <p className="eyebrow">Dispositivos · Error</p>
      <h1>El inventario no responde.</h1>
      <p className="hero-copy">{error.message}</p>
    </main>
  ),
})

// Perfiles de fábrica del firmware; un dispositivo puede provisionar otros,
// por eso el perfil deseado también se muestra tal cual venga del inventario.
const QUICK_PROFILES = ['agente', 'micro-usb']

function Devices() {
  const devices = Route.useLoaderData()
  const router = useRouter()
  const [saving, setSaving] = useState<string | null>(null)

  async function apply(deviceId: string, name: string) {
    setSaving(deviceId)
    try {
      await setDeviceProfile({ data: { deviceId, name } })
      await router.invalidate()
    } finally {
      setSaving(null)
    }
  }

  return (
    <main className="page-shell">
      <section className="archive-section">
        <div className="section-heading">
          <div>
            <p className="eyebrow">Flota · Estado deseado</p>
            <h2>Dispositivos</h2>
          </div>
          <span className="result-count">{devices.length} registrados</span>
        </div>

        {devices.length === 0 ? (
          <div className="empty-state">
            <h3>Ningún dispositivo se ha anunciado aún</h3>
            <p>Cada unidad aparece aquí con su primer poll de reconciliación.</p>
          </div>
        ) : (
          <div className="device-list">
            {devices.map((item) => (
              <DeviceRow key={item.id} device={item} saving={saving === item.id} onApply={apply} />
            ))}
          </div>
        )}
      </section>
    </main>
  )
}

function DeviceRow({
  device,
  saving,
  onApply,
}: Readonly<{
  device: Device
  saving: boolean
  onApply: (deviceId: string, name: string) => Promise<void>
}>) {
  const reported = device.reportedProfile ?? null
  const desired = device.desiredProfile ?? null
  const synced = desired === null || desired === reported

  return (
    <article className="device-row">
      <div>
        <h3 className="mono">{device.displayName}</h3>
        <p className="device-meta">
          {reported ? (
            <>
              Ejecutando <strong>{reported}</strong>
              {device.profileReportedAt ? ` · visto ${formatDate(device.profileReportedAt)}` : null}
            </>
          ) : (
            'Sin estado reportado todavía'
          )}
        </p>
      </div>
      <div className="device-actions">
        {!synced && (
          <span className="device-pending">aplicando “{desired}” en el próximo poll…</span>
        )}
        {QUICK_PROFILES.map((name) => (
          <button
            key={name}
            type="button"
            className="chip-button"
            disabled={saving || name === (desired ?? reported)}
            onClick={() => void onApply(device.id, name)}
          >
            {name}
          </button>
        ))}
      </div>
    </article>
  )
}
