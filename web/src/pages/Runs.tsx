import { useState, useEffect, useMemo, useRef } from 'react'
import { useQuery, useQueries, useMutation, useQueryClient, keepPreviousData } from '@tanstack/react-query'
import { toast } from 'sonner'
import { api } from 'src/lib/api'
import { getToken } from 'src/lib/auth'
import { StatusBadge } from 'src/components/StatusBadge'
import { CopyButton } from 'src/components/CopyButton'
import { RestoreDialog } from 'src/components/RestoreDialog'
import { Button } from 'src/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from 'src/components/ui/card'
import { Sheet, SheetContent, SheetHeader, SheetTitle } from 'src/components/ui/sheet'
import { formatDate, formatBytes, formatRelative } from 'src/lib/utils'
import { summarizeSkips, type SkipSummary } from 'src/lib/skips'
import { FileText, X, ChevronLeft, ChevronRight, AlertTriangle, RotateCcw, Terminal } from 'lucide-react'
import { cn } from 'src/lib/utils'
import type { Agent, BackupJob, BackupRun, RunStatus, StorageDestination } from 'src/lib/types'

/** Rendered scrollback per run. */
const MAX_LOG_LINES = 2000
const BASE_RECONNECT_MS = 1_000
const MAX_RECONNECT_MS = 15_000
const MAX_RECONNECT_ATTEMPTS = 5
const PAGE_SIZE = 50
/** How often the stored log is re-read while a run is still going. */
const LOG_POLL_MS = 5_000

// ── Log model ─────────────────────────────────────────────────────────────────

/** A line ready to render. `at` is the server timestamp, or null for a live line
 *  that has not been persisted (progress ticks never are). */
interface RenderLine {
  at: string | null
  line: string
}

function formatLogTime(iso: string): string {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return ''
  return d.toLocaleTimeString('en-US', { hour12: false, hour: '2-digit', minute: '2-digit', second: '2-digit' })
}

interface LiveState {
  runId: string
  connected: boolean
  error: string
  lines: string[]
}

const emptyLive: LiveState = { runId: '', connected: false, error: '', lines: [] }

/**
 * Streams live output for a running backup.
 *
 * Only terminal events are persisted server-side, so this socket is the only
 * source of progress ticks — but it exists only while the run is active.
 */
function useLiveLogs(runId: string | null, isRunning: boolean) {
  const [live, setLive] = useState<LiveState>(emptyLive)
  const wsRef = useRef<WebSocket | null>(null)

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
  return { lines: active.lines, connected: isRunning && active.connected, error: isRunning ? active.error : '' }
}

/**
 * The log of a run: whatever the server stored, plus the live stream while the
 * run is still going.
 *
 * The stored log is authoritative for a finished run — the UI no longer invents
 * cosmetic summary lines, so a run with nothing stored shows an honest empty.
 */
function useRunLogs(run: BackupRun | null) {
  const runId = run?.id ?? null

  const { data: stored, isLoading, isError } = useQuery({
    queryKey: ['run-logs', runId],
    queryFn: ({ signal }) => {
      if (runId === null) throw new Error('no run selected')
      return api.getRunLogs(runId, signal)
    },
    enabled: runId !== null,
    // The terminal line lands in the table only when the run ends, so keep
    // re-reading while it is active.
    refetchInterval: run?.status === 'running' ? LOG_POLL_MS : false,
  })

  // The logs endpoint reports the run's current status, which stays fresh even
  // when the run's row is no longer on the page being displayed.
  const status: RunStatus | null = stored?.status ?? run?.status ?? null
  const isRunning = status === 'running'

  const live = useLiveLogs(runId, isRunning)

  const logs = useMemo<RenderLine[]>(() => {
    const storedLines = stored?.lines ?? []
    if (live.lines.length === 0) {
      return storedLines.map(l => ({ at: l.logged_at, line: l.line }))
    }

    // A terminal event is both broadcast and persisted, so it can appear in both
    // sources. The live copy wins on ordering (it sits after the progress ticks
    // that were never stored) but borrows the stored timestamp once written.
    const seenLive = new Set(live.lines)
    const storedAt = new Map(storedLines.map(l => [l.line, l.logged_at]))
    return [
      ...storedLines.filter(l => !seenLive.has(l.line)).map(l => ({ at: l.logged_at, line: l.line })),
      ...live.lines.map(line => ({ at: storedAt.get(line) ?? null, line })),
    ]
  }, [stored, live.lines])

  return {
    logs,
    status,
    isRunning,
    isLoading,
    isError,
    connected: live.connected,
    streamError: live.error,
  }
}

