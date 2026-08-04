import { useEffect, useMemo, useRef, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { api } from 'src/lib/api'
import { useRestoreProgress } from 'src/lib/use-restore-progress'
import { isAbsolutePath } from 'src/lib/paths'
import { Button } from 'src/components/ui/button'
import { ConfirmDialog } from 'src/components/ConfirmDialog'
import {
  Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle,
} from 'src/components/ui/dialog'
import { Input } from 'src/components/ui/input'
import { Label } from 'src/components/ui/label'
import { cn, formatBytes } from 'src/lib/utils'
import {
  AlertTriangle, CheckCircle2, File, Folder, Loader2, RotateCw, Search, XCircle,
} from 'lucide-react'
import type { Agent, ArchiveEntry, BackupJob, BackupRun } from 'src/lib/types'

/** Rows drawn at once. A backup of a home directory lists tens of thousands of
 *  entries and the browser will not survive drawing them; the filter is the way
 *  through, and saying so beats a frozen tab. */
const MAX_VISIBLE = 400

/** `path.Clean`-ish: what the agent compares `include` patterns against. */
function normalize(name: string): string {
  return name.replace(/\/+$/, '')
}

/**
 * True when some selected ancestor already covers this entry.
 *
 * The agent restores everything under a selected directory
 * (`backup.matchesInclude`), so listing the children too would only make the
 * request bigger.
 */
function coveredByAncestor(name: string, selected: ReadonlySet<string>): boolean {
  let i = name.lastIndexOf('/')
  while (i > 0) {
    const parent = name.slice(0, i)
    if (selected.has(parent)) return true
    i = parent.lastIndexOf('/')
  }
  return false
}

/**
 * Time since this element mounted.
 *
 * It is rendered only while the listing is in flight, so mounting *is* the start
 * of the request — no effect has to observe the query to know when to start
 * counting.
 */
function ElapsedNotice() {
  const [since] = useState(() => Date.now())
  const [now, setNow] = useState(since)
  useEffect(() => {
    const t = window.setInterval(() => setNow(Date.now()), 1_000)
    return () => window.clearInterval(t)
  }, [])
  const secs = Math.max(0, Math.floor((now - since) / 1000))
  const mins = Math.floor(secs / 60)
  return (
    <span className="tabular-nums">
      {mins > 0 ? `${mins}m ${secs % 60}s` : `${secs}s`}
    </span>
  )
}

export function RestoreDialog({
  run,
  job,
  agent,
  onClose,
}: {
  run: BackupRun
  job: BackupJob | undefined
  agent: Agent | undefined
  onClose: () => void
}) {
  const queryClient = useQueryClient()
  const destRef = useRef<HTMLInputElement>(null)

  const [mode, setMode] = useState<'all' | 'select'>('all')
  const [selected, setSelected] = useState<ReadonlySet<string>>(() => new Set<string>())
  const [filter, setFilter] = useState('')
  const [destDir, setDestDir] = useState('')
  const [confirmOpen, setConfirmOpen] = useState(false)
  const [dispatched, setDispatched] = useState(false)

  const agentName = agent?.name ?? 'the agent host'

  // Listing means the agent downloads and decrypts the whole archive, so it is
  // never done just because the dialog opened — only when the operator asks to
  // pick entries out of it.
  const listing = useQuery({
    queryKey: ['run-archive', run.id],
    queryFn: ({ signal }) => api.listRunArchive(run.id, signal),
    enabled: mode === 'select',
    // A stored archive never changes, so one listing per run is enough even
    // across dialog opens.
    staleTime: Infinity,
    gcTime: 15 * 60_000,
    retry: false,
  })

  const entries: ArchiveEntry[] = useMemo(() => {
    const list = listing.data?.entries ?? []
    return [...list].sort((a, b) => a.name.localeCompare(b.name))
  }, [listing.data])

  const filtered = useMemo(() => {
    const q = filter.trim().toLowerCase()
    if (!q) return entries
    return entries.filter(e => e.name.toLowerCase().includes(q))
  }, [entries, filter])

  const visible = filtered.slice(0, MAX_VISIBLE)

  /** Only the entries that are not already covered by a selected directory. */
  const include = useMemo(
    () => [...selected].filter(n => !coveredByAncestor(n, selected)).sort(),
    [selected],
  )

  const progress = useRestoreProgress(run.id, dispatched)

  const restoreMut = useMutation({
    mutationFn: () => api.startRestore(run.id, {
      dest_dir: destDir.trim(),
      include: mode === 'select' ? include : [],
    }),
    onSuccess: () => {
      setConfirmOpen(false)
      setDispatched(true)
      // A restore writes files on the agent; it does not touch the run row, so
      // ['runs'] is deliberately left alone rather than churning a list that
      // polls every five seconds anyway. The run's log is the one thing the
      // server could add to.
      queryClient.invalidateQueries({ queryKey: ['run-logs', run.id] })
      toast.success(`Restore dispatched to ${agentName}`)
    },
    onError: (e: Error) => {
      setConfirmOpen(false)
      toast.error(`Restore could not start: ${e.message}`)
    },
  })

  function toggle(name: string) {
    setSelected(prev => {
      const next = new Set(prev)
      if (next.has(name)) next.delete(name)
      else next.add(normalize(name))
      return next
    })
  }

  const destValid = isAbsolutePath(destDir, agent?.os)
  const destTouched = destDir.trim().length > 0
  const canRestore =
    destValid && (mode === 'all' || include.length > 0) && !restoreMut.isPending

  const scopeLabel = mode === 'all'
    ? 'the entire archive'
    : `${include.length} selected ${include.length === 1 ? 'entry' : 'entries'}`

  return (
    <>
      <Dialog open onOpenChange={(o) => { if (!o) onClose() }}>
        <DialogContent
          className="max-w-2xl max-h-[90vh] flex flex-col gap-4 overflow-hidden"
          initialFocus={destRef}
        >
          <DialogHeader>
            <DialogTitle>Restore backup</DialogTitle>
            <DialogDescription>
              {job?.name ? <span className="font-medium text-foreground">{job.name}</span> : 'This backup'}
              {' · '}
              <span className="font-mono">{run.storage_path ?? run.id.slice(0, 8)}</span>
              {run.size_bytes != null && <> · {formatBytes(run.size_bytes)}</>}
            </DialogDescription>
          </DialogHeader>

          {dispatched ? (
            <RestoreProgressPanel
              progress={progress}
              destDir={destDir.trim()}
              agentName={agentName}
              scopeLabel={scopeLabel}
            />
          ) : (
            <div className="flex-1 min-h-0 overflow-y-auto space-y-4 pr-1">
              {/* ── What to restore ─────────────────────────────────────── */}
              <fieldset className="space-y-2">
                <legend className="text-xs font-semibold uppercase tracking-wider text-muted-foreground mb-2">
                  What to restore
                </legend>
                <label className="flex items-start gap-2.5 cursor-pointer">
                  <input
                    type="radio"
                    name="restore-scope"
                    className="mt-0.5 accent-foreground"
                    checked={mode === 'all'}
                    onChange={() => setMode('all')}
                  />
                  <span className="text-sm leading-tight">
                    Everything in this backup
                    <span className="block text-xs text-muted-foreground">
                      Unpacks the whole archive. Nothing is downloaded to list it first.
                    </span>
                  </span>
                </label>
                <label className="flex items-start gap-2.5 cursor-pointer">
                  <input
                    type="radio"
                    name="restore-scope"
                    className="mt-0.5 accent-foreground"
                    checked={mode === 'select'}
                    onChange={() => setMode('select')}
                  />
                  <span className="text-sm leading-tight">
                    Selected files and folders
                    <span className="block text-xs text-muted-foreground">
                      Lists the archive first — {agentName} has to download and decrypt the whole
                      file to read it, which can take minutes.
                    </span>
                  </span>
                </label>
              </fieldset>

              {/* ── Archive listing ─────────────────────────────────────── */}
              {mode === 'select' && (
                <div className="space-y-2">
                  {listing.isFetching && (
                    <div
                      role="status"
                      className="flex items-center gap-2.5 rounded border border-border bg-muted/30 px-3 py-2.5 text-xs"
                    >
                      <Loader2 aria-hidden="true" className="h-3.5 w-3.5 animate-spin shrink-0" />
                      <span className="text-muted-foreground">
                        Reading the archive on {agentName} — downloading and decrypting it in full.
                        {' '}Elapsed <ElapsedNotice />. The server gives up after 5 minutes.
                      </span>
                    </div>
                  )}

                  {listing.isError && !listing.isFetching && (
                    <div
                      role="alert"
                      className="flex items-start gap-2.5 rounded border border-destructive/25 bg-destructive/8 px-3 py-2.5 text-xs text-destructive"
                    >
                      <AlertTriangle aria-hidden="true" className="h-3.5 w-3.5 shrink-0 mt-px" />
                      <div className="space-y-1.5">
                        <p>{(listing.error as Error).message}</p>
                        <p className="text-muted-foreground">
                          The archive can still be restored whole, or recovered from the agent&apos;s
                          command line — see the restore command on the run.
                        </p>
                        <Button
                          type="button"
                          size="sm"
                          variant="outline"
                          className="h-7 gap-1.5 text-xs"
                          onClick={() => void listing.refetch()}
                        >
                          <RotateCw aria-hidden="true" className="h-3 w-3" />
                          Try again
                        </Button>
                      </div>
                    </div>
                  )}

                  {listing.isSuccess && (
                    <>
                      <div className="flex items-center gap-2">
                        <div className="relative flex-1">
                          <Search
                            aria-hidden="true"
                            className="absolute left-2 top-1/2 -translate-y-1/2 h-3.5 w-3.5 text-muted-foreground"
                          />
                          <Input
                            className="pl-7"
                            placeholder="Filter by path…"
                            aria-label="Filter archive entries"
                            value={filter}
                            onChange={e => setFilter(e.target.value)}
                          />
                        </div>
                        <Button
                          type="button"
                          size="sm"
                          variant="outline"
                          className="h-8 text-xs shrink-0"
                          onClick={() => setSelected(new Set(visible.map(e => normalize(e.name))))}
                          disabled={visible.length === 0}
                        >
                          Select shown
                        </Button>
                        <Button
                          type="button"
                          size="sm"
                          variant="ghost"
                          className="h-8 text-xs shrink-0"
                          onClick={() => setSelected(new Set())}
                          disabled={selected.size === 0}
                        >
                          Clear
                        </Button>
                      </div>

                      <div className="border border-border rounded max-h-64 overflow-y-auto">
                        {filtered.length === 0 ? (
                          <p className="text-xs text-muted-foreground px-3 py-3">
                            {entries.length === 0
                              ? 'This archive is empty.'
                              : 'No entry matches that filter.'}
                          </p>
                        ) : (
                          visible.map((entry, i) => {
                            const name = normalize(entry.name)
                            const checked = selected.has(name)
                            const implied = !checked && coveredByAncestor(name, selected)
                            // Indexed, not derived from the path: archive entries
                            // contain spaces and quotes, which are not valid in an
                            // id and would break the label association.
                            const id = `archive-entry-${i}`
                            return (
                              <div
                                key={entry.name}
                                className={cn(
                                  'flex items-center gap-2 px-3 py-1 hover:bg-accent/50 transition-colors',
                                  (checked || implied) && 'bg-accent/30',
                                )}
                              >
                                <input
                                  id={id}
                                  type="checkbox"
                                  className="accent-foreground shrink-0"
                                  checked={checked || implied}
                                  disabled={implied}
                                  onChange={() => toggle(name)}
                                />
                                {entry.is_dir
                                  ? <Folder aria-hidden="true" className="h-3.5 w-3.5 text-muted-foreground shrink-0" />
                                  : <File aria-hidden="true" className="h-3.5 w-3.5 text-muted-foreground/50 shrink-0" />}
                                <label
                                  htmlFor={id}
                                  className="flex-1 min-w-0 font-mono text-xs truncate cursor-pointer"
                                  title={implied ? `${entry.name} — already covered by a selected folder` : entry.name}
                                >
                                  {entry.name}{entry.is_dir && '/'}
                                </label>
                                <span className="text-[11px] text-muted-foreground tabular-nums shrink-0">
                                  {entry.is_dir ? '—' : formatBytes(entry.size)}
                                </span>
                              </div>
                            )
                          })
                        )}
                      </div>

                      <p className="text-[11px] text-muted-foreground">
                        {filtered.length > MAX_VISIBLE
                          ? `Showing ${MAX_VISIBLE} of ${filtered.length.toLocaleString('en-US')} matching entries — narrow the filter to reach the rest. `
                          : `${filtered.length.toLocaleString('en-US')} of ${entries.length.toLocaleString('en-US')} entries. `}
                        Selecting a folder restores everything under it.
                      </p>
                    </>
                  )}
                </div>
              )}

              {/* ── Destination ─────────────────────────────────────────── */}
              <div className="space-y-1.5">
                <Label htmlFor="restore-dest">Destination directory</Label>
                <Input
                  id="restore-dest"
                  ref={destRef}
                  value={destDir}
                  onChange={e => setDestDir(e.target.value)}
                  placeholder={agent?.os === 'windows' ? 'C:\\restore\\2026-08-04' : '/var/restore/2026-08-04'}
                  aria-invalid={destTouched && !destValid}
                  aria-describedby="restore-dest-help"
                  className="font-mono"
                />
                <p id="restore-dest-help" className="text-[11px] text-muted-foreground leading-relaxed">
                  This is a path on <span className="font-medium text-foreground">{agentName}</span> — the
                  machine running the agent — not on this computer. It must be absolute, and it is created
                  if it does not exist.
                </p>
                {destTouched && !destValid && (
                  <p role="alert" className="text-[11px] text-destructive">
                    {agent?.os === 'windows'
                      ? 'Enter an absolute Windows path, for example C:\\restore.'
                      : 'Enter an absolute path starting with /. The agent rejects relative paths.'}
                  </p>
                )}
              </div>

              <p className="flex gap-2 text-[11px] text-amber-600 dark:text-amber-500 leading-relaxed">
                <AlertTriangle aria-hidden="true" className="h-3.5 w-3.5 shrink-0 mt-px" />
                <span>
                  Files already in the destination are overwritten when the archive contains the same
                  path. Restoring into a fresh directory is the safe option.
                </span>
              </p>
            </div>
          )}

          <DialogFooter>
            {dispatched ? (
              <Button type="button" onClick={onClose}>
                {progress.outcome === 'pending' ? 'Close (keeps running)' : 'Done'}
              </Button>
            ) : (
              <>
                <Button type="button" variant="outline" onClick={onClose}>Cancel</Button>
                <Button
                  type="button"
                  onClick={() => setConfirmOpen(true)}
                  disabled={!canRestore}
                  title={
                    !destValid ? 'Enter an absolute destination directory on the agent host'
                      : mode === 'select' && include.length === 0 ? 'Select at least one entry'
                      : undefined
                  }
                >
                  Restore…
                </Button>
              </>
            )}
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <ConfirmDialog
        open={confirmOpen}
        title="Restore over existing files?"
        description={
          `Restore ${scopeLabel} into ${destDir.trim()} on ${agentName}. ` +
          'Any file already there with a path the archive also contains is overwritten, ' +
          'and that cannot be undone.'
        }
        confirmLabel="Restore"
        loadingLabel="Starting…"
        loading={restoreMut.isPending}
        onConfirm={() => restoreMut.mutate()}
        onCancel={() => setConfirmOpen(false)}
      />
    </>
  )
}

/** Live output of a dispatched restore. */
function RestoreProgressPanel({
  progress,
  destDir,
  agentName,
  scopeLabel,
}: {
  progress: ReturnType<typeof useRestoreProgress>
  destDir: string
  agentName: string
  scopeLabel: string
}) {
  // Scrolls the terminal itself rather than calling scrollIntoView, which walks
  // every scrollable ancestor and would drag the dialog body along with it.
  const terminalRef = useRef<HTMLDivElement>(null)
  useEffect(() => {
    const el = terminalRef.current
    if (el) el.scrollTop = el.scrollHeight
  }, [progress.lines])

  return (
    <div className="flex-1 min-h-0 flex flex-col gap-3">
      <div className="flex items-center gap-2 text-sm">
        {progress.outcome === 'done' ? (
          <>
            <CheckCircle2 aria-hidden="true" className="h-4 w-4 text-emerald-500 shrink-0" />
            <span className="font-medium text-emerald-600 dark:text-emerald-400">Restore finished</span>
          </>
        ) : progress.outcome === 'failed' ? (
          <>
            <XCircle aria-hidden="true" className="h-4 w-4 text-destructive shrink-0" />
            <span className="font-medium text-destructive">Restore failed</span>
          </>
        ) : (
          <>
            <Loader2 aria-hidden="true" className="h-4 w-4 animate-spin shrink-0" />
            <span className="font-medium">Restoring…</span>
          </>
        )}
        <span className="text-xs text-muted-foreground truncate">
          {scopeLabel} → <span className="font-mono">{destDir}</span> on {agentName}
        </span>
      </div>

      {progress.outcome === 'failed' && progress.outcomeDetail && (
        // The reason, out of the scrollback: on a long restore the line that says
        // why it stopped is the one thing nobody should have to scroll for.
        <p role="alert" className="rounded border border-destructive/25 bg-destructive/8 px-3 py-2 text-xs text-destructive break-words">
          {progress.outcomeDetail}
        </p>
      )}

      {progress.status && (
        <p role="status" className="text-xs text-muted-foreground">{progress.status}</p>
      )}

      <div
        ref={terminalRef}
        className="flex-1 min-h-32 bg-zinc-950 border border-zinc-800 rounded p-3 font-mono text-[11px] overflow-auto"
        role="log"
        aria-live="polite"
        aria-label="Restore output"
      >
        {progress.lines.length === 0 ? (
          <span className="text-zinc-600">
            {progress.watching
              ? 'Waiting for the agent to report…'
              : 'Connecting to the progress stream…'}
          </span>
        ) : (
          progress.lines.map(l => (
            <div
              key={l.id}
              className={cn(
                'whitespace-pre-wrap break-all leading-relaxed',
                /failed/i.test(l.text) ? 'text-red-400'
                  : /completed/i.test(l.text) ? 'text-emerald-400'
                  : 'text-zinc-300',
              )}
            >
              {l.text}
            </div>
          ))
        )}
      </div>

      <p className="text-[11px] text-muted-foreground leading-relaxed">
        Progress comes from this run&apos;s log stream, plus the server log for the agent&apos;s own
        warnings. Closing this dialog stops watching; it does not stop the restore.
      </p>
    </div>
  )
}
