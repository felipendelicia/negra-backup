import { useState, useEffect, useMemo, useRef } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { api } from 'src/lib/api'
import { getToken } from 'src/lib/auth'
import { StatusBadge } from 'src/components/StatusBadge'
import { Button } from 'src/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from 'src/components/ui/card'
import { Sheet, SheetContent, SheetHeader, SheetTitle } from 'src/components/ui/sheet'
import { formatDate, formatBytes, formatRelative } from 'src/lib/utils'
import { FileText, X } from 'lucide-react'
import { cn } from 'src/lib/utils'
import type { BackupRun } from 'src/lib/types'

/** Rendered scrollback per run. */
const MAX_LOG_LINES = 2000
const BASE_RECONNECT_MS = 1_000
const MAX_RECONNECT_MS = 15_000
const MAX_RECONNECT_ATTEMPTS = 5

/** Synthetic transcript for a run that already finished. */
function summaryLines(run: BackupRun): string[] {
  const lines: string[] = []
  if (run.status === 'success') {
    lines.push(`✓ Backup completed successfully`)
    if (run.file_count != null) lines.push(`  Files backed up: ${run.file_count}`)
    if (run.size_bytes  != null) lines.push(`  Total size: ${formatBytes(run.size_bytes)}`)
    if (run.storage_path)        lines.push(`  Stored at: ${run.storage_path}`)
    if (run.finished_at)         lines.push(`  Finished: ${new Date(run.finished_at).toLocaleString()}`)
  } else if (run.status === 'failed') {
    lines.push(`✗ Backup failed`)
    if (run.error_message) lines.push(`  Error: ${run.error_message}`)
    if (run.finished_at)   lines.push(`  Failed at: ${new Date(run.finished_at).toLocaleString()}`)
  } else if (run.status === 'cancelled') {
    lines.push(`⊘ Run cancelled by user`)
    if (run.finished_at)  lines.push(`  Cancelled at: ${new Date(run.finished_at).toLocaleString()}`)
  }
  return lines
}

interface LiveState {
  runId: string
  connected: boolean
  error: string
  lines: string[]
}

const emptyLive: LiveState = { runId: '', connected: false, error: '', lines: [] }

/**
 * Streams logs for a running backup. Finished runs render a summary derived
 * from the run itself — no state, so switching runs can never show stale lines.
 */
function useLiveLogs(run: BackupRun | null) {
  const runId = run?.id ?? null
  const isRunning = run?.status === 'running'

  const [live, setLive] = useState<LiveState>(emptyLive)
  const wsRef = useRef<WebSocket | null>(null)

  const summary = useMemo(
    () => (run && run.status !== 'running' ? summaryLines(run) : []),
    [run]
  )

  useEffect(() => {
    if (!runId || !isRunning) return

    let cancelled = false
    let attempt = 0
    let timer: number | null = null

    // Only ever writes state belonging to the run this effect is bound to.
    const update = (fn: (s: LiveState) => LiveState) => {
      if (cancelled) return
      setLive(prev => fn(prev.runId === runId ? prev : { ...emptyLive, runId }))
    }

    const scheduleRetry = () => {
      if (cancelled) return
      if (attempt >= MAX_RECONNECT_ATTEMPTS) {
        update(s => ({ ...s, error: 'Log stream unavailable. Close and reopen this run to retry.' }))
        return
      }
      const delay = Math.min(BASE_RECONNECT_MS * 2 ** attempt, MAX_RECONNECT_MS)
      attempt++
      update(s => ({ ...s, error: `Log stream disconnected — reconnecting in ${Math.round(delay / 1000)}s…` }))
      timer = window.setTimeout(open, delay)
    }

    function open() {
      timer = null
      if (cancelled) return

      const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
      // The token rides in the query string: browsers cannot set headers on a
      // WebSocket handshake.
      const token = getToken()
      const qs = token ? `?token=${encodeURIComponent(token)}` : ''
      const url = `${proto}//${window.location.host}/api/runs/${runId}/logs/ws${qs}`

      let ws: WebSocket
      try {
        ws = new WebSocket(url)
      } catch {
        scheduleRetry()
        return
      }

      wsRef.current = ws

      ws.onopen = () => {
        attempt = 0
        update(s => ({ ...s, connected: true, error: '' }))
      }
      ws.onmessage = (e) => {
        update(s => ({ ...s, lines: [...s.lines, e.data as string].slice(-MAX_LOG_LINES) }))
      }
      // onerror is always followed by onclose; reconnect logic lives there only.
      ws.onerror = () => {}
      ws.onclose = () => {
        if (wsRef.current !== ws) return // superseded or unmounted
        wsRef.current = null
        update(s => ({ ...s, connected: false }))
        scheduleRetry()
      }
    }

    open()

    return () => {
      cancelled = true
      if (timer !== null) window.clearTimeout(timer)
      const ws = wsRef.current
      wsRef.current = null
      ws?.close()
    }
  }, [runId, isRunning])

  const active = live.runId === runId ? live : emptyLive

  return {
    logs: isRunning ? active.lines : summary,
    connected: isRunning && active.connected,
    error: isRunning ? active.error : '',
  }
}

