import { useQuery } from '@tanstack/react-query'
import { api } from 'src/lib/api'
import { StatusBadge } from 'src/components/StatusBadge'
import { Card, CardContent, CardHeader, CardTitle } from 'src/components/ui/card'
import { BarChart, Bar, XAxis, YAxis, Tooltip, ResponsiveContainer, Legend } from 'recharts'
import { formatDate, formatBytes } from 'src/lib/utils'
import { Server, Briefcase, CheckCircle, XCircle } from 'lucide-react'

export default function Dashboard() {
  const { data: agents = [] } = useQuery({ queryKey: ['agents'], queryFn: ({ signal }) => api.listAgents(signal) })
  const { data: jobs = [] } = useQuery({ queryKey: ['jobs'], queryFn: ({ signal }) => api.listJobs(signal) })
  const { data: runs = [], isError: runsError } = useQuery({ queryKey: ['runs'], queryFn: ({ signal }) => api.listRuns(undefined, signal) })

  const onlineAgents = agents.filter(a => a.status === 'online').length
  const enabledJobs = jobs.filter(j => j.enabled).length
  const successRuns = runs.filter(r => r.status === 'success').length
  const failedRuns = runs.filter(r => r.status === 'failed').length

  const last7Days = Array.from({ length: 7 }, (_, i) => {
    const d = new Date()
    d.setDate(d.getDate() - (6 - i))
    const key = d.toISOString().slice(0, 10)
    const dayRuns = runs.filter(r => r.started_at?.startsWith(key))
    return {
      date: d.toLocaleDateString('en-US', { month: 'short', day: 'numeric' }),
      success: dayRuns.filter(r => r.status === 'success').length,
      failed: dayRuns.filter(r => r.status === 'failed').length,
    }
  })

  const recentRuns = runs.slice(0, 10)

  return (
    <div className="space-y-6">
      <h2 className="text-2xl font-bold">Dashboard</h2>
      {runsError && <p className="text-sm text-destructive">Failed to load run data. Please refresh.</p>}
      <div className="grid grid-cols-2 lg:grid-cols-4 gap-4">
        <Card>
          <CardHeader className="flex flex-row items-center justify-between pb-2">
            <CardTitle className="text-sm text-muted-foreground">Agents Online</CardTitle>
            <Server className="h-4 w-4 text-muted-foreground" />
          </CardHeader>
          <CardContent>
            <div className="text-3xl font-bold">{onlineAgents}</div>
            <p className="text-xs text-muted-foreground">{agents.length} total</p>
          </CardContent>
        </Card>
        <Card>
          <CardHeader className="flex flex-row items-center justify-between pb-2">
            <CardTitle className="text-sm text-muted-foreground">Active Jobs</CardTitle>
            <Briefcase className="h-4 w-4 text-muted-foreground" />
          </CardHeader>
          <CardContent>
            <div className="text-3xl font-bold">{enabledJobs}</div>
            <p className="text-xs text-muted-foreground">{jobs.length} total</p>
          </CardContent>
        </Card>
        <Card>
          <CardHeader className="flex flex-row items-center justify-between pb-2">
            <CardTitle className="text-sm text-muted-foreground">Successful Runs</CardTitle>
            <CheckCircle className="h-4 w-4 text-green-500" />
          </CardHeader>
          <CardContent>
            <div className="text-3xl font-bold text-green-600">{successRuns}</div>
          </CardContent>
        </Card>
        <Card>
          <CardHeader className="flex flex-row items-center justify-between pb-2">
            <CardTitle className="text-sm text-muted-foreground">Failed Runs</CardTitle>
            <XCircle className="h-4 w-4 text-red-500" />
          </CardHeader>
          <CardContent>
            <div className="text-3xl font-bold text-red-600">{failedRuns}</div>
          </CardContent>
        </Card>
      </div>
      <Card>
        <CardHeader><CardTitle>Runs — Last 7 Days</CardTitle></CardHeader>
        <CardContent>
          <ResponsiveContainer width="100%" height={220}>
            <BarChart data={last7Days}>
              <XAxis dataKey="date" fontSize={12} />
              <YAxis fontSize={12} allowDecimals={false} />
              <Tooltip />
              <Legend />
              <Bar dataKey="success" fill="#22c55e" name="Success" radius={[2,2,0,0]} />
              <Bar dataKey="failed"  fill="#ef4444" name="Failed"  radius={[2,2,0,0]} />
            </BarChart>
          </ResponsiveContainer>
        </CardContent>
      </Card>
      <Card>
        <CardHeader><CardTitle>Recent Runs</CardTitle></CardHeader>
        <CardContent>
          <div className="space-y-2">
            {recentRuns.length === 0 && <p className="text-sm text-muted-foreground">No runs yet.</p>}
            {recentRuns.map(run => (
              <div key={run.id} className="flex items-center justify-between py-2 border-b last:border-0">
                <div className="text-sm font-mono text-muted-foreground">{run.id.slice(0, 8)}</div>
                <StatusBadge status={run.status} />
                <div className="text-sm text-muted-foreground">{formatBytes(run.size_bytes)}</div>
                <div className="text-sm text-muted-foreground">{formatDate(run.started_at)}</div>
              </div>
            ))}
          </div>
        </CardContent>
      </Card>
    </div>
  )
}
