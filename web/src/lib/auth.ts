import { useState, useCallback } from 'react'

const TOKEN_KEY = 'nat_backup_token'

export function useAuth() {
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

  return { token, setToken, logout }
}

export function getToken(): string | null {
  return localStorage.getItem(TOKEN_KEY)
}
