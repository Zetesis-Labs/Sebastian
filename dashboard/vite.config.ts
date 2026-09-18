import viteReact from '@vitejs/plugin-react'
import { tanstackStart } from '@tanstack/react-start/plugin/vite'
import { nitro } from 'nitro/vite'
import { fileURLToPath } from 'node:url'
import { defineConfig } from 'vite'

// The device form reuses the installer's field metadata and validation so the
// two edit the same document (functional spec RF-40).
const installerLib = fileURLToPath(new URL('../web-installer/src/lib', import.meta.url))

export default defineConfig({
  resolve: {
    alias: { '@installer': installerLib },
  },
  server: {
    host: '0.0.0.0',
    port: 3001,
  },
  plugins: [tanstackStart(), nitro(), viteReact()],
})
