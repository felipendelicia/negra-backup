import { Outlet, NavLink, useNavigate } from 'react-router-dom'
import { useAuth } from 'src/lib/auth'
import { useTheme } from 'next-themes'
import { Button } from 'src/components/ui/button'
import { ErrorBoundary } from 'src/components/ErrorBoundary'
import {
  LayoutDashboard, Server, Briefcase, History,
  Database, Settings, LogOut, Moon, Sun, Terminal,
} from 'lucide-react'
import { cn } from 'src/lib/utils'

const nav = [
  { to: '/dashboard', icon: LayoutDashboard, label: 'Dashboard' },
  { to: '/agents',    icon: Server,          label: 'Agents'    },
  { to: '/jobs',      icon: Briefcase,       label: 'Jobs'      },
  { to: '/runs',      icon: History,         label: 'Runs'      },
  { to: '/storage',   icon: Database,        label: 'Storage'   },
  { to: '/console',   icon: Terminal,        label: 'Console'   },
  { to: '/settings',  icon: Settings,        label: 'Settings'  },
]

export default function Layout() {
  const { logout } = useAuth()
  const navigate = useNavigate()
  const { resolvedTheme, setTheme } = useTheme()

  function handleLogout() {
    logout()
    navigate('/login')
  }

  function toggleTheme() {
    setTheme(resolvedTheme === 'dark' ? 'light' : 'dark')
  }

  return (
    <div className="flex h-screen bg-background">
      {/* ── Sidebar ───────────────────────────────────────────── */}
      <aside className="w-52 border-r bg-sidebar flex flex-col shrink-0">

        {/* Logo */}
        <div className="px-4 py-4 border-b">
          <p className="font-sans font-bold text-sm uppercase tracking-widest leading-none text-foreground">Negra Backup</p>
        </div>

        {/* Nav */}
        <nav className="flex-1 px-2 py-3 space-y-0.5">
          {nav.map(({ to, icon: Icon, label }) => (
            <NavLink
              key={to}
              to={to}
              className={({ isActive }) =>
                cn(
                  'group relative flex items-center gap-2.5 px-3 py-2 rounded text-sm transition-all',
                  isActive
                    ? 'bg-foreground text-background font-medium'
                    : 'text-muted-foreground hover:text-foreground hover:bg-accent'
                )
              }
            >
              <Icon className="h-4 w-4 shrink-0" />
              {label}
            </NavLink>
          ))}
        </nav>

        {/* Footer */}
        <div className="px-2 py-3 border-t flex items-center gap-1">
          <Button
            variant="ghost"
            size="sm"
            className="flex-1 justify-start gap-2.5 text-muted-foreground hover:text-foreground text-sm"
            onClick={handleLogout}
          >
            <LogOut className="h-4 w-4 shrink-0" />
            Logout
          </Button>
          <Button
            variant="ghost"
            size="icon"
            className="h-8 w-8 text-muted-foreground hover:text-foreground shrink-0"
            onClick={toggleTheme}
            title={resolvedTheme === 'dark' ? 'Switch to light mode' : 'Switch to dark mode'}
          >
            {resolvedTheme === 'dark'
              ? <Sun className="h-4 w-4" />
              : <Moon className="h-4 w-4" />
            }
          </Button>
        </div>
      </aside>

      {/* ── Main content ──────────────────────────────────────── */}
      <main className="flex-1 overflow-auto">
        <div className="p-6 page-enter">
          <ErrorBoundary>
            <Outlet />
          </ErrorBoundary>
        </div>
      </main>
    </div>
  )
}
