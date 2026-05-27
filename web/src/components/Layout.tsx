import { Outlet, NavLink, useNavigate } from 'react-router-dom'
import { useAuth } from 'src/lib/auth'
import { Button } from 'src/components/ui/button'
import { ErrorBoundary } from 'src/components/ErrorBoundary'
import {
  LayoutDashboard, Server, Briefcase, History,
  Database, Settings, LogOut
} from 'lucide-react'
import { cn } from 'src/lib/utils'

const nav = [
  { to: '/dashboard', icon: LayoutDashboard, label: 'Dashboard' },
  { to: '/agents',    icon: Server,          label: 'Agents'    },
  { to: '/jobs',      icon: Briefcase,       label: 'Jobs'      },
  { to: '/runs',      icon: History,         label: 'Run History'},
  { to: '/storage',   icon: Database,        label: 'Storage'   },
  { to: '/settings',  icon: Settings,        label: 'Settings'  },
]

export default function Layout() {
  const { logout } = useAuth()
  const navigate = useNavigate()

  function handleLogout() {
    logout()
    navigate('/login')
  }

  return (
    <div className="flex h-screen bg-background">
      <aside className="w-56 border-r flex flex-col">
        <div className="p-4 border-b">
          <h1 className="font-bold text-lg">Negra Backup</h1>
        </div>
        <nav className="flex-1 p-2 space-y-1">
          {nav.map(({ to, icon: Icon, label }) => (
            <NavLink
              key={to}
              to={to}
              className={({ isActive }) =>
                cn(
                  'flex items-center gap-3 px-3 py-2 rounded-md text-sm transition-colors',
                  isActive
                    ? 'bg-primary text-primary-foreground'
                    : 'hover:bg-accent text-muted-foreground hover:text-foreground'
                )
              }
            >
              <Icon className="h-4 w-4" />
              {label}
            </NavLink>
          ))}
        </nav>
        <div className="p-2 border-t">
          <Button variant="ghost" size="sm" className="w-full justify-start gap-3" onClick={handleLogout}>
            <LogOut className="h-4 w-4" />
            Logout
          </Button>
        </div>
      </aside>
      <main className="flex-1 overflow-auto p-6">
        <ErrorBoundary>
          <Outlet />
        </ErrorBoundary>
      </main>
    </div>
  )
}
