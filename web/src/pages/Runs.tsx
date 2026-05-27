import { useState, useEffect, useRef } from 'react'
import { useQuery } from '@tanstack/react-query'
import { api } from 'src/lib/api'
import { StatusBadge } from 'src/components/StatusBadge'
import { Button } from 'src/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from 'src/components/ui/card'
import { Sheet, SheetContent, SheetHeader, SheetTitle } from 'src/components/ui/sheet'
import { formatDate, formatBytes, formatRelative } from 'src/lib/utils'
import { FileText } from 'lucide-react'
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
      // No live logs for completed runs
      return
    }

    const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
    const url = `${proto}//${window.location.host}/api/runs/${run.id}/logs/ws`

    try {
      const ws = new WebSocket(url)
      wsRef.current = ws

      ws.onopen = () => setConnected(true)
      ws.onmessage = (e) => setLogs(prev => [...prev, e.data as string])
      ws.onclose = () => setConnected(false)
      ws.onerror = () => {
        setConnected(false)
        ws.close()
      }
    } catch {
      setConnected(false)
    }

    return () => {
      wsRef.current?.close()
      wsRef.current = null
    }
  }, [run?.id, run?.status])

  return { logs, connected }
}

export default function Runs() {
  const [selectedRun, setSelectedRun] = useState<BackupRun | null>(null)
  const [filterStatus, setFilterStatus] = useState<string>('')
  const logsEndRef = useRef<HTMLDivElement>(null)

  const { data: runs = [], isLoading, isError } = useQuery({
    queryKey: ['runs'],
    queryFn: ({ signal }) => api.listRuns(undefined, signal),
    refetchInterval: 5_000,
  })

  const { data: jobs = [] } = useQuery({ queryKey: ['jobs'], queryFn: ({ signal }) => api.listJobs(signal) })

  const { logs, connected } = useLiveLogs(selectedRun)

  useEffect(() => {
    logsEndRef.current?.scrollIntoView({ behavior: 'auto' })
  }, [logs])

  const filtered = filterStatus ? runs.filter(r => r.status === filterStatus) : runs

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <h2 className="text-2xl font-bold">Run History</h2>
        <div className="flex gap-2">
          {['', 'running', 'success', 'failed'].map(s => (
            <Button
              key={s}
              variant={filterStatus === s ? 'default' : 'outline'}
              size="sm"
              onClick={() => setFilterStatus(s)}
            >
              {s || 'All'}
            </Button>
          ))}
        </div>
      </div>

      <Card>
        <CardHeader><CardTitle>Runs</CardTitle></CardHeader>
        <CardContent>
          {isLoading && <p className="text-sm text-muted-foreground">Loading...</p>}
          {isError && <p className="text-sm text-destructive">Failed to load data. Please refresh.</p>}
          {!isLoading && filtered.length === 0 && (
            <p className="text-sm text-muted-foreground">No runs found.</p>
          )}
          <div className="space-y-2">
            {filtered.map(run => {
              const job = jobs.find(j => j.id === run.job_id)
              return (
                <div key={run.id} className="flex items-center gap-4 p-3 border rounded-lg">
                  <StatusBadge status={run.status} />
                  <div className="flex-1 min-w-0">
                    <div className="text-sm font-medium truncate">{job?.name ?? run.job_id.slice(0, 8)}</div>
                    <div className="text-xs text-muted-foreground">
                      Started {formatRelative(run.started_at)} · {formatBytes(run.size_bytes)}
                      {run.file_count != null ? ` · ${run.file_count} files` : ''}
                    </div>
                    {run.error_message && (
                      <div className="text-xs text-destructive truncate">{run.error_message}</div>
                    )}
                  </div>
                  <div className="text-xs text-muted-foreground shrink-0">
                    {run.finished_at ? formatDate(run.finished_at) : '—'}
                  </div>
                  <Button
                    variant="ghost"
                    size="icon-sm"
                    onClick={() => setSelectedRun(run)}
                    title="View logs"
                  >
                    <FileText className="h-4 w-4" />
                  </Button>
                </div>
              )
            })}
          </div>
        </CardContent>
      </Card>

      <Sheet open={selectedRun !== null} onOpenChange={(o) => !o && setSelectedRun(null)}>
        <SheetContent side="right" className="w-full sm:max-w-2xl flex flex-col">
          <SheetHeader>
            <SheetTitle>
              Run Logs
              {selectedRun && (
                <span className="ml-2 text-xs text-muted-foreground font-mono">
                  {selectedRun.id.slice(0, 8)}
                </span>
              )}
            </SheetTitle>
          </SheetHeader>
          {selectedRun && (
            <div className="flex-1 overflow-auto">
              <div className="flex items-center gap-3 mb-3 text-xs text-muted-foreground">
                <StatusBadge status={selectedRun.status} />
                <span>{formatDate(selectedRun.started_at)}</span>
                {selectedRun.status === 'running' && (
                  <span className={connected ? 'text-green-600' : 'text-muted-foreground'}>
                    {connected ? '● Live' : '○ Connecting...'}
                  </span>
                )}
              </div>
              {selectedRun.error_message && (
                <div className="mb-3 p-3 bg-destructive/10 rounded text-sm text-destructive">
                  {selectedRun.error_message}
                </div>
              )}
              <div className="bg-muted rounded p-3 font-mono text-xs h-96 overflow-auto">
                {logs.length === 0 ? (
                  <span className="text-muted-foreground">
                    {selectedRun.status === 'running' ? 'Waiting for logs...' : 'No logs available.'}
                  </span>
                ) : (
                  logs.map((line, i) => (
                    <div key={i} className="whitespace-pre-wrap">{line}</div>
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
