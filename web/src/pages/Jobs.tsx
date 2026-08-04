import { useEffect, useMemo, useRef, useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { api } from 'src/lib/api'
import { ConfirmDialog } from 'src/components/ConfirmDialog'
import { HealthBadge } from 'src/components/HealthBadge'
import { Button } from 'src/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from 'src/components/ui/card'
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogFooter } from 'src/components/ui/dialog'
import { Input } from 'src/components/ui/input'
import { Label } from 'src/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from 'src/components/ui/select'
import { Switch } from 'src/components/ui/switch'
import { Textarea } from 'src/components/ui/textarea'
import { cronHumanize, formatDate, formatRelative } from 'src/lib/utils'
import { healthLabel, isHealthProblem } from 'src/lib/health'
import { Plus, Trash2, Play, Pencil, WifiOff } from 'lucide-react'
import { cn } from 'src/lib/utils'
import { FileBrowser } from 'src/components/FileBrowser'
import type { BackupJob, CreateJobRequest, JobHealth } from 'src/lib/types'

const JOB_TYPES = ['files', 'postgres', 'mysql', 'sqlite', 'mongodb'] as const
const COMPRESSIONS = ['zstd', 'gzip'] as const
const CRON_PRESETS = [
  { label: 'Every day at 2 AM', value: '0 2 * * *' },
  { label: 'Every day at 3 AM', value: '0 3 * * *' },
  { label: 'Every day at midnight', value: '0 0 * * *' },
  { label: 'Every hour', value: '@hourly' },
  { label: 'Every week', value: '@weekly' },
  { label: 'Custom', value: '__custom__' },
]

const emptyForm = (): CreateJobRequest => ({
  agent_id: '',
  name: '',
  enabled: true,
  type: 'files',
  source: {},
  storage_destination_id: '',
  schedule_cron: '0 2 * * *',
  retention_days: 30,
  compression: 'zstd',
  encrypt: false,
  encrypt_passphrase: '',
})

/** Stable identity for "no patterns", so ExcludeField's sync effect can tell an
 *  externally-changed list from a re-render of the same empty one. */
const NO_EXCLUDES: string[] = []

const EXCLUDE_PLACEHOLDER = ['node_modules', '*.log', '.cache/', '/var/www/*/dist'].join('\n')

/**
 * The exclusion patterns of a `files` job, one per line — the shape a sysadmin
 * already writes them in.
 *
 * The textarea keeps its own text because the stored value is a `string[]`:
 * round-tripping through `split('\n').filter(Boolean)` on every keystroke would
 * eat the newline the moment it is typed.
 */
function ExcludeField({ value, onChange }: { value: string[]; onChange: (patterns: string[]) => void }) {
  const [text, setText] = useState(() => value.join('\n'))
  const pushed = useRef(value)

  // Adopt a list set from outside (the dialog reopening on another job) without
  // fighting the typing: anything we pushed up ourselves comes back identical.
  useEffect(() => {
    if (value !== pushed.current) {
      pushed.current = value
      setText(value.join('\n'))
    }
  }, [value])

  function handle(next: string) {
    setText(next)
    // Blank lines are dropped here and also ignored by the agent, so a stray
    // newline can never fail a nightly backup.
    const parsed = next.split('\n').map(s => s.trim()).filter(Boolean)
    pushed.current = parsed
    onChange(parsed)
  }

  return (
    <div className="space-y-1">
      <Label htmlFor="job-exclude">
        Exclude
        <span className="ml-1.5 text-xs text-muted-foreground font-normal">(one pattern per line, optional)</span>
      </Label>
      <Textarea
        id="job-exclude"
        rows={4}
        className="font-mono text-xs"
        placeholder={EXCLUDE_PLACEHOLDER}
        value={text}
        onChange={e => handle(e.target.value)}
        aria-describedby="job-exclude-help"
      />
      <div id="job-exclude-help" className="text-[11px] text-muted-foreground leading-relaxed space-y-1">
        <p>
          A subset of <code className="font-mono">.gitignore</code> syntax.{' '}
          <span className="font-medium text-foreground">Negations (<code className="font-mono">!keep.log</code>) are
          not supported</span> — an excluded directory is never even read, which is the point on trees like{' '}
          <code className="font-mono">node_modules</code>.
        </p>
        <ul className="space-y-0.5">
          <li><code className="font-mono text-foreground">node_modules</code> — any entry with that name, at any depth, and everything under it</li>
          <li><code className="font-mono text-foreground">*.log</code> — matches on the name only, at any depth</li>
          <li><code className="font-mono text-foreground">.cache/</code> — the trailing slash restricts it to directories</li>
          <li><code className="font-mono text-foreground">www/*/dist</code> — a path fragment: matches that sequence of segments anywhere</li>
          <li><code className="font-mono text-foreground">/var/www/*/dist</code> — leading slash: anchored to that absolute path</li>
          <li><code className="font-mono text-foreground">**/dist</code> — <code className="font-mono">**</code> spans any number of segments</li>
        </ul>
      </div>
    </div>
  )
}

