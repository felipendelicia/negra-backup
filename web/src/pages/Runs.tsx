import { useState, useEffect, useRef } from 'react'
import { useQuery } from '@tanstack/react-query'
import { api } from 'src/lib/api'
import { StatusBadge } from 'src/components/StatusBadge'
import { Button } from 'src/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from 'src/components/ui/card'
import { Sheet, SheetContent, SheetHeader, SheetTitle } from 'src/components/ui/sheet'
import { formatDate, formatBytes, formatRelative } from 'src/lib/utils'
import { FileText } from 'lucide-react'
import { cn } from 'src/lib/utils'
import type { BackupRun } from 'src/lib/types'

function useLiveLogs(run: BackupRun | null) {
  const [logs, setLogs] = useState<string[]>([])
  const [connected, setConnected] = useState(false)
  const wsRef = useRef<WebSocket | null>(null)

  useEffect(() => {
    if (!run) {
      setLogs([])
      setConnected(false)
      return
    }

    setLogs([])

    if (run.status !== 'running') {
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
      }
      setLogs(lines)
      return
    }

    const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
    const url = `${proto}//${window.location.host}/api/runs/${run.id}/logs/ws`

    try {
      const ws = new WebSocket(url)
      wsRef.current = ws
      ws.onopen    = () => setConnected(true)
      ws.onmessage = (e) => setLogs(prev => [...prev, e.data as string])
      ws.onclose   = () => setConnected(false)
      ws.onerror   = () => { setConnected(false); ws.close() }
    } catch {
      setConnected(false)
    }

    return () => { wsRef.current?.close(); wsRef.current = null }
  }, [run?.id, run?.status])

  return { logs, connected }
}

const FILTERS = ['', 'running', 'success', 'failed'] as const

export default function Runs() {
  const [selectedRun, setSelectedRun] = useState<BackupRun | null>(null)
  const [filterStatus, setFilterStatus] = useState<string>('')
  const logsEndRef = useRef<HTMLDivElement>(null)

  const { data: runs = [], isLoading, isError } = useQuery({
    queryKey: ['runs'],
    queryFn: ({ signal }) => api.listRuns(undefined, signal),
    refetchInterval: 5_000,
  })

  const { data: jobs = [] } = useQuery({
    queryKey: ['jobs'],
    queryFn: ({ signal }) => api.listJobs(signal),
  })

  const { logs, connected } = useLiveLogs(selectedRun)

  useEffect(() => {
    logsEndRef.current?.scrollIntoView({ behavior: 'smooth' })
  }, [logs])

  const filtered = filterStatus ? runs.filter(r => r.status === filterStatus) : runs

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <h2 className="font-sans font-bold text-2xl tracking-tight">Run History</h2>

        {/* Filter pills */}
        <div className="flex gap-1">
          {FILTERS.map(s => (
            <button
              key={s}
              onClick={() => setFilterStatus(s)}
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
                  <Button
                    variant="ghost"
                    size="icon"
                    className="h-7 w-7 opacity-0 group-hover:opacity-100 transition-opacity"
                    onClick={() => setSelectedRun(run)}
                    title="View logs"
                  >
                    <FileText className="h-3.5 w-3.5" />
                  </Button>
                </div>
              )
            })}
          </div>
        </CardContent>
      </Card>

      {/* ── Log drawer ──────────────────────────────────────────────── */}
      <Sheet open={selectedRun !== null} onOpenChange={(o) => !o && setSelectedRun(null)}>
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
                    <span className={cn('text-xs', connected ? 'text-emerald-500' : 'text-muted-foreground')}>
                      {connected ? '● live' : '○ connecting…'}
                    </span>
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

              {/* Terminal */}
              <div className="flex-1 bg-zinc-950 rounded border border-zinc-800 p-4 font-mono text-xs overflow-auto min-h-0">
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
