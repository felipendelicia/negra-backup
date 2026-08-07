import { useState, useMemo } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { useTheme } from 'next-themes'
import { api } from 'src/lib/api'
import { Button } from 'src/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from 'src/components/ui/card'
import { Input } from 'src/components/ui/input'
import { Label } from 'src/components/ui/label'
import { Switch } from 'src/components/ui/switch'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from 'src/components/ui/select'
import { Sun, Moon, Monitor } from 'lucide-react'
import { cn } from 'src/lib/utils'
import {
  STORED_SECRET_SENTINEL, WEBHOOK_FORMATS,
  type EmailNotificationConfig, type WebhookNotificationConfig,
  type NotificationSettings, type WebhookFormat,
} from 'src/lib/types'

const defaultEmail = (): EmailNotificationConfig => ({
  smtp_host: '',
  smtp_port: 587,
  from: '',
  to: [],
  tls: true,
  username: '',
  password: '',
})

const defaultWebhook = (): WebhookNotificationConfig => ({
  url: '',
  format: 'generic',
  auth_header: '',
  chat_id: '',
})

const THEMES = [
  { value: 'light',  label: 'Light',  icon: Sun     },
  { value: 'dark',   label: 'Dark',   icon: Moon    },
  { value: 'system', label: 'System', icon: Monitor },
] as const

const FORMAT_LABELS: Record<WebhookFormat, string> = {
  generic:  'Generic JSON',
  slack:    'Slack',
  discord:  'Discord',
  telegram: 'Telegram',
  ntfy:     'ntfy',
}

const FORMAT_HINTS: Record<WebhookFormat, string> = {
  generic:  'POSTs {source, subject, body} as JSON.',
  slack:    'Paste an Incoming Webhook URL from your Slack app.',
  discord:  'Paste a channel Webhook URL from Integrations.',
  telegram: 'URL: https://api.telegram.org/bot<token>/sendMessage — the chat ID is separate.',
  ntfy:     'URL: https://ntfy.sh/<your-topic>. The subject becomes the notification title.',
}

