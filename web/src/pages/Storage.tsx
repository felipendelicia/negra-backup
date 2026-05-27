import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api } from 'src/lib/api'
import { ConfirmDialog } from 'src/components/ConfirmDialog'
import { Button } from 'src/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from 'src/components/ui/card'
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogFooter } from 'src/components/ui/dialog'
import { Input } from 'src/components/ui/input'
import { Label } from 'src/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from 'src/components/ui/select'
import { Plus, Trash2, Pencil, Database } from 'lucide-react'
import type { StorageDestination, CreateStorageRequest } from 'src/lib/types'

const STORAGE_TYPES = ['local', 's3', 'sftp'] as const

function emptyForm(type: StorageDestination['type'] = 'local'): CreateStorageRequest {
  return { name: '', type, config: {} }
}

function ConfigFields({
  type,
  config,
  onChange,
}: {
  type: StorageDestination['type']
  config: Record<string, unknown>
  onChange: (c: Record<string, unknown>) => void
}) {
  function field(key: string, label: string, placeholder?: string, inputType?: string) {
    return (
      <div className="space-y-1" key={key}>
        <Label>{label}</Label>
        <Input
          type={inputType ?? 'text'}
          value={(config[key] as string) ?? ''}
          placeholder={placeholder}
          onChange={e => onChange({ ...config, [key]: e.target.value })}
        />
      </div>
    )
  }

  if (type === 'local') {
    return field('path', 'Storage Path', '/backups')
  }

  if (type === 's3') {
    return (
      <>
        {field('bucket', 'Bucket Name')}
        {field('region', 'Region', 'us-east-1')}
        {field('prefix', 'Key Prefix (optional)', 'backups/')}
        {field('endpoint', 'Endpoint URL (optional, for S3-compatible)', 'https://s3.example.com')}
        {field('access_key_id', 'Access Key ID')}
        {field('secret_access_key', 'Secret Access Key', undefined, 'password')}
      </>
    )
  }

  if (type === 'sftp') {
    return (
      <>
        {field('host', 'Host')}
        {field('port', 'Port', '22')}
        {field('username', 'Username')}
        {field('password', 'Password', undefined, 'password')}
        {field('path', 'Remote Path', '/backups')}
      </>
    )
  }

  return null
}