function SourceFields({
  type,
  source,
  agentId,
  agentOnline,
  onChange,
}: {
  type: BackupJob['type']
  source: Record<string, unknown>
  agentId: string
  agentOnline: boolean
  onChange: (s: Record<string, unknown>) => void
}) {
  function field(key: string, label: string, placeholder?: string) {
    return (
      <div className="space-y-1">
        <Label>{label}</Label>
        <Input
          value={(source[key] as string) ?? ''}
          placeholder={placeholder}
          onChange={e => onChange({ ...source, [key]: e.target.value })}
        />
      </div>
    )
  }

  if (type === 'files') {
    const paths = Array.isArray(source['paths']) ? (source['paths'] as string[]) : []
    const exclude = Array.isArray(source['exclude']) ? (source['exclude'] as string[]) : NO_EXCLUDES

    // The exclusion patterns live inside `source` next to `paths`, so every
    // writer here has to spread the existing source rather than replace it —
    // dropping the object wholesale used to wipe them on any path edit.
    const excludeField = (
      <ExcludeField
        value={exclude}
        onChange={patterns => {
          // An empty list is dropped rather than sent as [], so the stored JSON
          // of a job with no exclusions stays byte-identical to what it was
          // before the field existed (the Go struct has `omitempty`).
          const { exclude: _dropped, ...rest } = source
          onChange(patterns.length > 0 ? { ...rest, exclude: patterns } : rest)
        }}
      />
    )

    if (agentId && agentOnline) {
      return (
        <div className="space-y-4">
          <div className="space-y-1.5">
            <Label>Source Paths</Label>
            <FileBrowser
              agentId={agentId}
              selectedPaths={paths}
              onChange={newPaths => onChange({ ...source, paths: newPaths })}
            />
          </div>
          {excludeField}
        </div>
      )
    }

    // Fallback: plain text input when no agent selected or offline
    return (
      <div className="space-y-4">
        <div className="space-y-1">
          <Label htmlFor="job-paths">
            Source Paths
            <span className="ml-1.5 text-xs text-muted-foreground font-normal">
              {agentId && !agentOnline ? '(agent offline — enter manually)' : '(comma-separated)'}
            </span>
          </Label>
          <Input
            id="job-paths"
            value={paths.join(', ')}
            placeholder="/home/user/documents, /var/data"
            onChange={e => {
              const arr = e.target.value.split(',').map(s => s.trim()).filter(Boolean)
              onChange({ ...source, paths: arr })
            }}
          />
        </div>
        {excludeField}
      </div>
    )
  }
  if (type === 'sqlite') {
    return field('conn_string', 'Database File Path', '/var/db/app.db')
  }
  if (type === 'postgres' || type === 'mysql' || type === 'mongodb') {
    return field('conn_string', 'Connection String', type === 'mongodb' ? 'mongodb://localhost:27017/mydb' : 'postgresql://user:pass@localhost/dbname')
  }
  return null
}

