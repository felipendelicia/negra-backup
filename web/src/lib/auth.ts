import { createContext, useContext } from 'react'

export const TOKEN_KEY = 'nat_backup_token'

export interface AuthContextValue {
  token: string | null
  setToken: (t: string | null) => void
  logout: () => void
}

// The context lives here rather than next to the provider so `auth-context.tsx`
// exports a component and nothing else (keeps fast refresh working).
export const AuthContext = createContext<AuthContextValue | null>(null)

export function useAuth(): AuthContextValue {
  const ctx = useContext(AuthContext)
  if (!ctx) throw new Error('useAuth must be used inside AuthProvider')
  return ctx
}

export function getToken(): string | null {
  return localStorage.getItem(TOKEN_KEY)
}

/**
 * Drops the persisted session. Lives outside the provider so code that isn't a
 * component — the API client on a 401 — can clear it too, and so the storage
 * key is written in exactly one place.
 */
export function clearToken(): void {
  localStorage.removeItem(TOKEN_KEY)
}
