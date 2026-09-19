import { createFileRoute } from '@tanstack/react-router'
import { proxyMeetingAudio } from '../lib/api'

// The player's source (RM-41/47): served by the dashboard process with the
// admin secret, Range passed through so seeking works.
export const Route = createFileRoute('/meetings/$meetingId/audio')({
  server: {
    handlers: {
      GET: async ({ request, params }) => proxyMeetingAudio(params.meetingId, request.headers.get('range')),
    },
  },
})
