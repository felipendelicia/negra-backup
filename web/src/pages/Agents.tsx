import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api } from 'src/lib/api'
import { StatusBadge } from 'src/components/StatusBadge'
import { ConfirmDialog } from 'src/components/ConfirmDialog'
import { Button } from 'src/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from 'src/components/ui/card'
import { formatRelative } from 'src/lib/utils'
import { Trash2 } from 'lucide-react'
import type { Agent } from 'src/lib/types'

export default function Agents() {
  const qc = useQueryClient()
  const [deleteTarget, setDeleteTarget] = useState<Agent | null>(null)

  const { data: agents = [], isLoading, isError } = useQuery({
    queryKey: ['agents'],
    queryFn: ({ signal }) => api.listAgents(signal),
    refetchInterval: 10_000,
  })

  const deleteMut = useMutation({
    mutationFn: (id: string) => api.deleteAgent(id),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['agents'] })
      setDeleteTarget(null)
    },
  })

  return (
    <div className="space-y-6">
      <h2 className="text-2xl font-bold">Agents</h2>
      <Card>
        <CardHeader>
          <CardTitle>Registered Agents</CardTitle>
        </CardHeader>
        <CardContent>
          {isLoading && <p className="text-sm text-muted-foreground">Loading...</p>}
          {isError && <p className="text-sm text-destructive">Failed to load data. Please refresh.</p>}
          {!isLoading && agents.length === 0 && (
            <p className="text-sm text-muted-foreground">No agents registered yet. Install the nat-backup-agent on your machines.</p>
          )}
          <div className="space-y-3">
            {agents.map(agent => (
              <div key={agent.id} className="flex items-center justify-between p-3 border rounded-lg">
                <div className="flex items-center gap-4">
                  <StatusBadge status={agent.status} />
                  <div>
                    <div className="font-medium text-sm">{agent.name}</div>
                    <div className="text-xs text-muted-foreground">
                      {agent.os}/{agent.arch} · v{agent.version} · seen {formatRelative(agent.last_seen)}
                    </div>
                  </div>
                </div>
                <Button
                  variant="ghost"
                  size="icon-sm"
                  onClick={() => setDeleteTarget(agent)}
                >
                  <Trash2 className="h-4 w-4 text-destructive" />
                </Button>
              </div>
            ))}
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
