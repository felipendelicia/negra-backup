import { useState, useEffect, useRef, useCallback } from 'react'
import { useAuth } from 'src/lib/auth'
import { Button } from 'src/components/ui/button'
import { Trash2, Pause, Play, Circle } from 'lucide-react'
import { cn } from 'src/lib/utils'

interface LogEntry {
  id: number
  text: string
  ts: string
}

export default function Console() {
  const { token } = useAuth()
  const [entries, setEntries] = useState<LogEntry[]>([])
  const [paused, setPaused] = useState(false)
  const [connected, setConnected] = useState(false)
  const [error, setError] = useState('')
  const idRef      = useRef(0)
  const bottomRef  = useRef<HTMLDivElement>(null)
  const pausedRef  = useRef(false)
  const wsRef      = useRef<WebSocket | null>(null)
  const bufferRef  = useRef<LogEntry[]>([])

  pausedRef.current = paused

  const connect = useCallback(() => {
    if (!token) return
    setError('')

    const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
    const url   = `${proto}//${window.location.host}/ws/console?token=${encodeURIComponent(token)}`

    try {
      const ws = new WebSocket(url)
      wsRef.current = ws

      ws.onopen = () => setConnected(true)

      ws.onmessage = (e) => {
        const entry: LogEntry = {
          id:   ++idRef.current,
          text: (e.data as string).replace(/\n$/, ''),
          ts:   new Date().toISOString().slice(11, 23),
        }
        if (pausedRef.current) {
          bufferRef.current.push(entry)
        } else {
          setEntries(prev => [...prev.slice(-2000), entry]) // keep last 2k lines
        }
      }

      ws.onclose = () => {
        setConnected(false)
        // Auto-reconnect after 3 s
        setTimeout(() => { if (wsRef.current === ws) connect() }, 3000)
      }

      ws.onerror = () => {
        setError('WebSocket error — reconnecting…')
        ws.close()
      }
    } catch (err) {
      setError(String(err))
    }
  }, [token])

  useEffect(() => {
    connect()
    return () => {
      wsRef.current?.close()
      wsRef.current = null
    }
  }, [connect])

  // Flush buffer when unpausing
  useEffect(() => {
    if (!paused && bufferRef.current.length > 0) {
      const buffered = bufferRef.current.splice(0)
      setEntries(prev => [...prev, ...buffered].slice(-2000))
    }
  }, [paused])

  // Auto-scroll when not paused
  useEffect(() => {
    if (!paused) {
      bottomRef.current?.scrollIntoView({ behavior: 'smooth' })
    }
  }, [entries, paused])

  function clear() {
    setEntries([])
    bufferRef.current = []
  }

  function togglePause() {
    setPaused(p => !p)
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
          <span className={cn(
            'flex items-center gap-1.5 text-xs font-medium',
            connected ? 'text-emerald-500' : 'text-muted-foreground'
          )}>
            <Circle className={cn('h-2 w-2 fill-current', connected && 'animate-pulse')} />
            {connected ? 'Live' : 'Connecting…'}
          </span>
        </div>

        <div className="flex items-center gap-2">
          {paused && bufferRef.current.length > 0 && (
            <span className="text-xs text-yellow-500 font-medium">
              {bufferRef.current.length} buffered
            </span>
          )}
          <Button
            variant="outline"
            size="sm"
            onClick={togglePause}
            className="gap-1.5 text-xs"
          >
            {paused ? <Play className="h-3.5 w-3.5" /> : <Pause className="h-3.5 w-3.5" />}
            {paused ? 'Resume' : 'Pause'}
          </Button>
          <Button
            variant="outline"
            size="sm"
            onClick={clear}
            className="gap-1.5 text-xs"
          >
            <Trash2 className="h-3.5 w-3.5" />
            Clear
          </Button>
        </div>
      </div>

      {error && (
        <p className="text-xs text-destructive shrink-0">{error}</p>
      )}

      {/* Terminal */}
      <div className="flex-1 bg-zinc-950 border border-zinc-800 rounded-lg overflow-auto min-h-0 relative">
        {/* Sticky header bar */}
        <div className="sticky top-0 px-4 py-1.5 bg-zinc-900/80 border-b border-zinc-800 flex items-center gap-2 backdrop-blur-sm">
          <div className="flex gap-1.5">
            <span className="w-2.5 h-2.5 rounded-full bg-zinc-700" />
            <span className="w-2.5 h-2.5 rounded-full bg-zinc-700" />
            <span className="w-2.5 h-2.5 rounded-full bg-zinc-700" />
          </div>
          <span className="text-xs text-zinc-500 font-mono">negra-backup-server — stdout/stderr</span>
        </div>

        <div className="p-4 font-mono text-xs space-y-0.5">
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
