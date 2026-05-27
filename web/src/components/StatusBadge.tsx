import { Badge } from 'src/components/ui/badge'
import { cn } from 'src/lib/utils'

interface Props {
  status: string
  className?: string
}

const statusConfig: Record<string, { label: string; className: string }> = {
  online:  { label: 'Online',  className: 'bg-green-100 text-green-800 dark:bg-green-900 dark:text-green-300' },
  offline: { label: 'Offline', className: 'bg-gray-100 text-gray-600 dark:bg-gray-800 dark:text-gray-400' },
  running: { label: 'Running', className: 'bg-blue-100 text-blue-800 dark:bg-blue-900 dark:text-blue-300' },
  success: { label: 'Success', className: 'bg-green-100 text-green-800 dark:bg-green-900 dark:text-green-300' },
  failed:  { label: 'Failed',  className: 'bg-red-100 text-red-800 dark:bg-red-900 dark:text-red-300' },
}

export function StatusBadge({ status, className }: Props) {
  const cfg = statusConfig[status] ?? { label: status, className: '' }
  return (
    <Badge className={cn('font-normal', cfg.className, className)}>
      {cfg.label}
    </Badge>
  )
}
