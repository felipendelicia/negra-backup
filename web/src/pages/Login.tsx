import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { useAuth } from 'src/lib/auth'
import { api } from 'src/lib/api'
import { Button } from 'src/components/ui/button'
import { Input } from 'src/components/ui/input'
import { Label } from 'src/components/ui/label'

export default function Login() {
  const [username, setUsername] = useState('admin')
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(false)
  const { setToken } = useAuth()
  const navigate = useNavigate()

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    setError('')
    setLoading(true)
    try {
      const { token } = await api.login(username, password)
      setToken(token)
      navigate('/dashboard')
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : 'Login failed')
    } finally {
      setLoading(false)
    }
  }

  return (
    <div className="relative min-h-screen flex items-center justify-center bg-background overflow-hidden">
      {/* Dot-grid background */}
      <div className="absolute inset-0 dot-grid" />

      {/* Gradient vignette */}
      <div className="absolute inset-0 bg-[radial-gradient(ellipse_80%_80%_at_50%_50%,transparent_40%,var(--background)_100%)]" />

      <div className="relative z-10 w-full max-w-sm px-4">
        {/* Brand */}
        <div className="mb-10 text-center select-none">
          <h1 className="font-sans font-black text-4xl uppercase tracking-tight text-foreground leading-none">
            Negra Backup
          </h1>
          <p className="text-xs text-muted-foreground tracking-[0.2em] uppercase mt-2">
            Self-hosted backup manager
          </p>
        </div>

        {/* Card */}
        <div className="bg-card border-2 border-border rounded-lg p-6 shadow-sm">
          <form onSubmit={handleSubmit} className="space-y-4">
            <div className="space-y-1.5">
              <Label htmlFor="username" className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">
                Username
              </Label>
              <Input
                id="username"
                value={username}
                onChange={e => setUsername(e.target.value)}
                autoComplete="username"
                autoFocus
                className="border-2 focus-visible:ring-0 focus-visible:border-foreground transition-colors"
              />
            </div>

            <div className="space-y-1.5">
              <Label htmlFor="password" className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">
                Password
              </Label>
              <Input
                id="password"
                type="password"
                value={password}
                onChange={e => setPassword(e.target.value)}
                autoComplete="current-password"
                className="border-2 focus-visible:ring-0 focus-visible:border-foreground transition-colors"
              />
            </div>

            {error && (
              <p className="text-xs text-destructive bg-destructive/8 border border-destructive/20 rounded px-3 py-2">
                {error}
              </p>
            )}

            <Button
              type="submit"
              className="w-full font-semibold uppercase tracking-wider text-xs h-10"
              disabled={loading}
            >
              {loading ? 'Signing in…' : 'Sign in'}
            </Button>
          </form>
        </div>
      </div>
    </div>
  )
}
