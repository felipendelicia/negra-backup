import { useState, useEffect, useRef, useCallback } from 'react'
import { useNavigate } from 'react-router-dom'
import { useAuth } from 'src/lib/auth'
import { Button } from 'src/components/ui/button'
import { Trash2, Pause, Play, Circle, RotateCw } from 'lucide-react'
import { cn } from 'src/lib/utils'

interface LogEntry {
  id: number
  text: string
  ts: string
}

/** Rendered scrollback. */
const MAX_LINES = 2000
/** Hard cap on the pause buffer so a long pause can't exhaust browser memory. */
const MAX_BUFFERED = 2000

const BASE_RECONNECT_MS = 1_000
const MAX_RECONNECT_MS = 30_000

export default function Console() {
  const { token, logout } = useAuth()
  const navigate = useNavigate()

  const [entries, setEntries] = useState<LogEntry[]>([])
  const [paused, setPaused] = useState(false)
  const [bufferedCount, setBufferedCount] = useState(0)
  const [droppedCount, setDroppedCount] = useState(0)
  const [connected, setConnected] = useState(false)
  const [status, setStatus] = useState('')
  const [authExpired, setAuthExpired] = useState(false)

  const idRef        = useRef(0)
  const bottomRef    = useRef<HTMLDivElement>(null)
  const pausedRef    = useRef(false)
  const wsRef        = useRef<WebSocket | null>(null)
  const bufferRef    = useRef<LogEntry[]>([])
  const attemptRef   = useRef(0)
  const retryRef     = useRef<number | null>(null)
  /** Set once we know retrying is pointless (the token is no longer accepted). */
  const authStopRef  = useRef(false)
  const mountedRef   = useRef(false)
  const connectRef   = useRef<() => void>(() => {})

  const clearRetry = useCallback(() => {
    if (retryRef.current !== null) {
      window.clearTimeout(retryRef.current)
      retryRef.current = null
    }
  }, [])

  /**
   * A WebSocket handshake rejected with 401 is indistinguishable from a network
   * drop in the browser (the HTTP status is never exposed). Probing an
   * authenticated REST endpoint tells us which one it was.
   */
  const tokenRejected = useCallback(async (): Promise<boolean> => {
    if (!token) return true
    try {
      const res = await fetch('/api/agents', {
        headers: { Authorization: `Bearer ${token}` },
      })
      return res.status === 401
    } catch {
      return false // network failure, not an auth failure
    }
  }, [token])

  const scheduleRetry = useCallback(() => {
    clearRetry()
    const attempt = attemptRef.current++
    const delay = Math.min(BASE_RECONNECT_MS * 2 ** attempt, MAX_RECONNECT_MS)
    setStatus(`Disconnected — reconnecting in ${Math.round(delay / 1000)}s…`)
    retryRef.current = window.setTimeout(() => {
      retryRef.current = null
      connectRef.current()
    }, delay)
  }, [clearRetry])

  const connect = useCallback(() => {
    if (!token || authStopRef.current || !mountedRef.current) return

    const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
    const url   = `${proto}//${window.location.host}/ws/console?token=${encodeURIComponent(token)}`

    let ws: WebSocket
    try {
      ws = new WebSocket(url)
    } catch (err) {
      setStatus(`Could not open the log stream: ${String(err)}`)
      scheduleRetry()
      return
    }

    let opened = false
    wsRef.current = ws

    ws.onopen = () => {
      opened = true
      attemptRef.current = 0
      setConnected(true)
      setStatus('')
    }

    ws.onmessage = (e) => {
      const entry: LogEntry = {
        id:   ++idRef.current,
        text: (e.data as string).replace(/\n$/, ''),
        ts:   new Date().toISOString().slice(11, 23),
      }
      if (pausedRef.current) {
        const buf = bufferRef.current
        buf.push(entry)
        if (buf.length > MAX_BUFFERED) {
          const dropped = buf.length - MAX_BUFFERED
          buf.splice(0, dropped) // oldest lines go first
          setDroppedCount(n => n + dropped)
        }
        setBufferedCount(buf.length)
      } else {
        setEntries(prev => [...prev, entry].slice(-MAX_LINES))
      }
    }

    // onerror is always followed by onclose; reconnect logic lives there only.
    ws.onerror = () => {}

    ws.onclose = () => {
      if (wsRef.current !== ws) return // superseded or unmounted
      wsRef.current = null
      setConnected(false)
      if (authStopRef.current || !mountedRef.current) return

      if (!opened) {
        // Never established: the handshake itself was refused.
        void tokenRejected().then(rejected => {
          if (authStopRef.current || !mountedRef.current) return
          if (rejected) {
            authStopRef.current = true
            clearRetry()
            setAuthExpired(true)
            setStatus('')
            return
          }
          scheduleRetry()
        })
        return
      }
      scheduleRetry()
    }
  }, [token, tokenRejected, scheduleRetry, clearRetry])

  // Keep the retry timer pointing at the current connect closure.
  useEffect(() => {
    connectRef.current = connect
  }, [connect])

  useEffect(() => {
    mountedRef.current = true
    connect()
    return () => {
      mountedRef.current = false
      clearRetry()
      const ws = wsRef.current
      wsRef.current = null
      ws?.close()
    }
  }, [connect, clearRetry])

  // Auto-scroll when not paused
  useEffect(() => {
    if (!paused) {
      bottomRef.current?.scrollIntoView({ behavior: 'smooth' })
    }
  }, [entries, paused])

  function clear() {
    setEntries([])
    bufferRef.current = []
    setBufferedCount(0)
    setDroppedCount(0)
  }

  function togglePause() {
    if (paused) {
      const buffered = bufferRef.current
      bufferRef.current = []
      pausedRef.current = false
      setBufferedCount(0)
      setDroppedCount(0)
      setPaused(false)
      if (buffered.length > 0) {
        setEntries(prev => [...prev, ...buffered].slice(-MAX_LINES))
      }
    } else {
      pausedRef.current = true
      setPaused(true)
    }
  }

  function reconnectNow() {
    authStopRef.current = false
    attemptRef.current = 0
    clearRetry()
    setAuthExpired(false)
    setStatus('')
    connect()
  }

  function signInAgain() {
    logout()
    navigate('/login')
  }

  // Colour-code log levels
  function lineClass(text: string): string {
    const t = text.toLowerCase()
    if (t.includes('error') || t.includes('fatal') || t.includes('✗'))
      return 'text-red-400'
    if (t.includes('warn'))
      return 'text-yellow-400'
    if (t.includes('✓') || t.includes('success'))
      return 'text-emerald-400'
    if (t.includes('start') || t.includes('listen') || t.includes('connect'))
      return 'text-sky-400'
    return 'text-zinc-300'
  }

  return (
    <div className="flex flex-col h-[calc(100vh-3rem)] gap-4">
      {/* Header */}
      <div className="flex items-center justify-between shrink-0">
        <div className="flex items-center gap-3">
          <h2 className="font-sans font-bold text-2xl tracking-tight">Console</h2>
          <span
            className={cn(
              'flex items-center gap-1.5 text-xs font-medium',
              connected ? 'text-emerald-500' : 'text-muted-foreground'
            )}
            role="status"
          >
            <Circle aria-hidden="true" className={cn('h-2 w-2 fill-current', connected && 'animate-pulse')} />
            {connected ? 'Live' : authExpired ? 'Disconnected' : 'Connecting…'}
          </span>
        </div>

        <div className="flex items-center gap-2">
          {paused && bufferedCount > 0 && (
            <span className="text-xs text-yellow-500 font-medium">
              {bufferedCount} buffered
              {droppedCount > 0 && ` · ${droppedCount} dropped`}
            </span>
          )}
          <Button
            variant="outline"
            size="sm"
            onClick={togglePause}
            className="gap-1.5 text-xs"
          >
            {paused
              ? <Play aria-hidden="true" className="h-3.5 w-3.5" />
              : <Pause aria-hidden="true" className="h-3.5 w-3.5" />}
            {paused ? 'Resume' : 'Pause'}
          </Button>
          <Button
            variant="outline"
            size="sm"
            onClick={clear}
            className="gap-1.5 text-xs"
          >
            <Trash2 aria-hidden="true" className="h-3.5 w-3.5" />
            Clear
          </Button>
        </div>
      </div>

      {authExpired ? (
        <div
          role="alert"
          className="shrink-0 flex flex-wrap items-center gap-3 rounded border border-destructive/25 bg-destructive/8 px-3 py-2 text-xs text-destructive"
        >
          <span>Your session expired, so the log stream was refused. Sign in again to resume it.</span>
          <Button size="sm" variant="outline" className="h-7 text-xs" onClick={signInAgain}>
            Sign in again
          </Button>
          <Button size="sm" variant="ghost" className="h-7 gap-1.5 text-xs" onClick={reconnectNow}>
            <RotateCw aria-hidden="true" className="h-3 w-3" />
            Retry
          </Button>
        </div>
      ) : status ? (
        <p role="status" className="text-xs text-muted-foreground shrink-0 flex items-center gap-2">
          {status}
          <Button size="sm" variant="ghost" className="h-6 gap-1.5 text-xs" onClick={reconnectNow}>
            <RotateCw aria-hidden="true" className="h-3 w-3" />
            Retry now
          </Button>
        </p>
      ) : null}

      {/* Terminal */}
      <div className="flex-1 bg-zinc-950 border border-zinc-800 rounded-lg overflow-auto min-h-0 relative">
        {/* Sticky header bar */}
        <div className="sticky top-0 px-4 py-1.5 bg-zinc-900/80 border-b border-zinc-800 flex items-center gap-2 backdrop-blur-sm">
          <div className="flex gap-1.5" aria-hidden="true">
            <span className="w-2.5 h-2.5 rounded-full bg-zinc-700" />
            <span className="w-2.5 h-2.5 rounded-full bg-zinc-700" />
            <span className="w-2.5 h-2.5 rounded-full bg-zinc-700" />
          </div>
          <span className="text-xs text-zinc-500 font-mono">negra-backup-server — stdout/stderr</span>
        </div>

        <div
          className="p-4 font-mono text-xs space-y-0.5"
          role="log"
          aria-live="polite"
          aria-label="Server output"
        >
          {entries.length === 0 ? (
            <span className="text-zinc-600">
              {connected ? 'Waiting for output…' : 'Connecting to server…'}
            </span>
          ) : (
            entries.map(entry => (
              <div key={entry.id} className="flex gap-3 leading-relaxed">
                <span className="text-zinc-600 shrink-0 select-none w-[7ch]">{entry.ts}</span>
                <span className={cn('whitespace-pre-wrap break-all', lineClass(entry.text))}>
                  {entry.text}
                </span>
              </div>
            ))
          )}
          <div ref={bottomRef} />
        </div>
      </div>
    </div>
  )
}
