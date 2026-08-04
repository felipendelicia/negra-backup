import { useQuery } from '@tanstack/react-query'
import { useTheme } from 'next-themes'
import { Link } from 'react-router-dom'
import { api } from 'src/lib/api'
import { StatusBadge } from 'src/components/StatusBadge'
import { HealthBadge } from 'src/components/HealthBadge'
import { Card, CardContent, CardHeader, CardTitle } from 'src/components/ui/card'
import { BarChart, Bar, XAxis, YAxis, Tooltip, ResponsiveContainer, Cell } from 'recharts'
import { formatDate, formatBytes, formatRelative } from 'src/lib/utils'
import { healthLabel, isHealthProblem } from 'src/lib/health'
import { AlertTriangle, Server, Briefcase, CheckCircle2, ShieldCheck, WifiOff, XCircle } from 'lucide-react'
import { cn } from 'src/lib/utils'
import type { HealthSummary, JobHealth, OpenAlert } from 'src/lib/types'

/** Days of history the chart shows. The server generates one bucket per day. */
const CHART_DAYS = 7
const RECENT_RUNS = 10
/** The dead-man's switch runs on its own interval; the panel just has to be no
 *  more stale than a person staring at it would tolerate. */
const HEALTH_POLL_MS = 30_000

/** Human wording for the alert kinds in notify.AlertAgentOffline / AlertJobLate. */
const ALERT_TITLES: Record<string, string> = {
  agent_offline: 'Agent offline',
  job_late: 'Backup overdue',
}

function alertTitle(alert: OpenAlert): string {
  return ALERT_TITLES[alert.kind] ?? alert.kind.replace(/_/g, ' ')
}

/**
 * The state of the backups, above everything else on the page.
 *
 * Open alerts come first and unconditionally: they are the conditions the
 * dead-man's switch has already decided are worth waking somebody for, and until
 * this panel existed the only place they were visible was an inbox. Under them
 * sit the agents that are silent — an agent that stopped calling home is a
 * backup that stopped happening, whatever its jobs claim.
 */
