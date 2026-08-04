import type { JobHealth } from './types'

/**
 * Presentation for every value of `notify.HealthStatus`.
 *
 * Colour alone is not the signal: `disabled` and `late` both mean "not backing up
 * right now" but only one of them is a problem, and `never_run` and `ok` both mean
 * "nothing has failed" but only one of them means the data is safe. So the states
 * differ in shape too — a hollow ring for the two that describe an absence
 * (nothing scheduled, nothing ever produced), a filled dot for the rest.
 *
 * Lives outside the component module so the badge file exports components only,
 * which is what react-refresh needs to hot-reload it.
 */
export interface HealthStyle {
  label: string
  text: string
  dot: string
  hollow?: boolean
}

export const HEALTH_STYLES: Record<JobHealth, HealthStyle> = {
  ok:        { label: 'Up to date', text: 'text-emerald-600 dark:text-emerald-400', dot: 'bg-emerald-500' },
  running:   { label: 'Running',    text: 'text-foreground',                        dot: 'bg-foreground pulse-dot' },
  late:      { label: 'Late',       text: 'text-amber-600 dark:text-amber-400',     dot: 'bg-amber-500' },
  failing:   { label: 'Failing',    text: 'text-destructive',                       dot: 'bg-destructive' },
  never_run: { label: 'Never run',  text: 'text-sky-600 dark:text-sky-400',         dot: 'border-2 border-sky-500', hollow: true },
  disabled:  { label: 'Disabled',   text: 'text-muted-foreground',                  dot: 'border-2 border-muted-foreground/50', hollow: true },
  unknown:   { label: 'Unknown',    text: 'text-muted-foreground',                  dot: 'bg-muted-foreground/60' },
}

export function healthStyle(health: JobHealth): HealthStyle {
  return HEALTH_STYLES[health] ?? {
    label: health,
    text: 'text-muted-foreground',
    dot: 'bg-muted-foreground',
  }
}

/** The states an operator has to do something about. `disabled` is a choice, not
 *  a fault, and `unknown` means the verdict is missing rather than bad. */
export function isHealthProblem(health: JobHealth): boolean {
  return health === 'late' || health === 'failing' || health === 'never_run'
}

export function healthLabel(health: JobHealth): string {
  return healthStyle(health).label
}
