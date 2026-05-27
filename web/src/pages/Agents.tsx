import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { api } from 'src/lib/api'
import { StatusBadge } from 'src/components/StatusBadge'
import { ConfirmDialog } from 'src/components/ConfirmDialog'
import { Button } from 'src/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from 'src/components/ui/card'
import { Dialog, DialogContent, DialogHeader, DialogTitle } from 'src/components/ui/dialog'
import { Input } from 'src/components/ui/input'
import { Label } from 'src/components/ui/label'
import { formatRelative } from 'src/lib/utils'
import { Trash2, RefreshCw, Plus, Copy, Check, Terminal } from 'lucide-react'
import { cn } from 'src/lib/utils'
import type { Agent } from 'src/lib/types'

// ── Copy button ───────────────────────────────────────────────────────────────
function CopyButton({ text, className }: { text: string; className?: string }) {
  const [copied, setCopied] = useState(false)
  function copy() {
    navigator.clipboard.writeText(text).then(() => {
      setCopied(true)
      setTimeout(() => setCopied(false), 2000)
    })
  }
  return (
    <button
      onClick={copy}
      className={cn(
        'flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground transition-colors',
        className,
      )}
    >
      {copied ? <Check className="h-3.5 w-3.5 text-emerald-500" /> : <Copy className="h-3.5 w-3.5" />}
      {copied ? 'Copiado' : 'Copiar'}
    </button>
  )
}

// ── Code block ────────────────────────────────────────────────────────────────
function CodeBlock({ code, label }: { code: string; label?: string }) {
  return (
    <div className="space-y-1.5">
      {label && <p className="text-xs font-medium text-muted-foreground uppercase tracking-wider">{label}</p>}
      <div className="relative group">
        <pre className="bg-zinc-950 border border-zinc-800 rounded p-3 text-xs text-zinc-300 font-mono overflow-x-auto whitespace-pre-wrap break-all leading-relaxed">
          {code}
        </pre>
        <div className="absolute top-2 right-2 opacity-0 group-hover:opacity-100 transition-opacity">
          <CopyButton text={code} className="bg-zinc-800 hover:bg-zinc-700 px-2 py-1 rounded" />
        </div>
      </div>
    </div>
  )
}

// ── New Agent modal ───────────────────────────────────────────────────────────
type NewAgentModal =
  | { step: 'form' }
  | { step: 'instructions'; agentName: string; apiKey: string }

