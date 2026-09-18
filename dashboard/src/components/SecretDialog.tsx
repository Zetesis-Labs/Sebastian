import { useEffect, useState } from 'react'
import { toDataURL } from 'qrcode'

// The per-device secret, shown once (RF-52): copy it or scan it; closing the
// dialog is the last time it is visible.
export function SecretDialog({ secret, onClose }: Readonly<{ secret: string; onClose: () => void }>) {
  const [qr, setQr] = useState<string | null>(null)
  const [copied, setCopied] = useState(false)

  useEffect(() => {
    let alive = true
    toDataURL(secret, { margin: 1, width: 192, color: { dark: '#f2eee6', light: '#111210' } })
      .then((url) => alive && setQr(url))
      .catch(() => alive && setQr(null))
    return () => {
      alive = false
    }
  }, [secret])

  async function copy() {
    try {
      await navigator.clipboard.writeText(secret)
      setCopied(true)
    } catch {
      setCopied(false)
    }
  }

  return (
    <div className="secret-dialog" role="dialog" aria-modal="true" aria-label="Secreto del altavoz">
      <div className="secret-card">
        <p className="eyebrow">Se muestra una sola vez</p>
        <h3>Secreto del altavoz</h3>
        <p className="device-meta">
          Guárdalo si vas a ceder este altavoz a otro control room: con él, y sin el secreto de organización, podrán adoptarlo por IP. Al cerrar no se puede volver a ver, solo regenerar.
        </p>
        <code className="secret-value">{secret}</code>
        {qr && <img className="secret-qr" src={qr} alt="QR con el secreto" width={192} height={192} />}
        <div className="device-actions">
          <button type="button" className="chip-button" onClick={() => void copy()}>{copied ? 'Copiado' : 'Copiar'}</button>
          <button type="button" className="chip-button primary" onClick={onClose}>Cerrar</button>
        </div>
      </div>
    </div>
  )
}