const FILTERS = ['', 'running', 'success', 'failed', 'cancelled'] as const

export default function Runs() {
  const queryClient = useQueryClient()
  const [selectedRunId, setSelectedRunId] = useState<string | null>(null)
  const [filterStatus, setFilterStatus] = useState<string>('')
  const logsEndRef = useRef<HTMLDivElement>(null)

  const cancelMutation = useMutation({
    mutationFn: (runId: string) => api.cancelRun(runId),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['runs'] })
      toast.success('Run cancelled')
      setSelectedRunId(null)
    },
    onError: (err: Error) => toast.error(`Failed to cancel: ${err.message}`),
  })

  const { data: runs = [], isLoading, isError } = useQuery({
    queryKey: ['runs'],
    queryFn: ({ signal }) => api.listRuns(undefined, signal),
    refetchInterval: 5_000,
  })

  const { data: jobs = [] } = useQuery({
    queryKey: ['jobs'],
    queryFn: ({ signal }) => api.listJobs(signal),
  })

  // Read the selection out of the polled list so an open drawer follows the
  // run's real status instead of freezing on the snapshot taken at click time.
  const selectedRun = selectedRunId ? runs.find(r => r.id === selectedRunId) ?? null : null

  const { logs, connected, error: logsError } = useLiveLogs(selectedRun)

  useEffect(() => {
    logsEndRef.current?.scrollIntoView({ behavior: 'smooth' })
  }, [logs])

  const filtered = filterStatus ? runs.filter(r => r.status === filterStatus) : runs

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <h2 className="font-sans font-bold text-2xl tracking-tight">Run History</h2>

        {/* Filter pills */}
        <div className="flex gap-1" role="group" aria-label="Filter runs by status">
          {FILTERS.map(s => (
            <button
              key={s}
              type="button"
              onClick={() => setFilterStatus(s)}
              aria-pressed={filterStatus === s}
              className={cn(
                'px-3 py-1 rounded text-xs font-medium transition-colors',
                filterStatus === s
                  ? 'bg-foreground text-background'
                  : 'text-muted-foreground hover:text-foreground hover:bg-accent'
              )}
            >
              {s || 'All'}
            </button>
          ))}
        </div>
      </div>

      <Card>
        <CardHeader className="pb-2">
          <CardTitle className="text-sm font-semibold uppercase tracking-wider text-muted-foreground">
            Runs
            {filtered.length > 0 && (
              <span className="ml-2 text-xs font-normal normal-case">({filtered.length})</span>
            )}
          </CardTitle>
        </CardHeader>
        <CardContent className="px-0">
          {isLoading && <p className="text-sm text-muted-foreground px-6 py-4">Loading…</p>}
          {isError   && <p className="text-sm text-destructive px-6 py-4">Failed to load data.</p>}
          {!isLoading && filtered.length === 0 && (
            <p className="text-sm text-muted-foreground px-6 py-4">No runs found.</p>
          )}
          <div>
            {filtered.map((run, i) => {
              const job = jobs.find(j => j.id === run.job_id)
              return (
                <div
                  key={run.id}
                  className={cn(
                    'flex items-center gap-4 px-6 py-3 hover:bg-accent/50 transition-colors group',
                    i < filtered.length - 1 && 'border-b border-border'
                  )}
                >
                  <StatusBadge status={run.status} className="w-16 shrink-0" />
                  <div className="flex-1 min-w-0">
                    <div className="text-sm font-medium truncate">
                      {job?.name ?? <span className="font-mono text-muted-foreground">{run.job_id.slice(0, 8)}</span>}
                    </div>
                    <div className="text-xs text-muted-foreground">
                      {formatRelative(run.started_at)}
                      {run.file_count != null && ` · ${run.file_count} files`}
                      {run.size_bytes  != null && ` · ${formatBytes(run.size_bytes)}`}
                    </div>
                    {run.error_message && (
                      <div className="text-xs text-destructive truncate mt-0.5">{run.error_message}</div>
                    )}
                  </div>
                  <div className="text-xs text-muted-foreground shrink-0 hidden sm:block">
                    {run.finished_at ? formatDate(run.finished_at) : '—'}
                  </div>
                  {run.status === 'running' && (
                    <Button
                      variant="ghost"
                      size="icon"
                      className="h-7 w-7 opacity-0 group-hover:opacity-100 focus-visible:opacity-100 transition-opacity text-muted-foreground hover:text-destructive"
                      onClick={(e) => { e.stopPropagation(); cancelMutation.mutate(run.id) }}
                      disabled={cancelMutation.isPending}
                      title="Cancel run"
                      aria-label={`Cancel run ${run.id.slice(0, 8)}`}
                    >
                      <X aria-hidden="true" className="h-3.5 w-3.5" />
                    </Button>
                  )}
                  <Button
                    variant="ghost"
                    size="icon"
                    className="h-7 w-7 opacity-0 group-hover:opacity-100 focus-visible:opacity-100 transition-opacity"
                    onClick={() => setSelectedRunId(run.id)}
                    title="View logs"
                    aria-label={`View logs for run ${run.id.slice(0, 8)}`}
                  >
                    <FileText aria-hidden="true" className="h-3.5 w-3.5" />
                  </Button>
                </div>
              )
            })}
          </div>
        </CardContent>
      </Card>

      {/* ── Log drawer ──────────────────────────────────────────────── */}
      <Sheet open={selectedRun !== null} onOpenChange={(o) => !o && setSelectedRunId(null)}>
        <SheetContent side="right" className="w-full sm:max-w-2xl flex flex-col gap-0 p-0">
          <SheetHeader className="px-5 py-4 border-b">
            <SheetTitle className="flex items-center gap-3 text-sm">
              <span>Run Logs</span>
              {selectedRun && (
                <>
                  <span className="font-mono text-xs text-muted-foreground bg-muted px-1.5 py-0.5 rounded">
                    {selectedRun.id.slice(0, 8)}
                  </span>
                  <StatusBadge status={selectedRun.status} />
                  {selectedRun.status === 'running' && (
                    <>
                      <span className={cn('text-xs', connected ? 'text-emerald-500' : 'text-muted-foreground')}>
                        {connected ? '● live' : '○ connecting…'}
                      </span>
                      <Button
                        variant="ghost"
                        size="sm"
                        className="h-6 px-2 text-xs text-muted-foreground hover:text-destructive ml-auto"
                        onClick={() => cancelMutation.mutate(selectedRun.id)}
                        disabled={cancelMutation.isPending}
                      >
                        <X aria-hidden="true" className="h-3 w-3 mr-1" />
                        Cancel
                      </Button>
                    </>
                  )}
                </>
              )}
            </SheetTitle>
          </SheetHeader>

          {selectedRun && (
            <div className="flex-1 overflow-hidden flex flex-col p-5 gap-3">
              {/* Meta row */}
              <div className="flex items-center gap-4 text-xs text-muted-foreground">
                <span>{formatDate(selectedRun.started_at)}</span>
                {selectedRun.size_bytes != null && <span>{formatBytes(selectedRun.size_bytes)}</span>}
                {selectedRun.file_count != null && <span>{selectedRun.file_count} files</span>}
              </div>

              {selectedRun.error_message && (
                <div className="p-3 bg-destructive/8 border border-destructive/25 rounded text-xs text-destructive">
                  {selectedRun.error_message}
                </div>
              )}

              {logsError && (
                <p role="status" className="text-xs text-muted-foreground">{logsError}</p>
              )}

              {/* Terminal */}
              <div
                className="flex-1 bg-zinc-950 rounded border border-zinc-800 p-4 font-mono text-xs overflow-auto min-h-0"
                role="log"
                aria-live="polite"
                aria-label="Run output"
              >
                {logs.length === 0 ? (
                  <span className="text-zinc-600">
                    {selectedRun.status === 'running' ? 'Waiting for output…' : 'No output recorded.'}
                  </span>
                ) : (
                  logs.map((line, i) => (
                    <div
                      key={i}
                      className={cn(
                        'whitespace-pre-wrap leading-relaxed',
                        line.startsWith('✓') ? 'text-emerald-400' :
                        line.startsWith('✗') ? 'text-red-400' :
                        line.startsWith('⊘') ? 'text-zinc-500' :
                        'text-zinc-300'
                      )}
                    >
                      {line}
                    </div>
                  ))
                )}
                <div ref={logsEndRef} />
              </div>
            </div>
          )}
        </SheetContent>
      </Sheet>
    </div>
  )
}
