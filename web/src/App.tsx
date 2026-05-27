import { BrowserRouter, Routes, Route, Navigate } from 'react-router-dom'
import { useAuth } from 'src/lib/auth'
import Layout from 'src/components/Layout'
import Login from 'src/pages/Login'
import Dashboard from 'src/pages/Dashboard'
import Agents from 'src/pages/Agents'
import Jobs from 'src/pages/Jobs'
import Runs from 'src/pages/Runs'
import Storage from 'src/pages/Storage'
import Settings from 'src/pages/Settings'

function ProtectedRoute({ children }: { children: React.ReactNode }) {
  const { token } = useAuth()
  if (!token) return <Navigate to="/login" replace />
  return <>{children}</>
}

export default function App() {
  return (
    <BrowserRouter>
      <Routes>
        <Route path="/login" element={<Login />} />
        <Route
          path="/"
          element={
            <ProtectedRoute>
              <Layout />
            </ProtectedRoute>
          }
        >
          <Route index element={<Navigate to="/dashboard" replace />} />
          <Route path="dashboard" element={<Dashboard />} />
          <Route path="agents" element={<Agents />} />
          <Route path="jobs" element={<Jobs />} />
          <Route path="runs" element={<Runs />} />
          <Route path="storage" element={<Storage />} />
          <Route path="settings" element={<Settings />} />
        </Route>
      </Routes>
    </BrowserRouter>
  )
}
