import { readFile } from 'node:fs/promises'
import { createFileRoute } from '@tanstack/react-router'

// The embedded web installer (block 0): the React app in web-installer/ built
// with base /installer/ into public/installer by `npm run build:installer`.
// Its assets are static files; the page itself is served here so that
// /installer works the same in `vite dev` (no directory index) and in the
// nitro output.
const candidates = ['public/installer/index.html', '.output/public/installer/index.html']

async function installerPage(): Promise<string | null> {
  for (const path of candidates) {
    try {
      return await readFile(path, 'utf8')
    } catch {
      // try the next location
    }
  }
  return null
}

export const Route = createFileRoute('/installer')({
  server: {
    handlers: {
      GET: async () => {
        const html = await installerPage()
        if (html === null) {
          return new Response('El instalador embebido no está construido: ejecuta `npm run build:installer` en dashboard/.', {
            status: 503,
            headers: { 'Content-Type': 'text/plain; charset=utf-8' },
          })
        }
        return new Response(html, { headers: { 'Content-Type': 'text/html; charset=utf-8', 'Cache-Control': 'no-store' } })
      },
    },
  },
})
