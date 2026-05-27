import { cn } from 'src/lib/utils'

interface Props {
  status: string
  className?: string
}

const statusConfig: Record<string, { label: string; dot: string; text: string }> = {
  online:  { label: 'Online',  dot: 'bg-emerald-500',        text: 'text-emerald-600 dark:text-emerald-400' },
  offline: { label: 'Offline', dot: 'bg-muted-foreground/50', text: 'text-muted-foreground'                 },
  running: { label: 'Running', dot: 'bg-foreground pulse-dot', text: 'text-foreground'                      },
  success: { label: 'Success', dot: 'bg-foreground',           text: 'text-foreground'                      },
  failed:  { label: 'Failed',  dot: 'bg-destructive',          text: 'text-destructive'                     },
}

export function StatusBadge({ status, className }: Props) {
  const cfg = statusConfig[status] ?? {
    label: status,
    dot: 'bg-muted-foreground',
    text: 'text-muted-foreground',
  }

  return (
    <span className={cn('inline-flex items-center gap-1.5 text-xs font-medium tabular-nums shrink-0', cfg.text, className)}>
      <span className={cn('w-1.5 h-1.5 rounded-full shrink-0', cfg.dot)} />
      {cfg.label}
    </span>
  )
}
