import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { api } from 'src/lib/api'
import { StatusBadge } from 'src/components/StatusBadge'
import { ConfirmDialog } from 'src/components/ConfirmDialog'
import { Button } from 'src/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from 'src/components/ui/card'
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogFooter } from 'src/components/ui/dialog'
import { Input } from 'src/components/ui/input'
import { Label } from 'src/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from 'src/components/ui/select'
import { Switch } from 'src/components/ui/switch'
import { cronHumanize } from 'src/lib/utils'
import { Plus, Trash2, Play, Pencil } from 'lucide-react'
import type { BackupJob, CreateJobRequest } from 'src/lib/types'

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

function SourceFields({
  type,
  source,
  onChange,
}: {
  type: BackupJob['type']
  source: Record<string, unknown>
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
    // paths is stored as array; UI shows comma-separated string
    const pathsVal = Array.isArray(source['paths'])
      ? (source['paths'] as string[]).join(', ')
      : (source['paths'] as string) ?? ''
    return (
      <div className="space-y-1">
        <Label>Source Paths <span className="text-xs text-muted-foreground">(comma-separated)</span></Label>
        <Input
          value={pathsVal}
          placeholder="/home/user/documents, /var/data"
          onChange={e => {
            const arr = e.target.value.split(',').map(s => s.trim()).filter(Boolean)
            onChange({ paths: arr })
          }}
        />
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

  const triggerMut = useMutation({
    mutationFn: (id: string) => api.triggerJob(id),
    onSuccess: (_, id) => {
      qc.invalidateQueries({ queryKey: ['runs'] })
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
    if (!data.encrypt) delete data.encrypt_passphrase
    if (editJob) {
      updateMut.mutate({ id: editJob.id, data })
    } else {
      createMut.mutate(data)
    }
  }

  const isPending = createMut.isPending || updateMut.isPending

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <h2 className="text-2xl font-bold">Backup Jobs</h2>
        <Button onClick={openCreate} size="sm">
          <Plus className="h-4 w-4 mr-1" />
          New Job
        </Button>
      </div>

      <Card>
        <CardHeader><CardTitle>Jobs</CardTitle></CardHeader>
        <CardContent>
          {isLoading && <p className="text-sm text-muted-foreground">Loading...</p>}
          {isError && <p className="text-sm text-destructive">Failed to load data. Please refresh.</p>}
          {!isLoading && jobs.length === 0 && (
            <p className="text-sm text-muted-foreground">No jobs yet. Create one to start backing up.</p>
          )}
          <div className="space-y-3">
            {jobs.map(job => {
              const agent = agents.find(a => a.id === job.agent_id)
              const dest = storage.find(s => s.id === job.storage_destination_id)
              return (
                <div key={job.id} className="flex items-center justify-between p-3 border rounded-lg">
                  <div className="flex items-center gap-4 min-w-0">
                    <StatusBadge status={job.enabled ? 'online' : 'offline'} />
                    <div className="min-w-0">
                      <div className="font-medium text-sm truncate">{job.name}</div>
                      <div className="text-xs text-muted-foreground">
                        {job.type} · {agent?.name ?? job.agent_id.slice(0, 8)} · {dest?.name ?? '—'} · {cronHumanize(job.schedule_cron)}
                      </div>
                    </div>
                  </div>
                  <div className="flex items-center gap-1 shrink-0">
                    <Button
                      variant="ghost"
                      size="icon-sm"
                      onClick={() => triggerMut.mutate(job.id)}
                      disabled={triggerMut.isPending}
                      title="Run now"
                    >
                      <Play className="h-4 w-4 text-green-600" />
                    </Button>
                    <Button variant="ghost" size="icon-sm" onClick={() => openEdit(job)}>
                      <Pencil className="h-4 w-4" />
                    </Button>
                    <Button variant="ghost" size="icon-sm" onClick={() => setDeleteTarget(job)}>
                      <Trash2 className="h-4 w-4 text-destructive" />
                    </Button>
                  </div>
                </div>
              )
            })}
          </div>
        </CardContent>
      </Card>

      <Dialog open={dialogOpen} onOpenChange={(o) => !o && closeDialog()}>
        <DialogContent className="max-w-lg max-h-[90vh] overflow-y-auto">
          <DialogHeader>
            <DialogTitle>{editJob ? 'Edit Job' : 'New Backup Job'}</DialogTitle>
          </DialogHeader>
          <form onSubmit={handleSubmit} className="space-y-4">
            <div className="space-y-1">
              <Label>Job Name</Label>
              <Input value={form.name} onChange={e => setForm(f => ({ ...f, name: e.target.value }))} placeholder="My Backup" />
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
                <Label>Retention (days)</Label>
                <Input
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
                <Label>Passphrase</Label>
                <Input
                  type="password"
                  value={form.encrypt_passphrase ?? ''}
                  onChange={e => setForm(f => ({ ...f, encrypt_passphrase: e.target.value }))}
                  placeholder="Encryption passphrase"
                />
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