export default function Storage() {
  const qc = useQueryClient()
  const [dialogOpen, setDialogOpen] = useState(false)
  const [editTarget, setEditTarget] = useState<StorageDestination | null>(null)
  const [form, setForm] = useState<CreateStorageRequest>(emptyForm())
  const [deleteTarget, setDeleteTarget] = useState<StorageDestination | null>(null)
  const [formError, setFormError] = useState('')

  const { data: storage = [], isLoading, isError } = useQuery({
    queryKey: ['storage'],
    queryFn: ({ signal }) => api.listStorage(signal),
  })

  const createMut = useMutation({
    mutationFn: (data: CreateStorageRequest) => api.createStorage(data),
    onSuccess: () => { qc.invalidateQueries({ queryKey: ['storage'] }); closeDialog() },
    onError: (e) => setFormError(e.message),
  })

  const updateMut = useMutation({
    mutationFn: ({ id, data }: { id: string; data: CreateStorageRequest }) => api.updateStorage(id, data),
    onSuccess: () => { qc.invalidateQueries({ queryKey: ['storage'] }); closeDialog() },
    onError: (e) => setFormError(e.message),
  })

  const deleteMut = useMutation({
    mutationFn: (id: string) => api.deleteStorage(id),
    onSuccess: () => { qc.invalidateQueries({ queryKey: ['storage'] }); setDeleteTarget(null) },
  })

  function openCreate() {
    setEditTarget(null)
    setForm(emptyForm())
    setFormError('')
    setDialogOpen(true)
  }

  function openEdit(dest: StorageDestination) {
    setEditTarget(dest)
    setForm({ name: dest.name, type: dest.type, config: {} })
    setFormError('')
    setDialogOpen(true)
  }

  function closeDialog() {
    setDialogOpen(false)
    setEditTarget(null)
  }

  function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    setFormError('')
    if (!form.name.trim()) { setFormError('Name required'); return }
    if (editTarget) {
      updateMut.mutate({ id: editTarget.id, data: form })
    } else {
      createMut.mutate(form)
    }
  }

  const isPending = createMut.isPending || updateMut.isPending

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <h2 className="text-2xl font-bold">Storage Destinations</h2>
        <Button onClick={openCreate} size="sm">
          <Plus className="h-4 w-4 mr-1" />
          Add Storage
        </Button>
      </div>

      <Card>
        <CardHeader><CardTitle>Destinations</CardTitle></CardHeader>
        <CardContent>
          {isLoading && <p className="text-sm text-muted-foreground">Loading...</p>}
          {isError && <p className="text-sm text-destructive">Failed to load data. Please refresh.</p>}
          {!isLoading && storage.length === 0 && (
            <p className="text-sm text-muted-foreground">No storage destinations configured. Add one to enable backups.</p>
          )}
          <div className="space-y-3">
            {storage.map(dest => (
              <div key={dest.id} className="flex items-center justify-between p-3 border rounded-lg">
                <div className="flex items-center gap-3">
                  <Database className="h-5 w-5 text-muted-foreground" />
                  <div>
                    <div className="font-medium text-sm">{dest.name}</div>
                    <div className="text-xs text-muted-foreground uppercase">{dest.type}</div>
                  </div>
                </div>
                <div className="flex items-center gap-1">
                  <Button variant="ghost" size="icon-sm" onClick={() => openEdit(dest)}>
                    <Pencil className="h-4 w-4" />
                  </Button>
                  <Button variant="ghost" size="icon-sm" onClick={() => setDeleteTarget(dest)}>
                    <Trash2 className="h-4 w-4 text-destructive" />
                  </Button>
                </div>
              </div>
            ))}
          </div>
        </CardContent>
      </Card>

      <Dialog open={dialogOpen} onOpenChange={(o) => !o && closeDialog()}>
        <DialogContent className="max-w-md">
          <DialogHeader>
            <DialogTitle>{editTarget ? 'Edit Storage' : 'Add Storage Destination'}</DialogTitle>
          </DialogHeader>
          <form onSubmit={handleSubmit} className="space-y-4">
            <div className="space-y-1">
              <Label>Name</Label>
              <Input
                value={form.name}
                onChange={e => setForm(f => ({ ...f, name: e.target.value }))}
                placeholder="My Storage"
              />
            </div>

            <div className="space-y-1">
              <Label>Type</Label>
              <Select
                value={form.type}
                onValueChange={v => setForm(f => ({ ...f, type: v as StorageDestination['type'], config: {} }))}
              >
                <SelectTrigger className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {STORAGE_TYPES.map(t => (
                    <SelectItem key={t} value={t}>{t.toUpperCase()}</SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>

            <ConfigFields
              type={form.type}
              config={form.config}
              onChange={config => setForm(f => ({ ...f, config }))}
            />

            {formError && <p className="text-sm text-destructive">{formError}</p>}

            <DialogFooter>
              <Button type="button" variant="outline" onClick={closeDialog} disabled={isPending}>Cancel</Button>
              <Button type="submit" disabled={isPending}>
                {isPending ? 'Saving...' : editTarget ? 'Save Changes' : 'Add Storage'}
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>

      <ConfirmDialog
        open={deleteTarget !== null}
        title="Delete Storage Destination"
        description={`Remove "${deleteTarget?.name}"? Jobs using this destination will fail until reassigned.`}
        onConfirm={() => deleteTarget && deleteMut.mutate(deleteTarget.id)}
        onCancel={() => setDeleteTarget(null)}
        loading={deleteMut.isPending}
      />
    </div>
  )
}