export default function Agents() {
  const qc = useQueryClient()
  const [deleteTarget, setDeleteTarget] = useState<Agent | null>(null)
  const [updatingId, setUpdatingId] = useState<string | null>(null)
  const [updatedIds, setUpdatedIds] = useState<Set<string>>(new Set())
  const [modal, setModal] = useState<NewAgentModal | null>(null)
  const [newName, setNewName] = useState('')
  const [nameError, setNameError] = useState('')

  const serverUrl = window.location.origin

  const { data: agents = [], isLoading, isError } = useQuery({
    queryKey: ['agents'],
    queryFn: ({ signal }) => api.listAgents(signal),
    refetchInterval: 10_000,
  })

  const createMut = useMutation({
    mutationFn: (name: string) => api.createAgent(name),
    onSuccess: (res) => {
      qc.invalidateQueries({ queryKey: ['agents'] })
      setModal({ step: 'instructions', agentName: res.agent.name, apiKey: res.api_key })
    },
    onError: (e: Error) => setNameError(e.message),
  })

  const deleteMut = useMutation({
    mutationFn: (id: string) => api.deleteAgent(id),
    onSuccess: (_, id) => {
      const name = agents.find(a => a.id === id)?.name ?? 'Agent'
      qc.invalidateQueries({ queryKey: ['agents'] })
      setDeleteTarget(null)
      toast.success(`${name} removed`)
    },
    onError: (e: Error) => toast.error(`Failed to remove: ${e.message}`),
  })

  const updateMut = useMutation({
    mutationFn: (id: string) => api.updateAgent(id),
    onMutate: (id) => {
      setUpdatingId(id)
      const name = agents.find(a => a.id === id)?.name ?? 'Agent'
      toast.loading(`Sending update signal to ${name}…`, { id: 'agent-update' })
    },
    onSuccess: (_, id) => {
      setUpdatingId(null)
      setUpdatedIds(prev => new Set(prev).add(id))
      const name = agents.find(a => a.id === id)?.name ?? 'Agent'
      toast.success(`Update signal sent to ${name}. Agent will reconnect shortly.`, {
        id: 'agent-update',
        duration: 6_000,
      })
      setTimeout(() => {
        qc.invalidateQueries({ queryKey: ['agents'] })
        setUpdatedIds(prev => { const s = new Set(prev); s.delete(id); return s })
      }, 5_000)
    },
    onError: (e: Error, id) => {
      setUpdatingId(null)
      setUpdatedIds(prev => { const s = new Set(prev); s.delete(id); return s })
      const name = agents.find(a => a.id === id)?.name ?? 'Agent'
      toast.error(`Could not update ${name}: ${e.message}`, { id: 'agent-update' })
    },
  })

  function openNewAgent() {
    setNewName('')
    setNameError('')
    setModal({ step: 'form' })
  }

  function handleCreate(e: React.FormEvent) {
    e.preventDefault()
    setNameError('')
    if (!newName.trim()) { setNameError('El nombre es requerido'); return }
    createMut.mutate(newName.trim())
  }

  function closeModal() {
    setModal(null)
    setNewName('')
    setNameError('')
  }

  // ── Install commands ────────────────────────────────────────────────────────
  const linuxCmd = (apiKey: string) =>
    `curl -fsSL https://raw.githubusercontent.com/felipendelicia/negra-backup/main/scripts/install.sh | sudo bash -s -- --agent-only --server-url ${serverUrl} --api-key ${apiKey}`

  const windowsCmd = (apiKey: string) =>
    `& ([scriptblock]::Create((iwr -useb 'https://raw.githubusercontent.com/felipendelicia/negra-backup/main/scripts/install-agent.ps1').Content)) -ServerUrl '${serverUrl}' -ApiKey '${apiKey}'`

  const yamlSnippet = (apiKey: string) =>
    `server_url: ${serverUrl}\napi_key: ${apiKey}`

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <h2 className="font-sans font-bold text-2xl tracking-tight">Agents</h2>
        <Button size="sm" onClick={openNewAgent}>
          <Plus className="h-4 w-4 mr-1.5" />
          New Agent
        </Button>
      </div>

      <Card>
        <CardHeader className="pb-2">
          <CardTitle className="text-sm font-semibold uppercase tracking-wider text-muted-foreground">
            Registered Agents
            {agents.length > 0 && (
              <span className="ml-2 text-xs font-normal normal-case">({agents.length})</span>
            )}
          </CardTitle>
        </CardHeader>
        <CardContent className="px-0">
          {isLoading && <p className="text-sm text-muted-foreground px-6 py-4">Loading…</p>}
          {isError   && <p className="text-sm text-destructive px-6 py-4">Failed to load data.</p>}
          {!isLoading && agents.length === 0 && (
            <p className="text-sm text-muted-foreground px-6 py-4">
              No agents registered yet. Click <strong>New Agent</strong> to get started.
            </p>
          )}
          <div>
            {agents.map((agent, i) => {
              const isUpdating = updatingId === agent.id
              const justSent  = updatedIds.has(agent.id)
              return (
                <div
                  key={agent.id}
                  className={cn(
                    'flex items-center gap-4 px-6 py-3 hover:bg-accent/50 transition-colors group',
                    i < agents.length - 1 && 'border-b border-border',
                  )}
                >
                  <StatusBadge status={agent.status} className="w-16 shrink-0" />
                  <div className="flex-1 min-w-0">
                    <div className="text-sm font-medium truncate">{agent.name}</div>
                    <div className="text-xs text-muted-foreground">
                      {[agent.os, agent.arch].filter(Boolean).join('/')}
                      {agent.version && ` · v${agent.version}`}
                      {agent.last_seen && ` · seen ${formatRelative(agent.last_seen)}`}
                    </div>
                  </div>
                  {agent.status === 'online' && (
                    <Button
                      variant="ghost"
                      size="sm"
                      className={cn(
                        'h-7 gap-1.5 text-xs opacity-0 group-hover:opacity-100 transition-all text-muted-foreground hover:text-foreground',
                        (isUpdating || justSent) && 'opacity-100',
                        justSent && 'text-emerald-600 dark:text-emerald-400',
                      )}
                      onClick={() => updateMut.mutate(agent.id)}
                      disabled={isUpdating || justSent}
                      title="Descargar último release y reiniciar agente"
                    >
                      <RefreshCw className={cn('h-3.5 w-3.5', isUpdating && 'animate-spin')} />
                      {justSent ? 'Actualizando…' : 'Update'}
                    </Button>
                  )}
                  <Button
                    variant="ghost"
                    size="icon"
                    className="h-7 w-7 opacity-0 group-hover:opacity-100 transition-opacity"
                    onClick={() => setDeleteTarget(agent)}
                  >
                    <Trash2 className="h-3.5 w-3.5 text-destructive" />
                  </Button>
                </div>
              )
            })}
          </div>
        </CardContent>
      </Card>

      {/* ── New Agent modal ────────────────────────────────────────────────── */}
      <Dialog open={modal !== null} onOpenChange={(o) => !o && closeModal()}>
        <DialogContent className={cn(
          'flex flex-col gap-0 p-0 overflow-hidden',
          modal?.step === 'instructions' ? 'max-w-2xl' : 'max-w-sm',
        )}>

          {modal?.step === 'form' && (
            <>
              <DialogHeader className="px-6 pt-6 pb-4">
                <DialogTitle className="text-base">New Agent</DialogTitle>
                <p className="text-xs text-muted-foreground mt-1">
                  Name this agent. An API key will be generated — you'll use it to connect the agent to this server.
                </p>
              </DialogHeader>
              <form onSubmit={handleCreate} className="px-6 pb-6 space-y-4">
                <div className="space-y-1.5">
                  <Label htmlFor="agent-name" className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">
                    Name
                  </Label>
                  <Input
                    id="agent-name"
                    placeholder="my-server, prod-db, raspberry-pi…"
                    value={newName}
                    onChange={e => setNewName(e.target.value)}
                    autoFocus
                    className="border-2 focus-visible:ring-0 focus-visible:border-foreground"
                  />
                  {nameError && <p className="text-xs text-destructive">{nameError}</p>}
                </div>
                <div className="flex gap-2 justify-end pt-1">
                  <Button type="button" variant="outline" size="sm" onClick={closeModal}>
                    Cancel
                  </Button>
                  <Button type="submit" size="sm" disabled={createMut.isPending}>
                    {createMut.isPending ? 'Creating…' : 'Create agent'}
                  </Button>
                </div>
              </form>
            </>
          )}

          {modal?.step === 'instructions' && (
            <>
              <DialogHeader className="px-6 pt-6 pb-4 border-b">
                <DialogTitle className="flex items-center gap-2 text-base">
                  <Terminal className="h-4 w-4" />
                  Install agent — <span className="font-mono text-sm">{modal.agentName}</span>
                </DialogTitle>
                <p className="text-xs text-muted-foreground mt-1">
                  The API key is shown <strong>only once</strong>. Save it before closing.
                </p>
              </DialogHeader>

              <div className="px-6 py-5 space-y-5 overflow-y-auto max-h-[70vh]">

                {/* API Key */}
                <div className="space-y-1.5">
                  <p className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">API Key</p>
                  <div className="flex items-center gap-2">
                    <code className="flex-1 font-mono text-xs bg-muted border border-border rounded px-3 py-2 break-all">
                      {modal.apiKey}
                    </code>
                    <CopyButton text={modal.apiKey} className="shrink-0 px-2 py-2 bg-muted border border-border rounded hover:bg-accent" />
                  </div>
                </div>

                <div className="border-t border-border" />

                {/* Linux */}
                <CodeBlock
                  label="🐧 Linux — install & configure"
                  code={linuxCmd(modal.apiKey)}
                />

                {/* Windows */}
                <CodeBlock
                  label="🪟 Windows — install & configure (PowerShell as Admin)"
                  code={windowsCmd(modal.apiKey)}
                />

                <div className="border-t border-border" />

                {/* Manual config */}
                <div className="space-y-1.5">
                  <p className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">
                    Or configure manually — <span className="normal-case font-normal">agent.yaml</span>
                  </p>
                  <CodeBlock code={yamlSnippet(modal.apiKey)} />
                  <p className="text-xs text-muted-foreground">
                    Then run: <code className="font-mono bg-muted px-1 rounded">nat-backup-agent /etc/nat-backup/agent.yaml</code>
                  </p>
                </div>

              </div>

              <div className="px-6 py-4 border-t flex justify-end">
                <Button size="sm" onClick={closeModal}>
                  Done, I've saved the key
                </Button>
              </div>
            </>
          )}
        </DialogContent>
      </Dialog>

      <ConfirmDialog
        open={deleteTarget !== null}
        title="Delete Agent"
        description={`Remove agent "${deleteTarget?.name}"? This will not stop the agent process — it will re-register on next connection.`}
        onConfirm={() => deleteTarget && deleteMut.mutate(deleteTarget.id)}
        onCancel={() => setDeleteTarget(null)}
        loading={deleteMut.isPending}
      />
    </div>
  )
}