function HealthPanel({ health, isError }: { health: HealthSummary | undefined; isError: boolean }) {
  if (isError) {
    return (
      <Card>
        <CardContent className="py-4">
          <p className="text-sm text-destructive">
            Could not load backup health. The dead-man&apos;s switch may still be alerting by email.
          </p>
        </CardContent>
      </Card>
    )
  }
  if (!health) return null

  const alerts = health.open_alerts
  const offline = health.agents.items.filter(a => !a.online)
  // An agent with no enabled job is not a problem to be silent about.
  const offlineWithJobs = offline.filter(a => a.enabled_jobs > 0)
  const problemJobs = (Object.entries(health.jobs.by_health) as [JobHealth, number][])
    .filter(([h, n]) => n > 0 && isHealthProblem(h))

  const allClear = alerts.length === 0 && offlineWithJobs.length === 0 && problemJobs.length === 0

  return (
    <Card className={cn(alerts.length > 0 && 'border-destructive/40')}>
      <CardHeader className="pb-2">
        <CardTitle className="flex flex-wrap items-center gap-x-3 gap-y-1 text-sm font-semibold uppercase tracking-wider text-muted-foreground">
          <span>Backup Health</span>
          {allClear ? (
            <span className="inline-flex items-center gap-1.5 text-xs font-medium normal-case tracking-normal text-emerald-600 dark:text-emerald-400">
              <ShieldCheck aria-hidden="true" className="h-3.5 w-3.5" />
              Everything is up to date
            </span>
          ) : (
            <span className="text-xs font-medium normal-case tracking-normal text-destructive">
              {alerts.length > 0 && `${alerts.length} open alert${alerts.length === 1 ? '' : 's'}`}
            </span>
          )}
          {health.thresholds.deadman_enabled === 'false' && (
            <span className="text-xs font-normal normal-case tracking-normal text-amber-600 dark:text-amber-400">
              Dead-man&apos;s switch disabled — nothing is watching for silence
            </span>
          )}
        </CardTitle>
      </CardHeader>

      <CardContent className="space-y-4">
        {/* ── Open alerts, first and loudest ─────────────────────────────── */}
        {alerts.length > 0 && (
          <div role="alert" className="rounded border border-destructive/30 bg-destructive/8 divide-y divide-destructive/15">
            {alerts.map(alert => (
              <div key={`${alert.subject_type}-${alert.subject_id}-${alert.kind}`} className="flex gap-2.5 px-3 py-2.5">
                <AlertTriangle aria-hidden="true" className="h-4 w-4 text-destructive shrink-0 mt-px" />
                <div className="min-w-0 space-y-0.5">
                  <p className="text-sm font-medium text-destructive">{alertTitle(alert)}</p>
                  <p className="text-xs text-foreground/80 break-words">{alert.detail}</p>
                  <p className="text-[11px] text-muted-foreground">
                    Open since {formatDate(alert.first_seen_at)} ({formatRelative(alert.first_seen_at)})
                    {alert.notify_count > 0 && ` · notified ${alert.notify_count}×`}
                  </p>
                </div>
              </div>
            ))}
          </div>
        )}

        {/* ── Agents that stopped calling home ───────────────────────────── */}
        {offlineWithJobs.length > 0 && (
          <div className="space-y-1.5">
            <p className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">
              Agents not reporting
            </p>
            <div className="rounded border border-border divide-y divide-border">
              {offlineWithJobs.map(a => (
                <div key={a.id} className="flex items-center gap-2.5 px-3 py-2">
                  <WifiOff aria-hidden="true" className="h-3.5 w-3.5 text-amber-600 dark:text-amber-400 shrink-0" />
                  <span className="text-sm font-medium truncate flex-1 min-w-0">{a.name}</span>
                  <span className="text-xs text-muted-foreground shrink-0">
                    {a.enabled_jobs} enabled job{a.enabled_jobs === 1 ? '' : 's'}
                  </span>
                  <span className="text-xs text-muted-foreground shrink-0 tabular-nums">
                    {a.last_seen ? `last seen ${formatRelative(a.last_seen)}` : 'never connected'}
                  </span>
                </div>
              ))}
            </div>
            <p className="text-[11px] text-muted-foreground">
              An agent counts as offline after {health.thresholds.agent_offline_after} without a heartbeat.
            </p>
          </div>
        )}

        {/* ── Job verdicts ───────────────────────────────────────────────── */}
        <div className="flex flex-wrap items-center gap-x-4 gap-y-1.5">
          {problemJobs.length > 0 ? (
            problemJobs.map(([h, n]) => (
              <span key={h} className="inline-flex items-center gap-1.5 text-sm">
                <HealthBadge health={h} />
                <span className="text-muted-foreground text-xs">
                  {n} job{n === 1 ? '' : 's'}
                </span>
              </span>
            ))
          ) : (
            <span className="text-xs text-muted-foreground">
              {health.jobs.total} job{health.jobs.total === 1 ? '' : 's'} tracked, none late or failing.
            </span>
          )}
          <Link
            to="/jobs"
            className="text-xs font-medium text-muted-foreground hover:text-foreground underline underline-offset-2 ml-auto"
          >
            {problemJobs.length > 0
              ? `Review ${problemJobs.map(([h]) => healthLabel(h).toLowerCase()).join(' and ')} jobs`
              : 'Open jobs'}
          </Link>
        </div>
      </CardContent>
    </Card>
  )
}

// ── Custom tooltip ────────────────────────────────────────────────────────────
function ChartTooltip({ active, payload, label }: {
  active?: boolean
  payload?: { name: string; value: number; color: string }[]
  label?: string
}) {
  if (!active || !payload?.length) return null
  return (
    <div className="bg-card border border-border rounded shadow-md px-3 py-2 text-xs font-medium">
      <p className="text-muted-foreground mb-1">{label}</p>
      {payload.map(p => (
        <div key={p.name} className="flex items-center gap-2">
          <span className="w-2 h-2 rounded-full" style={{ background: p.color }} />
          <span className="text-foreground">{p.name}: {p.value}</span>
        </div>
      ))}
    </div>
  )
}