export default function Jobs() {
  const qc = useQueryClient()
  const [dialogOpen, setDialogOpen] = useState(false)
  const [editJob, setEditJob] = useState<BackupJob | null>(null)
  const [form, setForm] = useState<CreateJobRequest>(emptyForm())
  const [deleteTarget, setDeleteTarget] = useState<BackupJob | null>(null)
  const [customCron, setCustomCron] = useState(false)
  const [formError, setFormError] = useState('')

  const { data: jobs = [], isLoading, isError } = useQuery({
    queryKey: ['jobs'],
    queryFn: ({ signal }) => api.listJobs(signal),
    // Health is derived per request from clock-dependent thresholds: a job goes
    // "late" while nobody touches the page, so the page has to keep asking.
    refetchInterval: 30_000,
  })
  const { data: agents = [] } = useQuery({ queryKey: ['agents'], queryFn: ({ signal }) => api.listAgents(signal) })
  const { data: storage = [] } = useQuery({ queryKey: ['storage'], queryFn: ({ signal }) => api.listStorage(signal) })

  const createMut = useMutation({
    mutationFn: (data: CreateJobRequest) => api.createJob(data),
    onSuccess: (job) => {
      qc.invalidateQueries({ queryKey: ['jobs'] })
      closeDialog()
      toast.success(`Job "${job.name}" created`)
    },
    onError: (e) => setFormError(e.message),
  })

  const updateMut = useMutation({
    mutationFn: ({ id, data }: { id: string; data: CreateJobRequest }) => api.updateJob(id, data),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['jobs'] })
      closeDialog()
      toast.success('Job updated')
    },
    onError: (e) => setFormError(e.message),
  })

  const deleteMut = useMutation({
    mutationFn: (id: string) => api.deleteJob(id),
    onSuccess: (_, id) => {
      qc.invalidateQueries({ queryKey: ['jobs'] })
      setDeleteTarget(null)
      const name = jobs.find(j => j.id === id)?.name ?? 'Job'
      toast.success(`"${name}" deleted`)
    },
    onError: (e: Error) => toast.error(`Failed to delete: ${e.message}`),
  })

  const toggleMut = useMutation({
    mutationFn: ({ id, enabled }: { id: string; enabled: boolean }) =>
      api.toggleJob(id, enabled),
    onSuccess: (_, { id, enabled }) => {
      qc.invalidateQueries({ queryKey: ['jobs'] })
      const name = jobs.find(j => j.id === id)?.name ?? 'Job'
      toast.success(`"${name}" ${enabled ? 'enabled' : 'disabled'}`)
    },
    onError: (e: Error) => toast.error(`Failed to toggle: ${e.message}`),
  })

  const triggerMut = useMutation({
    mutationFn: (id: string) => api.triggerJob(id),
    onSuccess: (_, id) => {
      qc.invalidateQueries({ queryKey: ['runs'] })
      qc.invalidateQueries({ queryKey: ['run-stats'] })
      const name = jobs.find(j => j.id === id)?.name ?? 'Job'
      toast.success(`"${name}" dispatched — check Runs for progress`)
    },
    onError: (e: Error) => toast.error(`Failed to trigger: ${e.message}`),
  })

  function openCreate() {
    setEditJob(null)
    setForm(emptyForm())
    setCustomCron(false)
    setFormError('')
    setDialogOpen(true)
  }

  function openEdit(job: BackupJob) {
    setEditJob(job)
    setForm({
      agent_id: job.agent_id,
      name: job.name,
      enabled: job.enabled,
      type: job.type,
      source: job.source,
      storage_destination_id: job.storage_destination_id,
      schedule_cron: job.schedule_cron,
      retention_days: job.retention_days,
      compression: job.compression,
      encrypt: job.encrypt,
      encrypt_passphrase: '',
    })
    const isPreset = CRON_PRESETS.some(p => p.value === job.schedule_cron && p.value !== '__custom__')
    setCustomCron(!isPreset)
    setFormError('')
    setDialogOpen(true)
  }

  function closeDialog() {
    setDialogOpen(false)
    setEditJob(null)
  }

  function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    setFormError('')
    if (!form.agent_id) { setFormError('Select an agent'); return }
    if (!form.storage_destination_id) { setFormError('Select a storage destination'); return }
    if (!form.name.trim()) { setFormError('Job name required'); return }
    const data = { ...form }
    // Omit the passphrase unless the user typed one: the API treats an absent field
    // as "keep the stored passphrase". Sending an empty string used to wipe it and
    // silently downgrade the job to plaintext backups.
    if (!data.encrypt || !data.encrypt_passphrase) delete data.encrypt_passphrase
    if (!editJob && form.encrypt && !form.encrypt_passphrase) {
      setFormError('Passphrase required to enable encryption')
      return
    }
    if (editJob) {
      updateMut.mutate({ id: editJob.id, data })
    } else {
      createMut.mutate(data)
    }
  }

  const isPending = createMut.isPending || updateMut.isPending

  // "Is everything up to date?" answered above the list, so nobody has to read
  // every row to find the one that stopped running three days ago.
  const problems = useMemo(() => {
    const counts = new Map<JobHealth, number>()
    for (const job of jobs) {
      if (isHealthProblem(job.health)) counts.set(job.health, (counts.get(job.health) ?? 0) + 1)
    }
    return [...counts.entries()]
  }, [jobs])

  const offlineAgents = useMemo(
    () => new Set(jobs.filter(j => j.enabled && !j.agent_online).map(j => j.agent_id)).size,
    [jobs],
  )

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <h2 className="font-sans font-bold text-2xl tracking-tight">Backup Jobs</h2>
        <Button onClick={openCreate} size="sm">
          <Plus className="h-4 w-4 mr-1" />
          New Job
        </Button>
      </div>

      <Card>
        <CardHeader className="pb-2">
          <CardTitle className="flex flex-wrap items-center gap-x-3 gap-y-1 text-sm font-semibold uppercase tracking-wider text-muted-foreground">
            <span>
              Jobs
              {jobs.length > 0 && <span className="ml-2 text-xs font-normal normal-case">({jobs.length})</span>}
            </span>
            {jobs.length > 0 && (
              problems.length === 0 ? (
                <span className="text-xs font-medium normal-case tracking-normal text-emerald-600 dark:text-emerald-400">
                  All up to date
                </span>
              ) : (
                <span className="text-xs font-medium normal-case tracking-normal text-destructive">
                  {problems.map(([health, n]) => `${n} ${healthLabel(health).toLowerCase()}`).join(' · ')}
                </span>
              )
            )}
            {offlineAgents > 0 && (
              <span className="text-xs font-medium normal-case tracking-normal text-amber-600 dark:text-amber-400">
                {offlineAgents} agent{offlineAgents === 1 ? '' : 's'} offline
              </span>
            )}
          </CardTitle>
        </CardHeader>
        <CardContent className="px-0">
          {isLoading && <p className="text-sm text-muted-foreground px-6 py-4">Loading…</p>}
          {isError   && <p className="text-sm text-destructive px-6 py-4">Failed to load data.</p>}
          {!isLoading && jobs.length === 0 && (
            <p className="text-sm text-muted-foreground px-6 py-4">No jobs yet. Create one to start backing up.</p>
          )}
          <div>
            {jobs.map((job, i) => {
              const agent = agents.find(a => a.id === job.agent_id)
              const dest  = storage.find(s => s.id === job.storage_destination_id)
              const toggling = toggleMut.isPending && toggleMut.variables?.id === job.id
              return (
                <div
                  key={job.id}
                  className={cn(
                    'flex items-center gap-4 px-6 py-3 hover:bg-accent/50 transition-colors group',
                    i < jobs.length - 1 && 'border-b border-border',
                  )}
                >
                  {/* Enable/disable toggle */}
                  <Switch
                    checked={job.enabled}
                    disabled={toggling}
                    onCheckedChange={(checked) => toggleMut.mutate({ id: job.id, enabled: checked })}
                    className="shrink-0"
                    title={job.enabled ? 'Disable job' : 'Enable job'}
                  />

                  <div className="flex-1 min-w-0">
                    <div className={cn('text-sm font-medium truncate', !job.enabled && 'text-muted-foreground')}>
                      {job.name}
                    </div>
                    <div className="text-xs text-muted-foreground truncate">
                      {job.type} · {agent?.name ?? job.agent_id.slice(0, 8)} · {dest?.name ?? '—'} · {cronHumanize(job.schedule_cron)}
                    </div>
                    {/* The two dates that answer "is this job actually running?" */}
                    <div className="mt-0.5 flex flex-wrap items-center gap-x-3 gap-y-0.5 text-xs text-muted-foreground">
                      <span>
                        Last:{' '}
                        {job.last_run ? (
                          <span
                            className={cn(
                              'font-medium',
                              job.last_run.status === 'failed' && 'text-destructive',
                              job.last_run.status === 'success' && 'text-foreground',
                            )}
                            title={job.last_run.error_message ?? undefined}
                          >
                            {formatRelative(job.last_run.started_at)} · {job.last_run.status}
                          </span>
                        ) : (
                          <span className="font-medium">never</span>
                        )}
                      </span>
                      <span>
                        Next:{' '}
                        <span className="font-medium">
                          {job.next_run ? formatDate(job.next_run) : job.enabled ? 'not scheduled' : 'disabled'}
                        </span>
                      </span>
                      {!job.agent_online && (
                        <span className="inline-flex items-center gap-1 text-amber-600 dark:text-amber-400 font-medium">
                          <WifiOff aria-hidden="true" className="h-3 w-3" />
                          agent offline
                          {job.agent_last_seen && ` · last seen ${formatRelative(job.agent_last_seen)}`}
                        </span>
                      )}
                    </div>
                  </div>

                  <HealthBadge
                    health={job.health}
                    detail={job.health_detail}
                    className="w-28 justify-start shrink-0 hidden sm:inline-flex"
                  />

                  <div className="flex items-center gap-0.5 opacity-0 group-hover:opacity-100 focus-within:opacity-100 transition-opacity shrink-0">
                    <Button
                      variant="ghost"
                      size="icon"
                      className="h-7 w-7"
                      onClick={() => triggerMut.mutate(job.id)}
                      disabled={triggerMut.isPending}
                      title="Run now"
                      aria-label={`Run "${job.name}" now`}
                    >
                      <Play aria-hidden="true" className="h-3.5 w-3.5" />
                    </Button>
                    <Button
                      variant="ghost"
                      size="icon"
                      className="h-7 w-7"
                      onClick={() => openEdit(job)}
                      title="Edit"
                      aria-label={`Edit "${job.name}"`}
                    >
                      <Pencil aria-hidden="true" className="h-3.5 w-3.5" />
                    </Button>
                    <Button
                      variant="ghost"
                      size="icon"
                      className="h-7 w-7"
                      onClick={() => setDeleteTarget(job)}
                      title="Delete"
                      aria-label={`Delete "${job.name}"`}
                    >
                      <Trash2 aria-hidden="true" className="h-3.5 w-3.5 text-destructive" />
                    </Button>
                  </div>
                </div>
              )
            })}
          </div>
        </CardContent>
      </Card>

      <Dialog
 open={dialogOpen} onOpenChange={(o) => !o && closeDialog()}>
        <DialogContent className="max-w-lg max-h-[90vh] overflow-y-auto">
          <DialogHeader>
            <DialogTitle>{editJob ? 'Edit Job' : 'New Backup Job'}</DialogTitle>
          </DialogHeader>
          <form onSubmit={handleSubmit} className="space-y-4">
            <div className="space-y-1">
              <Label htmlFor="job-name">Job Name</Label>
              <Input id="job-name" value={form.name} onChange={e => setForm(f => ({ ...f, name: e.target.value }))} placeholder="My Backup" />
            </div>

            <div className="space-y-1">
              <Label>Agent</Label>
              <Select value={form.agent_id} onValueChange={v => setForm(f => ({ ...f, agent_id: v as string }))}>
                <SelectTrigger className="w-full">
                  <SelectValue placeholder="Select agent..." />
                </SelectTrigger>
                <SelectContent>
                  {agents.map(a => (
                    <SelectItem key={a.id} value={a.id}>{a.name} ({a.status})</SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>

            <div className="space-y-1">
              <Label>Backup Type</Label>
              <Select value={form.type} onValueChange={v => setForm(f => ({ ...f, type: v as BackupJob['type'], source: {} }))}>
                <SelectTrigger className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {JOB_TYPES.map(t => <SelectItem key={t} value={t}>{t}</SelectItem>)}
                </SelectContent>
              </Select>
            </div>

            <SourceFields
              type={form.type}
              source={form.source}
              agentId={form.agent_id}
              agentOnline={agents.find(a => a.id === form.agent_id)?.status === 'online'}
              onChange={source => setForm(f => ({ ...f, source }))}
            />

            <div className="space-y-1">
              <Label>Storage Destination</Label>
              <Select value={form.storage_destination_id} onValueChange={v => setForm(f => ({ ...f, storage_destination_id: v as string }))}>
                <SelectTrigger className="w-full">
                  <SelectValue placeholder="Select storage..." />
                </SelectTrigger>
                <SelectContent>
                  {storage.map(s => (
                    <SelectItem key={s.id} value={s.id}>{s.name} ({s.type})</SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>

            <div className="space-y-1">
              <Label>Schedule</Label>
              <Select
                value={customCron ? '__custom__' : form.schedule_cron}
                onValueChange={v => {
                  if (v === '__custom__') {
                    setCustomCron(true)
                  } else {
                    setCustomCron(false)
                    setForm(f => ({ ...f, schedule_cron: v as string }))
                  }
                }}
              >
                <SelectTrigger className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {CRON_PRESETS.map(p => <SelectItem key={p.value} value={p.value}>{p.label}</SelectItem>)}
                </SelectContent>
              </Select>
              {customCron && (
                <Input
                  value={form.schedule_cron}
                  onChange={e => setForm(f => ({ ...f, schedule_cron: e.target.value }))}
                  placeholder="0 2 * * *"
                  className="mt-1"
                />
              )}
            </div>

            <div className="grid grid-cols-2 gap-4">
              <div className="space-y-1">
                <Label htmlFor="job-retention">Retention (days)</Label>
                <Input
                  id="job-retention"
                  type="number"
                  min={1}
                  value={form.retention_days}
                  onChange={e => setForm(f => ({ ...f, retention_days: parseInt(e.target.value) || 30 }))}
                />
              </div>
              <div className="space-y-1">
                <Label>Compression</Label>
                <Select value={form.compression} onValueChange={v => setForm(f => ({ ...f, compression: v as 'zstd' | 'gzip' }))}>
                  <SelectTrigger className="w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {COMPRESSIONS.map(c => <SelectItem key={c} value={c}>{c}</SelectItem>)}
                  </SelectContent>
                </Select>
              </div>
            </div>

            <div className="flex items-center gap-3">
              <Switch
                checked={form.encrypt}
                onCheckedChange={v => setForm(f => ({ ...f, encrypt: v }))}
              />
              <Label>Encrypt backup</Label>
            </div>

            {form.encrypt && (
              <div className="space-y-1">
                <Label htmlFor="job-passphrase">Passphrase</Label>
                <Input
                  id="job-passphrase"
                  type="password"
                  autoComplete="new-password"
                  value={form.encrypt_passphrase ?? ''}
                  onChange={e => setForm(f => ({ ...f, encrypt_passphrase: e.target.value }))}
                  placeholder={editJob ? 'Leave blank to keep current passphrase' : 'Encryption passphrase'}
                />
                {editJob && (
                  <p className="text-xs text-muted-foreground">
                    Leave blank to keep the current passphrase. Backups already made with a
                    different passphrase can only be restored with that original passphrase.
                  </p>
                )}
              </div>
            )}

            <div className="flex items-center gap-3">
              <Switch
                checked={form.enabled}
                onCheckedChange={v => setForm(f => ({ ...f, enabled: v }))}
              />
              <Label>Enabled</Label>
            </div>

            {formError && <p className="text-sm text-destructive">{formError}</p>}

            <DialogFooter>
              <Button type="button" variant="outline" onClick={closeDialog} disabled={isPending}>Cancel</Button>
              <Button type="submit" disabled={isPending}>
                {isPending ? 'Saving...' : editJob ? 'Save Changes' : 'Create Job'}
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>

      <ConfirmDialog
        open={deleteTarget !== null}
        title="Delete Job"
        description={`Delete job "${deleteTarget?.name}"? All run history will be preserved.`}
        onConfirm={() => deleteTarget && deleteMut.mutate(deleteTarget.id)}
        onCancel={() => setDeleteTarget(null)}
        loading={deleteMut.isPending}
      />
    </div>
  )
}

