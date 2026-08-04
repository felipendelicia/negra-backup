import { cn } from 'src/lib/utils'
import { healthStyle } from 'src/lib/health'
import type { JobHealth } from 'src/lib/types'

/** The verdict on one job, from `GET /api/jobs`. See src/lib/health.ts for why
 *  the states differ in shape and not only in colour. */
export function HealthBadge({
  health,
  detail,
  className,
}: {
  health: JobHealth
  /** The server's one-line explanation; shown on hover and to screen readers. */
  detail?: string
  className?: string
}) {
  const cfg = healthStyle(health)

  return (
    <span
      className={cn(
        'inline-flex items-center gap-1.5 text-xs font-medium whitespace-nowrap shrink-0',
        cfg.text,
        className,
      )}
      title={detail || cfg.label}
    >
      <span
        aria-hidden="true"
        className={cn('w-2 h-2 rounded-full shrink-0', cfg.dot, cfg.hollow && 'bg-transparent')}
      />
      {cfg.label}
      {detail ? <span className="sr-only"> — {detail}</span> : null}
    </span>
  )
}