/**
 * Turns a server `YYYY-MM-DD` bucket into a local calendar day.
 *
 * `new Date('2026-08-04')` is spec'd to parse as UTC midnight, which renders as
 * Aug 3 anywhere west of Greenwich — the axis would be off by one all day.
 * Feeding the parts to the `Date(y, m, d)` constructor keeps it local.
 */
function parseServerDay(date: string): Date | null {
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(date)
  if (!m) return null
  return new Date(Number(m[1]), Number(m[2]) - 1, Number(m[3]))
}

/** Short axis label, falling back to the raw server string if it is unparseable. */
function dayLabel(date: string): string {
  const d = parseServerDay(date)
  return d ? d.toLocaleDateString('en-US', { month: 'short', day: 'numeric' }) : date
}

export default function Dashboard() {
  const { resolvedTheme } = useTheme()
  const isDark = resolvedTheme === 'dark'

  const { data: agents = [] } = useQuery({ queryKey: ['agents'], queryFn: ({ signal }) => api.listAgents(signal) })
  const { data: jobs = [] } = useQuery({ queryKey: ['jobs'], queryFn: ({ signal }) => api.listJobs(signal) })

  // Counters and the weekly chart come from the database, not from a page of
  // rows: they are true totals, not "the last N runs we happened to fetch".
  const { data: stats, isError: statsError } = useQuery({
    queryKey: ['run-stats', CHART_DAYS],
    queryFn: ({ signal }) => api.getRunStats(CHART_DAYS, signal),
  })

  const { data: recent, isError: recentError } = useQuery({
    queryKey: ['runs', { limit: RECENT_RUNS, offset: 0 }],
    queryFn: ({ signal }) => api.listRuns({ limit: RECENT_RUNS, offset: 0 }, signal),
  })

  const { data: health, isError: healthError } = useQuery({
    queryKey: ['health'],
    queryFn: ({ signal }) => api.getHealth(signal),
    refetchInterval: HEALTH_POLL_MS,
  })

  const onlineAgents = agents.filter(a => a.status === 'online').length
  const enabledJobs  = jobs.filter(j => j.enabled).length

  const totals = stats?.totals
  const totalRuns = totals?.total ?? 0
  const totalLabel = `of ${totalRuns} run${totalRuns === 1 ? '' : 's'}`

  // Already bucketed server-side, one entry per day including empty ones — only
  // the label needs building.
  const chartData = (stats?.daily ?? []).map(d => ({
    date: dayLabel(d.date),
    success: d.success,
    failed: d.failed,
  }))

  const axisColor    = isDark ? '#666' : '#999'
  const successColor = isDark ? '#d4d4d4' : '#171717'

  const statTiles = [
    { label: 'Agents Online', value: onlineAgents,        sub: `${agents.length} registered`, icon: Server,       color: '' },
    { label: 'Active Jobs',   value: enabledJobs,         sub: `${jobs.length} total`,        icon: Briefcase,    color: '' },
    { label: 'Successful',    value: totals?.success ?? 0, sub: totalLabel,                   icon: CheckCircle2, color: 'text-emerald-600 dark:text-emerald-400' },
    { label: 'Failed',        value: totals?.failed ?? 0,  sub: totalLabel,                   icon: XCircle,      color: (totals?.failed ?? 0) > 0 ? 'text-destructive' : '' },
  ]

  const recentRuns = recent?.runs ?? []

  return (
    <div className="space-y-6">
      <div className="flex items-baseline justify-between">
        <h2 className="font-sans font-bold text-2xl tracking-tight">Dashboard</h2>
        {(statsError || recentError) && <p className="text-xs text-destructive">Failed to load run data.</p>}
      </div>

      {/* ── Health, above the counters: an open alert has to be the first
             thing on the page, not something found by scrolling. ────────── */}
      <HealthPanel health={health} isError={healthError} />

      {/* ── Stat grid ───────────────────────────────────────────────────── */}
      <div className="grid grid-cols-2 lg:grid-cols-4 border border-border rounded-lg overflow-hidden">
        {statTiles.map((stat, i) => (
          <div
            key={stat.label}
            className={cn(
              'p-5 flex flex-col gap-1.5 bg-card',
              i > 0 && 'border-l border-border',
              i >= 2 && 'border-t border-border lg:border-t-0',
            )}
          >
            <div className="flex items-center justify-between">
              <span className="text-[10px] font-semibold uppercase tracking-[0.12em] text-muted-foreground">
                {stat.label}
              </span>
              <stat.icon aria-hidden="true" className={cn('h-3.5 w-3.5 text-muted-foreground', stat.color && 'opacity-70')} />
            </div>
            <div className={cn('font-sans font-black text-4xl leading-none tabular-nums', stat.color)}>
              {stat.value}
            </div>
            <div className="text-xs text-muted-foreground">{stat.sub}</div>
          </div>
        ))}
      </div>

      {/* ── Chart ───────────────────────────────────────────────────────── */}
      <Card>
        <CardHeader className="pb-2">
          <CardTitle className="text-sm font-semibold uppercase tracking-wider text-muted-foreground">
            Runs — Last {stats?.days ?? CHART_DAYS} Days
            {totals != null && (totals.running > 0 || totals.cancelled > 0) && (
              <span className="ml-2 text-xs font-normal normal-case tracking-normal">
                ({totals.running} running · {totals.cancelled} cancelled)
              </span>
            )}
          </CardTitle>
        </CardHeader>
        <CardContent>
          <ResponsiveContainer width="100%" height={200}>
            <BarChart data={chartData} barGap={2} barCategoryGap="30%">
              <XAxis
                dataKey="date"
                fontSize={11}
                tickLine={false}
                axisLine={false}
                tick={{ fill: axisColor }}
              />
              <YAxis
                fontSize={11}
                tickLine={false}
                axisLine={false}
                allowDecimals={false}
                tick={{ fill: axisColor }}
                width={28}
              />
              <Tooltip content={<ChartTooltip />} cursor={{ fill: isDark ? 'rgba(255,255,255,0.04)' : 'rgba(0,0,0,0.04)' }} />
              <Bar dataKey="success" name="Success" radius={[3, 3, 0, 0]} fill={successColor} />
              <Bar dataKey="failed"  name="Failed"  radius={[3, 3, 0, 0]}>
                {chartData.map((entry, i) => (
                  <Cell key={i} fill={entry.failed > 0 ? '#ef4444' : (isDark ? '#333' : '#e5e5e5')} />
                ))}
              </Bar>
            </BarChart>
          </ResponsiveContainer>
        </CardContent>
      </Card>

      {/* ── Recent runs ─────────────────────────────────────────────────── */}
      <Card>
        <CardHeader className="pb-2">
          <CardTitle className="text-sm font-semibold uppercase tracking-wider text-muted-foreground">
            Recent Runs
          </CardTitle>
        </CardHeader>
        <CardContent className="px-0">
          {recentRuns.length === 0 ? (
            <p className="text-sm text-muted-foreground px-6 py-4">No runs yet.</p>
          ) : (
            <div>
              {recentRuns.map((run, i) => (
                <div
                  key={run.id}
                  className={cn(
                    'flex items-center gap-4 px-6 py-3 hover:bg-accent/50 transition-colors',
                    i < recentRuns.length - 1 && 'border-b border-border'
                  )}
                >
                  <StatusBadge status={run.status} className="w-16 shrink-0" />
                  <span className="font-mono text-xs text-muted-foreground w-20 shrink-0">{run.id.slice(0, 8)}</span>
                  <span className="text-xs text-muted-foreground flex-1 truncate">{formatBytes(run.size_bytes)}</span>
                  <span className="text-xs text-muted-foreground shrink-0">{formatDate(run.started_at)}</span>
                </div>
              ))}
            </div>
          )}
        </CardContent>
      </Card>
    </div>
  )
}
