import { useEffect, useRef, useState } from 'react'
import { getToken } from './auth'

/**
 * Watching a restore happen.
 *
 * The restore reports on two channels and both are read:
 *
 *  - `/api/runs/{id}/logs/ws` — the run's own stream, where the server publishes
 *    `restore_progress`, `restore_done` and `restore_failed`
 *    (internal/ws/handler.go). This is the authoritative one: it carries the
 *    per-file ticks and the final counts.
 *
 *  - `/ws/console` — the server's log. It adds what the run stream does not
 *    carry: the dispatch line, which archive the agent opened, and the agent's
 *    own non-fatal warnings ("skipping symlink …"), which are written with
 *    `log.Printf` and forwarded as `agent_log`. Every one of them names the run
 *    id, which is how they are told apart from other jobs' traffic.
 *
 * Both describe the same events, so the terminal line is deduplicated (see
 * `push`) rather than shown two or three times over.
 *
 * Neither socket replays history, so both are opened before the POST is sent.
 */

const BASE_RECONNECT_MS = 1_000
const MAX_RECONNECT_MS = 15_000
const MAX_ATTEMPTS = 5
/**
 * A close this soon after opening, with nothing sent, is a server that accepted
 * the handshake only to refuse the subscription — which is what a server from
 * before the run stream was opened to finished runs does with every restore.
 * Reconnecting into that is an endless handshake loop, because a socket that
 * opens resets the backoff.
 */
const REFUSAL_MS = 1_000
const MAX_REFUSALS = 2
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
        const verdict = classify(text)
        // The end of a restore is announced up to three times — by the agent, by
        // the server's log, and on the run's own stream — and all three arrive
        // here. The first one wins; the rest are dropped rather than shown as
        // three different-looking ways of saying "it worked".
        if (verdict !== 'pending' && prev.outcome !== 'pending') return prev
        return {
          ...prev,
          lines: [...prev.lines, { id: ++idRef.current, text }].slice(-MAX_LINES),
          outcome: prev.outcome === 'pending' ? verdict : prev.outcome,
          outcomeDetail: verdict !== 'pending' && prev.outcome === 'pending' ? text : prev.outcomeDetail,
        }
      })
    }

    /** One reconnecting socket. */
    function connect(
      path: string,
      onLine: (text: string) => void,
      opts: { onOpen?: (up: boolean) => void } = {},
    ) {
      let attempt = 0
      let refusals = 0

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
        let openedAt = 0

        ws.onopen = () => {
          // A socket that stays up resets the backoff; one that is accepted and
          // dropped straight away does not get to reset it forever.
          openedAt = Date.now()
          attempt = 0
          opts.onOpen?.(true)
        }
        ws.onmessage = (e) => {
          received = true
          refusals = 0
          onLine((e.data as string).replace(/\n$/, ''))
        }
        // onerror is always followed by onclose; reconnect logic lives there only.
        ws.onerror = () => {}
        ws.onclose = () => {
          sockets.delete(ws)
          if (cancelled) return
          opts.onOpen?.(false)
          if (!received && openedAt > 0 && Date.now() - openedAt < REFUSAL_MS) {
            refusals++
            if (refusals >= MAX_REFUSALS) return // this server will not serve it
          }
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

    // The run's own stream: per-file progress and the final counts. It owns the
    // "watching" flag, because losing it is what actually blinds the panel.
    connect(`/api/runs/${runId}/logs/ws`, push, {
      onOpen: (up) => {
        if (cancelled) return
        setState(prev => ({
          ...prev,
          watching: up,
          status: up ? '' : 'Progress stream lost — reconnecting…',
        }))
      },
    })

    // The server log, for the context the run stream does not carry: which
    // archive was opened, and the agent's non-fatal warnings.
    connect('/ws/console', (text) => { if (belongsToRestore(text, runId)) push(text) })

    return () => {
      cancelled = true
      for (const t of timers) window.clearTimeout(t)
      for (const ws of sockets) ws.close()
    }
  }, [runId, active])

  return state
}
