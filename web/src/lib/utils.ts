import { clsx, type ClassValue } from "clsx"
import { twMerge } from "tailwind-merge"

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs))
}

export function formatBytes(bytes: number | null): string {
  if (bytes === null || bytes === 0) return '—'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let i = 0
  let v = bytes
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${v.toFixed(1)} ${units[i]}`
}

export function formatDate(iso: string | null): string {
  if (!iso) return '—'
  return new Intl.DateTimeFormat('en-US', {
    month: 'short', day: 'numeric',
    hour: '2-digit', minute: '2-digit',
  }).format(new Date(iso))
}

export function formatRelative(iso: string | null): string {
  if (!iso) return 'never'
  const diff = Date.now() - new Date(iso).getTime()
  const mins = Math.floor(diff / 60000)
  if (mins < 1) return 'just now'
  if (mins < 60) return `${mins}m ago`
  const hrs = Math.floor(mins / 60)
  if (hrs < 24) return `${hrs}h ago`
  return `${Math.floor(hrs / 24)}d ago`
}

export function cronHumanize(expr: string): string {
  const presets: Record<string, string> = {
    '@daily': 'Every day at midnight',
    '@hourly': 'Every hour',
    '@weekly': 'Every week',
    '@monthly': 'Every month',
    '0 2 * * *': 'Every day at 2:00 AM',
    '0 3 * * *': 'Every day at 3:00 AM',
    '0 0 * * *': 'Every day at midnight',
    '*/5 * * * *': 'Every 5 minutes',
    '0 0 * * 0': 'Every Sunday at midnight',
  }
  return presets[expr] || expr
}
