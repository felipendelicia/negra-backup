import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { api } from 'src/lib/api'
import { StatusBadge } from 'src/components/StatusBadge'
import { ConfirmDialog } from 'src/components/ConfirmDialog'
import { Button } from 'src/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from 'src/components/ui/card'
import { formatRelative } from 'src/lib/utils'
import { Trash2, RefreshCw } from 'lucide-react'
import { cn } from 'src/lib/utils'
import type { Agent } from 'src/lib/types'

export default function Agents() {
  const qc = useQueryClient()
  const [deleteTarget, setDeleteTarget] = useState<Agent | null>(null)
  const [updatingId, setUpdatingId] = useState<string | null>(null)
  const [updatedIds, setUpdatedIds] = useState<Set<string>>(new Set())

  const { data: agents = [], isLoading, isError } = useQuery({
    queryKey: ['agents'],
    queryFn: ({ signal }) => api.listAgents(signal),
    refetchInterval: 10_000,
  })

  const deleteMut = useMutation({
    mutationFn: (id: string) => api.deleteAgent(id),
    onSuccess: (_, id) => {
      const name = agents.find(a => a.id === id)?.name ?? 'Agent'
      qc.invalidateQueries({ queryKey: ['agents'] })
      setDeleteTarget(null)
      toast.success(`${name} removed`)
    },
    onError: (err: Error) => toast.error(`Failed to remove agent: ${err.message}`),
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
    onError: (err: Error, id) => {
      setUpdatingId(null)
      setUpdatedIds(prev => { const s = new Set(prev); s.delete(id); return s })
      const name = agents.find(a => a.id === id)?.name ?? 'Agent'
      toast.error(`Could not update ${name}: ${err.message}`, { id: 'agent-update' })
    },
  })

  return (
    <div className="space-y-6">
      <h2 className="font-sans font-bold text-2xl tracking-tight">Agents</h2>

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
              No agents registered yet. Install the negra-backup-agent on your machines.
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

                  {/* Update button — only for online agents */}
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
                      title="Download latest release and restart agent"
                    >
                      <RefreshCw className={cn('h-3.5 w-3.5', isUpdating && 'animate-spin')} />
                      {justSent ? 'Updating…' : 'Update'}
                    </Button>
                  )}

                  <Button
                    variant="ghost"
                    size="icon"
                    className="h-7 w-7 opacity-0 group-hover:opacity-100 transition-opacity"
                    onClick={() => setDeleteTarget(agent)}
                    title="Remove agent"
                  >
                    <Trash2 className="h-3.5 w-3.5 text-destructive" />
                  </Button>
                </div>
              )
            })}
          </div>
        </CardContent>
      </Card>

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