// ── Skipped files ─────────────────────────────────────────────────────────────

/**
 * A one-line marker for a run that left files behind.
 *
 * A backup that silently dropped files looks exactly like a complete one in the
 * list, which is the failure mode worth shouting about — so this sits in the row
 * itself, not behind a click. Unreadable entries (permission denied, locked
 * files) read as an error; the routine ones (sockets, FIFOs) as a note.
 */
function SkipBadge({ skips, className }: { skips: SkipSummary; className?: string }) {
  const bad = skips.suspicious > 0
  const label = bad
    ? `${skips.suspicious} unreadable`
    : `${skips.total} skipped`
  return (
    <span
      className={cn(
        'inline-flex items-center gap-1 rounded px-1.5 py-0.5 text-[11px] font-medium whitespace-nowrap shrink-0',
        bad
          ? 'bg-destructive/10 text-destructive ring-1 ring-destructive/25'
          : 'text-amber-600 dark:text-amber-500',
        className,
      )}
      title={
        bad
          ? `${skips.suspicious} of ${skips.total} entries could not be read and are NOT in this backup`
          : `${skips.total} entries were left out (sockets and other unbackupable files)`
      }
    >
      <AlertTriangle aria-hidden="true" className="h-3 w-3 shrink-0" />
      {label}
    </span>
  )
}

/** The detail view of what a run left out. */
function SkipPanel({ skips }: { skips: SkipSummary }) {
  const bad = skips.suspicious > 0
  const samples = [...skips.samples].sort((a, b) => Number(b.suspicious) - Number(a.suspicious))
  return (
    <div
      role={bad ? 'alert' : undefined}
      className={cn(
        'shrink-0 rounded border p-3 space-y-2',
        bad
          ? 'border-destructive/25 bg-destructive/8'
          : 'border-amber-500/25 bg-amber-500/8',
      )}
    >
      <div className="flex items-center gap-2">
        <AlertTriangle
          aria-hidden="true"
          className={cn('h-3.5 w-3.5 shrink-0', bad ? 'text-destructive' : 'text-amber-600 dark:text-amber-500')}
        />
        <span className={cn('text-xs font-semibold', bad ? 'text-destructive' : 'text-amber-700 dark:text-amber-500')}>
          {skips.total} {skips.total === 1 ? 'entry was' : 'entries were'} left out of this backup
          {bad && ` — ${skips.suspicious} could not be read`}
        </span>
      </div>

      <p className="text-[11px] text-muted-foreground leading-relaxed">
        {bad
          ? 'Permission denied, a locked file or an I/O error: these paths are NOT in the archive and will not come back from a restore. Fix the cause and run the job again.'
          : 'Sockets, device nodes and other files that cannot be archived. Nothing recoverable was lost, but they will not appear in a restore either.'}
      </p>

      {samples.length > 0 && (
        <ul className="space-y-0.5 font-mono text-[11px] max-h-40 overflow-y-auto">
          {samples.map((s, i) => (
            <li key={`${s.path}-${i}`} className="flex gap-2">
              <span className={cn('shrink-0', s.suspicious ? 'text-destructive' : 'text-muted-foreground')}>
                {s.suspicious ? '!' : '·'}
              </span>
              <span className="break-all">
                <span className={s.suspicious ? 'text-destructive' : 'text-foreground'}>{s.path}</span>
                {s.reason && <span className="text-muted-foreground"> — {s.reason}</span>}
              </span>
            </li>
          ))}
        </ul>
      )}

      {skips.exact && samples.length < skips.total && (
        <p className="text-[11px] text-muted-foreground">
          Showing {samples.length} of {skips.total}; the agent caps the sample it reports.
        </p>
      )}
    </div>
  )
}

