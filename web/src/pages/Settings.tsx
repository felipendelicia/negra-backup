import { useState, useEffect } from 'react'
import { useQuery, useMutation } from '@tanstack/react-query'
import { useTheme } from 'next-themes'
import { api } from 'src/lib/api'
import { Button } from 'src/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from 'src/components/ui/card'
import { Input } from 'src/components/ui/input'
import { Label } from 'src/components/ui/label'
import { Switch } from 'src/components/ui/switch'
import { Sun, Moon, Monitor } from 'lucide-react'
import { cn } from 'src/lib/utils'
import type { EmailNotificationConfig } from 'src/lib/types'

const defaultConfig = (): EmailNotificationConfig => ({
  smtp_host: '',
  smtp_port: 587,
  from: '',
  to: [],
  tls: true,
  username: '',
  password: '',
})

const THEMES = [
  { value: 'light',  label: 'Light',  icon: Sun     },
  { value: 'dark',   label: 'Dark',   icon: Moon    },
  { value: 'system', label: 'System', icon: Monitor },
] as const

export default function Settings() {
  const { theme: currentTheme, setTheme } = useTheme()

  const [config, setConfig] = useState<EmailNotificationConfig>(defaultConfig())
  const [toStr, setToStr] = useState('')
  const [saveStatus, setSaveStatus] = useState<'idle' | 'saved' | 'error'>('idle')
  const [errorMsg, setErrorMsg] = useState('')
  const [initialized, setInitialized] = useState(false)

  const { data: settings, isError } = useQuery({
    queryKey: ['notification-settings'],
    queryFn: ({ signal }) => api.getNotificationSettings(signal),
  })

  useEffect(() => {
    if (!initialized && settings && 'type' in settings && settings.type === 'email') {
      setConfig(settings.config as EmailNotificationConfig)
      setToStr((settings.config as EmailNotificationConfig).to.join(', '))
      setInitialized(true)
    }
  }, [initialized, settings])

  const saveMut = useMutation({
    mutationFn: (data: { type: string; config: EmailNotificationConfig }) =>
      api.updateNotificationSettings(data),
    onSuccess: () => {
      setSaveStatus('saved')
      setTimeout(() => setSaveStatus('idle'), 3000)
    },
    onError: (e: Error) => {
      setSaveStatus('error')
      setErrorMsg(e.message)
      setTimeout(() => setSaveStatus('idle'), 5000)
    },
  })

  function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    const to = toStr.split(',').map(s => s.trim()).filter(Boolean)
    saveMut.mutate({ type: 'email', config: { ...config, to } })
  }

  return (
    <div className="space-y-6">
      <h2 className="font-sans font-bold text-2xl tracking-tight">Settings</h2>

      {/* ── Appearance ──────────────────────────────────────────────────── */}
      <Card>
        <CardHeader className="pb-2">
          <CardTitle className="text-sm font-semibold uppercase tracking-wider text-muted-foreground">
            Appearance
          </CardTitle>
        </CardHeader>
        <CardContent>
          <div className="space-y-3">
            <p className="text-sm text-muted-foreground">Choose your preferred color theme.</p>
            <div className="flex gap-2">
              {THEMES.map(({ value, label, icon: Icon }) => (
                <button
                  key={value}
                  onClick={() => setTheme(value)}
                  className={cn(
                    'flex-1 flex flex-col items-center gap-2 p-4 rounded-lg border-2 transition-all text-sm',
                    currentTheme === value
                      ? 'border-foreground bg-accent text-foreground font-medium'
                      : 'border-border text-muted-foreground hover:border-foreground/30 hover:text-foreground'
                  )}
                >
                  <Icon className="h-5 w-5" />
                  {label}
                </button>
              ))}
            </div>
          </div>
        </CardContent>
      </Card>

      {/* ── Email Notifications ─────────────────────────────────────────── */}
      <Card>
        <CardHeader className="pb-2">
          <CardTitle className="text-sm font-semibold uppercase tracking-wider text-muted-foreground">
            Email Notifications
          </CardTitle>
        </CardHeader>
        <CardContent>
          {isError && <p className="text-sm text-destructive mb-4">Failed to load settings.</p>}
          <form onSubmit={handleSubmit} className="space-y-4 max-w-lg">
            <div className="grid grid-cols-2 gap-4">
              <div className="space-y-1.5">
                <Label htmlFor="smtp_host" className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">
                  SMTP Host
                </Label>
                <Input
                  id="smtp_host"
                  value={config.smtp_host}
                  onChange={e => setConfig(c => ({ ...c, smtp_host: e.target.value }))}
                  placeholder="smtp.gmail.com"
                />
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="smtp_port" className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">
                  SMTP Port
                </Label>
                <Input
                  id="smtp_port"
                  type="number"
                  value={config.smtp_port}
                  onChange={e => setConfig(c => ({ ...c, smtp_port: parseInt(e.target.value) || 587 }))}
                />
              </div>
            </div>

            <div className="space-y-1.5">
              <Label htmlFor="from" className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">
                From Address
              </Label>
              <Input
                id="from"
                type="email"
                value={config.from}
                onChange={e => setConfig(c => ({ ...c, from: e.target.value }))}
                placeholder="backup@example.com"
              />
            </div>

            <div className="space-y-1.5">
              <Label htmlFor="to" className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">
                To Addresses <span className="normal-case font-normal">(comma-separated)</span>
              </Label>
              <Input
                id="to"
                value={toStr}
                onChange={e => setToStr(e.target.value)}
                placeholder="admin@example.com, ops@example.com"
              />
            </div>

            <div className="grid grid-cols-2 gap-4">
              <div className="space-y-1.5">
                <Label htmlFor="username" className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">
                  SMTP Username
                </Label>
                <Input
                  id="username"
                  value={config.username ?? ''}
                  onChange={e => setConfig(c => ({ ...c, username: e.target.value }))}
                  placeholder="optional"
                />
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="password" className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">
                  SMTP Password
                </Label>
                <Input
                  id="password"
                  type="password"
                  value={config.password ?? ''}
                  onChange={e => setConfig(c => ({ ...c, password: e.target.value }))}
                  placeholder="optional"
                />
              </div>
            </div>

            <div className="flex items-center gap-3">
              <Switch
                checked={config.tls}
                onCheckedChange={v => setConfig(c => ({ ...c, tls: v }))}
              />
              <Label className="text-sm">Use TLS</Label>
            </div>

            <div className="flex items-center gap-4 pt-1">
              <Button type="submit" disabled={saveMut.isPending} size="sm">
                {saveMut.isPending ? 'Saving…' : 'Save Settings'}
              </Button>
              {saveStatus === 'saved' && (
                <span className="text-xs text-emerald-600 dark:text-emerald-400">Saved successfully</span>
              )}
              {saveStatus === 'error' && (
                <span className="text-xs text-destructive">{errorMsg || 'Save failed'}</span>
              )}
            </div>
          </form>
        </CardContent>
      </Card>
    </div>
  )
}
