import { createFileRoute } from '@tanstack/react-router'
import { proxyMeetingTranscript } from '../lib/api'

// Downloads (RM-41): /meetings/{id}/transcript?format=txt|srt
export const Route = createFileRoute('/meetings/$meetingId/transcript')({
  server: {
    handlers: {
      GET: async ({ request, params }) => {
        const format = new URL(request.url).searchParams.get('format') === 'srt' ? 'srt' : 'txt'
        return proxyMeetingTranscript(params.meetingId, format)
      },
    },
  },
})