// ── Restore ───────────────────────────────────────────────────────────────────

/** Quotes a value for a POSIX shell. Job names flow into the archive filename
 *  and may contain spaces, so the command has to survive them verbatim. */
function shellQuote(v: string): string {
  return /^[A-Za-z0-9_@%+=:,./-]+$/.test(v) ? v : `'${v.replace(/'/g, `'\\''`)}'`
}

const RESTORE_DEST = '/path/to/restore'

/**
 * The exact command an operator runs to recover a finished backup by hand.
 *
 * The server can now drive a restore itself (`POST /api/runs/{id}/restore`), and
 * that is the path this page offers first. This stays as the fallback for the
 * case the API cannot cover: the agent has to be online for the server to reach
 * it, and the machine that needs its files back is often exactly the one that is
 * down.
 */
function RestorePanel({ run, job, destination, agent }: {
  run: BackupRun
  job: BackupJob | undefined
  destination: StorageDestination | undefined
  agent: Agent | undefined
}) {
  const archive = run.storage_path
  if (!archive) return null

  const bin = agent?.os === 'windows' ? 'nat-backup-agent.exe' : 'nat-backup-agent'
  const remote = destination?.type === 's3' || destination?.type === 'sftp'

  // storage_path is the archive *name* inside the destination, not an absolute
  // path — for a remote destination the operator pulls it through --from-storage.
  const command = remote
    ? `${bin} restore --from-storage ./destination.json --name ${shellQuote(archive)} --dest ${RESTORE_DEST}`
    : `${bin} restore --file ${shellQuote(archive)} --dest ${RESTORE_DEST}`

  return (
    <div className="shrink-0 rounded border border-border bg-muted/30 p-3 space-y-2">
      <div className="flex items-center gap-2">
        <Terminal aria-hidden="true" className="h-3.5 w-3.5 text-muted-foreground" />
        <span className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">
          Restore from the command line
        </span>
        <CopyButton text={command} label="Copy restore command" className="ml-auto" />
      </div>

      <pre className="bg-zinc-950 border border-zinc-800 rounded p-2.5 text-[11px] text-zinc-300 font-mono overflow-x-auto whitespace-pre-wrap break-all leading-relaxed">
        {command}
      </pre>

      <ul className="text-[11px] text-muted-foreground space-y-1 leading-relaxed">
        <li>
          Run it on the agent host{agent?.name ? <> (<span className="font-medium text-foreground">{agent.name}</span>)</> : null}
          {' '}and replace <code className="font-mono">{RESTORE_DEST}</code> with the target directory.
        </li>
        {remote ? (
          <li>
            <code className="font-mono">destination.json</code> holds the{' '}
            <span className="font-medium text-foreground">{destination?.name}</span> config as{' '}
            <code className="font-mono">{'{"type":"'}{destination?.type}{'","config":{…}}'}</code> — the same shape
            the server stores. Credentials are encrypted at rest and are never sent to this page.
          </li>
        ) : (
          <li>
            The archive lives under the{' '}
            {destination?.name ? <span className="font-medium text-foreground">{destination.name}</span> : 'configured'}
            {' '}destination directory on that host; pass the full path to <code className="font-mono">--file</code>.
          </li>
        )}
        <li>Append <code className="font-mono">--dry-run</code> to list the contents without writing anything.</li>
      </ul>

      {job?.encrypt && (
        <p className="flex gap-2 text-[11px] text-amber-600 dark:text-amber-500 leading-relaxed">
          <AlertTriangle aria-hidden="true" className="h-3.5 w-3.5 shrink-0 mt-px" />
          <span>
            This backup is encrypted. Export <code className="font-mono">NAT_BACKUP_PASSPHRASE</code> before running
            (the flag would leak it to the process list) or the agent will prompt for it. Without the job&apos;s
            passphrase the archive cannot be recovered.
          </span>
        </p>
      )}
    </div>
  )
}

// ── Page ──────────────────────────────────────────────────────────────────────

const FILTERS: { value: '' | RunStatus; label: string }[] = [
  { value: '',          label: 'All' },
  { value: 'running',   label: 'Running' },
  { value: 'success',   label: 'Success' },
  { value: 'failed',    label: 'Failed' },
  { value: 'cancelled', label: 'Cancelled' },
]

/** A finished run whose archive still exists is the only thing restorable. */
function isRestorable(run: BackupRun): boolean {
  return run.status === 'success' && !!run.storage_path
}

export default function Runs() {
  const queryClient = useQueryClient()
  const [selected, setSelected] = useState<BackupRun | null>(null)
  const [restoreTarget, setRestoreTarget] = useState<BackupRun | null>(null)
  const [showCliRestore, setShowCliRestore] = useState(false)
  const [filterStatus, setFilterStatus] = useState<'' | RunStatus>('')
  const [offset, setOffset] = useState(0)
  const terminalRef = useRef<HTMLDivElement>(null)

  const cancelMutation = useMutation({
    mutationFn: (runId: string) => api.cancelRun(runId),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['runs'] })
      queryClient.invalidateQueries({ queryKey: ['run-logs'] })
      queryClient.invalidateQueries({ queryKey: ['run-stats'] })
      toast.success('Run cancelled')
      setSelected(null)
    },
    onError: (err: Error) => toast.error(`Failed to cancel: ${err.message}`),
  })

  // The key carries the filters and the page: without them react-query would
  // hand back another page's rows from cache.
  const { data, isLoading, isError, isFetching } = useQuery({
    queryKey: ['runs', { limit: PAGE_SIZE, offset, status: filterStatus }],
    queryFn: ({ signal }) => api.listRuns(
      { limit: PAGE_SIZE, offset, status: filterStatus || undefined },
      signal,
    ),
    refetchInterval: 5_000,
    // Keeps the current page on screen while the next one loads, instead of
    // collapsing the table to a spinner on every click.
    placeholderData: keepPreviousData,
  })

  const runs = data?.runs ?? []
  const total = data?.total ?? 0

  const { data: jobs = [] } = useQuery({
    queryKey: ['jobs'],
    queryFn: ({ signal }) => api.listJobs(signal),
  })
  const { data: agents = [] } = useQuery({
    queryKey: ['agents'],
    queryFn: ({ signal }) => api.listAgents(signal),
  })
  const { data: destinations = [] } = useQuery({
    queryKey: ['storage'],
    queryFn: ({ signal }) => api.listStorage(signal),
  })

  // Which runs left files behind.
  //
  // There is no column for it: the counts live in the run's log
  // (internal/ws/handler.go writes them on job_done), so the only way to mark the
  // list is to read the log of every finished run on the page. It is one request
  // per row, but a finished run's log never changes — `staleTime: Infinity` makes
  // it once per row for the life of the page, and the drawer reuses the very same
  // cache entry under the same key.
  const succeeded = useMemo(() => runs.filter(r => r.status === 'success'), [runs])
  const skipsByRun = useQueries({
    queries: succeeded.map(run => ({
      queryKey: ['run-logs', run.id],
      queryFn: ({ signal }: { signal: AbortSignal }) => api.getRunLogs(run.id, signal),
      staleTime: Infinity,
      gcTime: 15 * 60_000,
      retry: false,
    })),
    combine: (results) => {
      const map = new Map<string, SkipSummary>()
      results.forEach((res, i) => {
        const run = succeeded[i]
        if (!run || !res.data) return
        const summary = summarizeSkips(res.data.lines)
        if (summary) map.set(run.id, summary)
      })
      return map
    },
  })

  // Retention can shrink the history under an open page. Rather than correcting
  // the offset from an effect — which would cascade a second render on every
  // poll — the overshoot is rendered as its own state with a way out.
  const pastEnd = !isLoading && !isError && offset > 0 && runs.length === 0 && total > 0

  function applyFilter(status: '' | RunStatus) {
    setFilterStatus(status)
    setOffset(0) // page N of the old filter is meaningless under the new one
  }

  // Prefer the polled row so an open drawer follows the run's real status, and
  // fall back to the snapshot taken at click time so paging away — or the row
  // ageing off the page — does not blank the drawer.
  const selectedRun = selected ? runs.find(r => r.id === selected.id) ?? selected : null
  const selectedJob = selectedRun ? jobs.find(j => j.id === selectedRun.job_id) : undefined
  const selectedAgent = selectedJob ? agents.find(a => a.id === selectedJob.agent_id) : undefined
  const selectedDestination = selectedJob
    ? destinations.find(d => d.id === selectedJob.storage_destination_id)
    : undefined

  const { logs, status: liveStatus, isRunning, isLoading: logsLoading, isError: logsFailed, connected, streamError } =
    useRunLogs(selectedRun)

  const selectedSkips = useMemo(() => summarizeSkips(logs), [logs])

  // Scrolls the terminal itself rather than calling scrollIntoView, which walks
  // every scrollable ancestor and would drag the drawer body along with it.
  useEffect(() => {
    const el = terminalRef.current
    if (el) el.scrollTop = el.scrollHeight
  }, [logs])

  const rangeFrom = runs.length === 0 ? 0 : offset + 1
  const rangeTo = offset + runs.length
  const hasPrev = offset > 0
  const hasNext = offset + PAGE_SIZE < total

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <h2 className="font-sans font-bold text-2xl tracking-tight">Run History</h2>

        {/* Filter pills — applied server-side, so they filter the whole history
            rather than just the page on screen. */}
        <div className="flex gap-1" role="group" aria-label="Filter runs by status">
          {FILTERS.map(f => (
            <button
              key={f.value}
              type="button"
              onClick={() => applyFilter(f.value)}
              aria-pressed={filterStatus === f.value}
              className={cn(
                'px-3 py-1 rounded text-xs font-medium transition-colors',
                filterStatus === f.value
                  ? 'bg-foreground text-background'
                  : 'text-muted-foreground hover:text-foreground hover:bg-accent'
              )}
            >
              {f.label}
            </button>
          ))}
        </div>
      </div>

      <Card>
        <CardHeader className="pb-2">
          <CardTitle className="text-sm font-semibold uppercase tracking-wider text-muted-foreground">
            Runs
            {total > 0 && (
              <span className="ml-2 text-xs font-normal normal-case tabular-nums">
                ({total.toLocaleString('en-US')})
              </span>
            )}
          </CardTitle>
        </CardHeader>
        <CardContent className="px-0">
          {isLoading && <p className="text-sm text-muted-foreground px-6 py-4">Loading…</p>}
          {isError   && <p className="text-sm text-destructive px-6 py-4">Failed to load data.</p>}
          {pastEnd ? (
            <div className="px-6 py-4 flex items-center gap-3">
              <p className="text-sm text-muted-foreground">
                This page is past the end of the history — {total.toLocaleString('en-US')} run
                {total === 1 ? '' : 's'} remain.
              </p>
              <Button variant="outline" size="sm" className="h-7 px-2 text-xs" onClick={() => setOffset(0)}>
                Go to first page
              </Button>
            </div>
          ) : (!isLoading && !isError && runs.length === 0 && (
            <p className="text-sm text-muted-foreground px-6 py-4">
              {filterStatus ? `No ${filterStatus} runs found.` : 'No runs found.'}
            </p>
          ))}
          <div>
            {runs.map((run, i) => {
              const job = jobs.find(j => j.id === run.job_id)
              const skips = skipsByRun.get(run.id)
              return (
                <div
                  key={run.id}
                  className={cn(
                    'flex items-center gap-4 px-6 py-3 hover:bg-accent/50 transition-colors group',
                    i < runs.length - 1 && 'border-b border-border'
                  )}
                >
                  <StatusBadge status={run.status} className="w-16 shrink-0" />
                  <div className="flex-1 min-w-0">
                    <div className="flex items-center gap-2 min-w-0">
                      <span className="text-sm font-medium truncate">
                        {job?.name ?? <span className="font-mono text-muted-foreground">{run.job_id.slice(0, 8)}</span>}
                      </span>
                      {skips && <SkipBadge skips={skips} />}
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
                  {isRestorable(run) && (
                    <Button
                      variant="ghost"
                      size="icon"
                      className="h-7 w-7 opacity-0 group-hover:opacity-100 focus-visible:opacity-100 transition-opacity"
                      onClick={(e) => { e.stopPropagation(); setRestoreTarget(run) }}
                      title="Restore this backup"
                      aria-label={`Restore backup from run ${run.id.slice(0, 8)}`}
                    >
                      <RotateCcw aria-hidden="true" className="h-3.5 w-3.5" />
                    </Button>
                  )}
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
                    onClick={() => setSelected(run)}
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

        {/* ── Pagination ─────────────────────────────────────────────────── */}
        {total > 0 && (
          <div className="flex items-center justify-between gap-4 border-t border-border px-6 py-3">
            <p className="text-xs text-muted-foreground tabular-nums" aria-live="polite">
              {runs.length === 0
                ? `0 of ${total.toLocaleString('en-US')}`
                : `${rangeFrom.toLocaleString('en-US')}–${rangeTo.toLocaleString('en-US')} of ${total.toLocaleString('en-US')}`}
              {isFetching && <span className="ml-2 opacity-60">updating…</span>}
            </p>
            <nav className="flex items-center gap-1" aria-label="Run history pages">
              <Button
                variant="outline"
                size="sm"
                className="h-7 px-2 text-xs"
                onClick={() => setOffset(o => Math.max(0, o - PAGE_SIZE))}
                disabled={!hasPrev}
                aria-label="Previous page of runs"
              >
                <ChevronLeft aria-hidden="true" className="h-3.5 w-3.5 mr-1" />
                Previous
              </Button>
              <Button
                variant="outline"
                size="sm"
                className="h-7 px-2 text-xs"
                onClick={() => setOffset(o => o + PAGE_SIZE)}
                disabled={!hasNext}
                aria-label="Next page of runs"
              >
                Next
                <ChevronRight aria-hidden="true" className="h-3.5 w-3.5 ml-1" />
              </Button>
            </nav>
          </div>
        )}
      </Card>

      {/* ── Log drawer ──────────────────────────────────────────────── */}
      <Sheet open={selectedRun !== null} onOpenChange={(o) => !o && setSelected(null)}>
        <SheetContent side="right" className="w-full sm:max-w-2xl flex flex-col gap-0 p-0">
          <SheetHeader className="px-5 py-4 border-b">
            <SheetTitle className="flex items-center gap-3 text-sm">
              <span>Run Logs</span>
              {selectedRun && (
                <>
                  <span className="font-mono text-xs text-muted-foreground bg-muted px-1.5 py-0.5 rounded">
                    {selectedRun.id.slice(0, 8)}
                  </span>
                  <StatusBadge status={liveStatus ?? selectedRun.status} />
                  {isRunning && (
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
            <div className="flex-1 overflow-y-auto flex flex-col p-5 gap-3">
              {/* Meta row */}
              <div className="flex items-center gap-4 text-xs text-muted-foreground shrink-0">
                <span>{formatDate(selectedRun.started_at)}</span>
                {selectedRun.size_bytes != null && <span>{formatBytes(selectedRun.size_bytes)}</span>}
                {selectedRun.file_count != null && <span>{selectedRun.file_count} files</span>}
              </div>

              {selectedRun.error_message && (
                <div className="p-3 bg-destructive/8 border border-destructive/25 rounded text-xs text-destructive shrink-0">
                  {selectedRun.error_message}
                </div>
              )}

              {selectedSkips && <SkipPanel skips={selectedSkips} />}

              {!isRunning && (
                <div className="shrink-0 space-y-2">
                  {isRestorable(selectedRun) && (
                    <div className="flex flex-wrap items-center gap-2">
                      <Button
                        size="sm"
                        className="gap-1.5"
                        onClick={() => setRestoreTarget(selectedRun)}
                      >
                        <RotateCcw aria-hidden="true" className="h-3.5 w-3.5" />
                        Restore this backup…
                      </Button>
                      <span className="text-[11px] text-muted-foreground">
                        Runs on {selectedAgent?.name ?? 'the agent host'}, which must be online.
                      </span>
                    </div>
                  )}

                  <button
                    type="button"
                    onClick={() => setShowCliRestore(v => !v)}
                    aria-expanded={showCliRestore}
                    className="flex items-center gap-1.5 text-[11px] text-muted-foreground hover:text-foreground transition-colors"
                  >
                    <Terminal aria-hidden="true" className="h-3 w-3" />
                    {showCliRestore ? 'Hide' : 'Show'} the command-line restore
                    {isRestorable(selectedRun) && ' (works with the agent offline)'}
                  </button>

                  {showCliRestore && (
                    <RestorePanel
                      run={selectedRun}
                      job={selectedJob}
                      destination={selectedDestination}
                      agent={selectedAgent}
                    />
                  )}
                </div>
              )}

              {streamError && (
                <p role="status" className="text-xs text-muted-foreground shrink-0">{streamError}</p>
              )}
              {logsFailed && (
                <p role="status" className="text-xs text-destructive shrink-0">Failed to load the stored log.</p>
              )}

              {/* Terminal */}
              <div
                ref={terminalRef}
                className="flex-1 bg-zinc-950 rounded border border-zinc-800 p-4 font-mono text-xs overflow-auto min-h-40"
                role="log"
                aria-live="polite"
                aria-label="Run output"
              >
                {logs.length === 0 ? (
                  <span className="text-zinc-600">
                    {logsLoading ? 'Loading log…'
                      : isRunning ? 'Waiting for output…'
                      : 'No log lines were stored for this run.'}
                  </span>
                ) : (
                  logs.map((entry, i) => (
                    <div key={i} className="flex gap-2 leading-relaxed">
                      <span
                        className="text-zinc-600 shrink-0 tabular-nums select-none"
                        title={entry.at ? new Date(entry.at).toLocaleString() : 'Live output — not stored'}
                      >
                        {entry.at ? formatLogTime(entry.at) : '   ·    '}
                      </span>
                      <span
                        className={cn(
                          'whitespace-pre-wrap break-all',
                          entry.line.startsWith('completed:') ? 'text-emerald-400' :
                          entry.line.startsWith('failed:')    ? 'text-red-400' :
                          'text-zinc-300'
                        )}
                      >
                        {entry.line}
                      </span>
                    </div>
                  ))
                )}
              </div>

              {logs.length > 0 && (
                <p className="text-[11px] text-muted-foreground shrink-0">
                  Timestamped lines are stored server-side; lines marked <span className="font-mono">·</span> are live
                  progress and are not kept.
                </p>
              )}
            </div>
          )}
        </SheetContent>
      </Sheet>

      {/* Keyed on the run so every restore starts from a clean form rather than
          inheriting the previous run's selection and destination. */}
      {restoreTarget && (
        <RestoreDialog
          key={restoreTarget.id}
          run={restoreTarget}
          job={jobs.find(j => j.id === restoreTarget.job_id)}
          agent={(() => {
            const j = jobs.find(x => x.id === restoreTarget.job_id)
            return j ? agents.find(a => a.id === j.agent_id) : undefined
          })()}
          onClose={() => setRestoreTarget(null)}
        />
      )}
    </div>
  )
}