export default function Settings() {
  const { theme: currentTheme, setTheme } = useTheme()
  const queryClient = useQueryClient()

  // `null` means "untouched" — the field then mirrors whatever the server
  // returned. Deriving instead of copying into state on load avoids a
  // render-cascade effect and keeps the form in sync with a refetch.
  const [emailDraft, setEmailDraft] = useState<EmailNotificationConfig | null>(null)
  const [webhookDraft, setWebhookDraft] = useState<WebhookNotificationConfig | null>(null)
  const [toDraft, setToDraft] = useState<string | null>(null)
  const [saveStatus, setSaveStatus] = useState<'idle' | 'saved' | 'error'>('idle')
  const [errorMsg, setErrorMsg] = useState('')

  const { data: settings, isError } = useQuery({
    queryKey: ['notification-settings'],
    queryFn: ({ signal }) => api.getNotificationSettings(signal),
  })

  const loadedEmail = useMemo<EmailNotificationConfig>(
    () => ({ ...defaultEmail(), ...(settings?.email ?? {}) }),
    [settings],
  )
  const loadedWebhook = useMemo<WebhookNotificationConfig>(
    () => ({ ...defaultWebhook(), ...(settings?.webhook ?? {}) }),
    [settings],
  )

  const email = emailDraft ?? loadedEmail
  const webhook = webhookDraft ?? loadedWebhook
  const toStr = toDraft ?? loadedEmail.to.join(', ')

  // The server returns the sentinel instead of the stored URL and auth header,
  // because for Slack, Discord and Telegram the URL is the credential. The inputs
  // therefore start blank with the preview as a placeholder, and a blank field on
  // submit means "keep what is stored".
  const hasStoredURL = settings?.webhook?.url === STORED_SECRET_SENTINEL
  const hasStoredAuth = settings?.webhook?.auth_header === STORED_SECRET_SENTINEL
  const urlInput = webhook.url === STORED_SECRET_SENTINEL ? '' : webhook.url
  const authInput = webhook.auth_header === STORED_SECRET_SENTINEL ? '' : (webhook.auth_header ?? '')

  function patchEmail(patch: Partial<EmailNotificationConfig>) {
    setEmailDraft({ ...email, ...patch })
  }
  function patchWebhook(patch: Partial<WebhookNotificationConfig>) {
    setWebhookDraft({ ...webhook, ...patch })
  }

  const saveMut = useMutation({
    mutationFn: (data: NotificationSettings) => api.updateNotificationSettings(data),
    onSuccess: () => {
      setSaveStatus('saved')
      setTimeout(() => setSaveStatus('idle'), 3000)
      // Refetch so the freshly stored secrets come back as sentinels and the
      // previews reflect what was actually saved.
      setEmailDraft(null)
      setWebhookDraft(null)
      setToDraft(null)
      queryClient.invalidateQueries({ queryKey: ['notification-settings'] })
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

    // A blank primary field means "this channel is off"; the server deletes it.
    const emailPayload: EmailNotificationConfig | null =
      email.smtp_host.trim() === '' ? null : { ...email, to }

    const url = urlInput.trim() === '' && hasStoredURL ? STORED_SECRET_SENTINEL : urlInput.trim()
    const authHeader = authInput.trim() === '' && hasStoredAuth ? STORED_SECRET_SENTINEL : authInput.trim()
    const webhookPayload: WebhookNotificationConfig | null =
      url === '' ? null : {
        url,
        format: webhook.format,
        auth_header: authHeader || undefined,
        chat_id: webhook.format === 'telegram' ? (webhook.chat_id ?? '') : undefined,
      }

    saveMut.mutate({ email: emailPayload, webhook: webhookPayload })
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
                  type="button"
                  onClick={() => setTheme(value)}
                  aria-pressed={currentTheme === value}
                  className={cn(
                    'flex-1 flex flex-col items-center gap-2 p-4 rounded-lg border-2 transition-all text-sm',
                    currentTheme === value
                      ? 'border-foreground bg-accent text-foreground font-medium'
                      : 'border-border text-muted-foreground hover:border-foreground/30 hover:text-foreground'
                  )}
                >
                  <Icon aria-hidden="true" className="h-5 w-5" />
                  {label}
                </button>
              ))}
            </div>
          </div>
        </CardContent>
      </Card>

      {/* Both channels live in one form: the API replaces the whole set on save,
          so submitting them separately would delete the other one. */}
      <form onSubmit={handleSubmit} className="space-y-6">
        {/* ── Email Notifications ───────────────────────────────────────── */}
        <Card>
          <CardHeader className="pb-2">
            <CardTitle className="text-sm font-semibold uppercase tracking-wider text-muted-foreground">
              Email Notifications
            </CardTitle>
          </CardHeader>
          <CardContent>
            {isError && <p className="text-sm text-destructive mb-4">Failed to load settings.</p>}
            <div className="space-y-4 max-w-lg">
              <p className="text-sm text-muted-foreground">
                Leave the SMTP host blank to turn email alerts off.
              </p>
              <div className="grid grid-cols-2 gap-4">
                <div className="space-y-1.5">
                  <Label htmlFor="smtp_host" className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">
                    SMTP Host
                  </Label>
                  <Input
                    id="smtp_host"
                    value={email.smtp_host}
                    onChange={e => patchEmail({ smtp_host: e.target.value })}
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
                    value={email.smtp_port}
                    onChange={e => patchEmail({ smtp_port: parseInt(e.target.value) || 587 })}
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
                  value={email.from}
                  onChange={e => patchEmail({ from: e.target.value })}
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
                  onChange={e => setToDraft(e.target.value)}
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
                    value={email.username ?? ''}
                    onChange={e => patchEmail({ username: e.target.value })}
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
                    value={email.password ?? ''}
                    onChange={e => patchEmail({ password: e.target.value })}
                    placeholder="optional"
                  />
                </div>
              </div>

              <div className="flex items-center gap-3">
                <Switch
                  id="tls"
                  checked={email.tls}
                  onCheckedChange={v => patchEmail({ tls: v })}
                />
                <Label htmlFor="tls" className="text-sm">Use TLS</Label>
              </div>
            </div>
          </CardContent>
        </Card>

        {/* ── Webhook Notifications ─────────────────────────────────────── */}
        <Card>
          <CardHeader className="pb-2">
            <CardTitle className="text-sm font-semibold uppercase tracking-wider text-muted-foreground">
              Webhook Notifications
            </CardTitle>
          </CardHeader>
          <CardContent>
            <div className="space-y-4 max-w-lg">
              <p className="text-sm text-muted-foreground">
                Alerts are delivered to every configured channel. Leave the URL blank to
                turn webhooks off.
              </p>

              <div className="space-y-1.5">
                <Label htmlFor="webhook_format" className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">
                  Service
                </Label>
                <Select
                  value={webhook.format}
                  onValueChange={v => patchWebhook({ format: v as WebhookFormat })}
                >
                  <SelectTrigger id="webhook_format">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {WEBHOOK_FORMATS.map(f => (
                      <SelectItem key={f} value={f}>{FORMAT_LABELS[f]}</SelectItem>
                    ))}
                  </SelectContent>
                </Select>
                <p className="text-xs text-muted-foreground">{FORMAT_HINTS[webhook.format]}</p>
              </div>

              <div className="space-y-1.5">
                <Label htmlFor="webhook_url" className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">
                  Webhook URL
                </Label>
                <Input
                  id="webhook_url"
                  type="url"
                  value={urlInput}
                  onChange={e => patchWebhook({ url: e.target.value })}
                  placeholder={hasStoredURL ? 'leave blank to keep the stored URL' : 'https://hooks.slack.com/services/…'}
                />
                {hasStoredURL && (
                  <p className="text-xs text-muted-foreground">
                    Stored: <span className="font-mono">{settings?.webhook?.url_preview}</span> — the
                    full URL is a credential and is never sent back to the browser.
                  </p>
                )}
              </div>

              {webhook.format === 'telegram' && (
                <div className="space-y-1.5">
                  <Label htmlFor="webhook_chat_id" className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">
                    Chat ID
                  </Label>
                  <Input
                    id="webhook_chat_id"
                    value={webhook.chat_id ?? ''}
                    onChange={e => patchWebhook({ chat_id: e.target.value })}
                    placeholder="-1001234567890"
                  />
                </div>
              )}

              <div className="space-y-1.5">
                <Label htmlFor="webhook_auth" className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">
                  Authorization Header
                </Label>
                <Input
                  id="webhook_auth"
                  type="password"
                  value={authInput}
                  onChange={e => patchWebhook({ auth_header: e.target.value })}
                  placeholder={hasStoredAuth ? 'leave blank to keep the stored value' : 'optional — e.g. Bearer tk_…'}
                />
              </div>
            </div>
          </CardContent>
        </Card>

        <div className="flex items-center gap-4" role="status">
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
    </div>
  )
}
