import { useEffect, useRef, useState } from 'react'
import { getToken } from './auth'

/**
 * Watching a restore happen.
 *
 * The restore reports on two channels and needs both:
 *
 *  - `/api/runs/{id}/logs/ws` is where the server broadcasts `restore_progress`,
 *    `restore_done` and `restore_failed` (internal/ws/handler.go). It carries the
 *    per-file ticks — but the handler only keeps the socket open while the run's
 *    row says `running`, and only a *finished* run can be restored, so today it
 *    closes the connection immediately with "run not active". It is opened anyway:
 *    it costs one handshake, and the day that check is relaxed the per-file
 *    progress appears here with no change on this side.
 *
 *  - `/ws/console` is the server's own log, which does reach the browser for a
 *    finished run. The dispatch, the agent's "restoring … (restore <id>)", the
 *    agent's warnings, and the final "restore <id> completed/failed on agent …"
 *    all pass through it, every one of them carrying the run id. Filtering on that
 *    id is what makes the outcome visible today.
 *
 * Neither socket replays history, so both are opened before the POST is sent.
 */

const BASE_RECONNECT_MS = 1_000
const MAX_RECONNECT_MS = 15_000
const MAX_ATTEMPTS = 5
/** Rendered scrollback. A restore of a large tree can emit a line per file. */
const MAX_LINES = 2_000

export type RestoreOutcome = 'pending' | 'done' | 'failed'

export interface RestoreLine {
  id: number
  text: string
}

export interface RestoreProgress {
  lines: RestoreLine[]
  /** True once the console stream is live — i.e. the outcome will be seen. */
  watching: boolean
  outcome: RestoreOutcome
  /** The message of the terminal line, when the restore ended. */
  outcomeDetail: string
  status: string
}

/**
 * Reads the terminal state out of a line.
 *
 * The server writes "restore <id> completed on agent <id>: N files" and the agent
 * writes "restore completed: …" / "restore <id> failed: …", so the id in the
 * middle is optional. Warnings look like "restore <id>: skipping symlink …" and
 * must not match either.
 */
function classify(text: string): RestoreOutcome {
  if (/restore\s+(?:\S+\s+)?failed\b/i.test(text)) return 'failed'
  if (/restore\s+(?:\S+\s+)?completed\b/i.test(text)) return 'done'
  return 'pending'
}

/** chi's request log: `2026/08/04 19:15:06 POST /api/… HTTP/1.1 202 209B 1ms`. */
const ACCESS_LOG_RE = /^\d{4}\/\d{2}\/\d{2} \d{2}:\d{2}:\d{2} (?:GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS) \S+ HTTP\//

/**
 * Whether a console line belongs to this restore.
 *
 * Carrying the run id is necessary but not sufficient: the request log echoes
 * the same id on every poll of the run's own endpoints, and the backup that
 * produced the archive logged it too. Requiring the word as well keeps the panel
 * to the restore's own story, and the request log is dropped outright — an
 * operator watching files come back does not need the HTTP status of the call
 * that started them.
 */
function belongsToRestore(text: string, runId: string): boolean {
  if (!text.includes(runId)) return false
  if (ACCESS_LOG_RE.test(text)) return false
  return /restor/i.test(text)
}

function socketURL(path: string): string {
  const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
  const token = getToken()
  // The token rides in the query string: browsers cannot set headers on a
  // WebSocket handshake.
  const qs = token ? `?token=${encodeURIComponent(token)}` : ''
  return `${proto}//${window.location.host}${path}${qs}`
}

const empty: RestoreProgress = {
  lines: [], watching: false, outcome: 'pending', outcomeDetail: '', status: '',
}

/**
 * @param runId  the run being restored — also the restore id the agent echoes.
 * @param active whether a restore is in flight. Both sockets close when false.
 */
export function useRestoreProgress(runId: string | null, active: boolean): RestoreProgress {
  const [state, setState] = useState<RestoreProgress>(empty)
  const idRef = useRef(0)

  // Watching a different restore (or none) starts from a blank panel. Doing it
  // during render rather than from an effect is React's own pattern for state
  // derived from props: an effect would render the previous restore's output
  // once before clearing it.
  const session = active && runId ? runId : null
  const [prevSession, setPrevSession] = useState(session)
  if (session !== prevSession) {
    setPrevSession(session)
    setState(empty)
  }

  useEffect(() => {
    if (!runId || !active) return

    let cancelled = false
    const sockets = new Set<WebSocket>()
    const timers = new Set<number>()

    const push = (text: string) => {
      if (cancelled) return
      setState(prev => {
        const outcome = prev.outcome === 'pending' ? classify(text) : prev.outcome
        return {
          ...prev,
          lines: [...prev.lines, { id: ++idRef.current, text }].slice(-MAX_LINES),
          outcome,
          outcomeDetail: outcome !== 'pending' && prev.outcome === 'pending' ? text : prev.outcomeDetail,
        }
      })
    }

    /**
     * One reconnecting socket.
     *
     * `giveUpWhenSilent` is for the run-log stream: the server accepts the
     * handshake and closes it again without sending anything, so retrying on a
     * backoff would be an infinite loop of handshakes against a check that will
     * not change while this restore runs.
     */
    function connect(
      path: string,
      onLine: (text: string) => void,
      opts: { giveUpWhenSilent?: boolean; onOpen?: (up: boolean) => void } = {},
    ) {
      let attempt = 0

      const open = () => {
        if (cancelled) return

        let ws: WebSocket
        try {
          ws = new WebSocket(socketURL(path))
        } catch {
          retry()
          return
        }
        sockets.add(ws)

        let received = false

        ws.onopen = () => {
          attempt = 0
          opts.onOpen?.(true)
        }
        ws.onmessage = (e) => {
          received = true
          onLine((e.data as string).replace(/\n$/, ''))
        }
        // onerror is always followed by onclose; reconnect logic lives there only.
        ws.onerror = () => {}
        ws.onclose = () => {
          sockets.delete(ws)
          if (cancelled) return
          opts.onOpen?.(false)
          if (opts.giveUpWhenSilent && !received) return
          retry()
        }
      }

      const retry = () => {
        if (cancelled || attempt >= MAX_ATTEMPTS) return
        const delay = Math.min(BASE_RECONNECT_MS * 2 ** attempt, MAX_RECONNECT_MS)
        attempt++
        const t = window.setTimeout(() => {
          timers.delete(t)
          open()
        }, delay)
        timers.add(t)
      }

      open()
    }

    // The console stream is the one that actually delivers today, so it owns the
    // "watching" flag and the connection status the panel shows.
    connect(
      '/ws/console',
      (text) => { if (belongsToRestore(text, runId)) push(text) },
      {
        onOpen: (up) => {
          if (cancelled) return
          setState(prev => ({
            ...prev,
            watching: up,
            status: up ? '' : 'Server log stream lost — reconnecting…',
          }))
        },
      },
    )

    // Per-file progress, if the server ever keeps this socket open for a
    // finished run. Silent failure is expected and is not reported.
    connect(`/api/runs/${runId}/logs/ws`, push, { giveUpWhenSilent: true })

    return () => {
      cancelled = true
      for (const t of timers) window.clearTimeout(t)
      for (const ws of sockets) ws.close()
    }
  }, [runId, active])

  return state
}
