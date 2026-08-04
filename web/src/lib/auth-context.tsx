import { createContext, useContext, useState, useCallback, type ReactNode } from 'react'

export const TOKEN_KEY = 'nat_backup_token'

interface AuthContextValue {
  token: string | null
  setToken: (t: string | null) => void
  logout: () => void
}

const AuthContext = createContext<AuthContextValue | null>(null)

export function AuthProvider({ children }: { children: ReactNode }) {
  const [token, setTokenState] = useState<string | null>(
    () => localStorage.getItem(TOKEN_KEY)
  )

  const setToken = useCallback((t: string | null) => {
    if (t) {
      localStorage.setItem(TOKEN_KEY, t)
    } else {
      localStorage.removeItem(TOKEN_KEY)
    }
    setTokenState(t)
  }, [])

  const logout = useCallback(() => setToken(null), [setToken])

  return (
    <AuthContext.Provider value={{ token, setToken, logout }}>
      {children}
    </AuthContext.Provider>
  )
}

export function useAuth() {
  const ctx = useContext(AuthContext)
  if (!ctx) throw new Error('useAuth must be used inside AuthProvider')
  return ctx
}

export function getToken(): string | null {
  return localStorage.getItem(TOKEN_KEY)
}

/**
 * Drops the persisted session. Callable from outside React (e.g. the API
 * client on a 401) so the token key lives in exactly one place.
 */
export function clearToken(): void {
  localStorage.removeItem(TOKEN_KEY)
}
