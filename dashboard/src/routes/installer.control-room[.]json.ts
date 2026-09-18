import { createFileRoute } from '@tanstack/react-router'
import { controlRoomForInstaller } from '../lib/api'

// Pre-fill for the embedded web installer (block 0): served from the dashboard
// process, which is the only place the admin secret lives. The installer at
// /installer/ fetches ./control-room.json and fills the form with this control
// room; whoever can open the dashboard can read this, exactly like the rest of
// the panel (it is protected by the network / tailnet, not by a login).
export const Route = createFileRoute('/installer/control-room.json')({
  server: {
    handlers: {
      GET: async () => {
        try {
          const room = await controlRoomForInstaller()
          return Response.json(
            {
              name: room.name,
              tokenServerUrl: `${room.apiUrl.replace(/\/$/, '')}/token`,
              syslogIp: room.syslogIp ?? '',
              syslogPort: room.syslogPort ?? 514,
              orgSecret: room.orgSecret ?? '',
              adoptPort: room.adoptPort,
            },
            { headers: { 'Cache-Control': 'no-store' } },
          )
        } catch (error) {
          return Response.json({ error: error instanceof Error ? error.message : String(error) }, { status: 503 })
        }
      },
    },
  },
})
