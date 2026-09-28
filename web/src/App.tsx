import { lazy, Suspense } from 'react'
import type { ReactNode } from 'react'
import { Navigate, Route, Routes, useLocation } from 'react-router-dom'
import { Header } from './components/Header.tsx'
import { Home } from './pages/Home.tsx'
import { Login } from './pages/Login.tsx'
import { Users } from './pages/Users.tsx'
import { NodeDetail } from './pages/NodeDetail.tsx'
import { Workspace } from './pages/Workspace.tsx'
import { Usage } from './pages/Usage.tsx'
import { ErrorState } from './components/ErrorState.tsx'
import { LoadingSkeleton } from './components/LoadingSkeleton.tsx'
import { AntdProvider } from './providers/AntdProvider.tsx'
import { RefineProvider } from './providers/RefineProvider.tsx'
import { getStoredToken } from './apiClient.ts'
import { useAuth } from './hooks/useAuth.ts'
import { useTaskNotifications } from './hooks/useTaskNotifications.ts'
import './App.css'

// The session stream view is the heaviest route (streaming, message bubbles,
// permission picker); code-split it so board browsing ships less upfront.
const Session = lazy(() => import('./pages/Session.tsx').then((m) => ({ default: m.Session })))

/**
 * Guards the app shell. No token → /login. A token whose `me` check fails with
 * 401 is cleared and redirected by `apiClient` itself (the redirect lands on
 * /login and cannot loop); other failures (hub down) surface a retry state
 * instead of silently signing the user out.
 */
function RequireAuth({ children }: { children: ReactNode }) {
  const { user, isLoading, isError, me } = useAuth()
  if (!getStoredToken()) return <Navigate to="/login" replace />
  if (isLoading) return <LoadingSkeleton count={3} />
  if (isError) {
    return (
      <ErrorState title="Cannot reach the hub" onRetry={() => void me.refetch()}>
        Your session could not be verified. Try again in a moment.
      </ErrorState>
    )
  }
  if (!user) return <Navigate to="/login" replace />
  return <>{children}</>
}

/** Admin-only routes (user management); everyone else goes home. */
function RequireAdmin({ children }: { children: ReactNode }) {
  const { isAdmin } = useAuth()
  if (!isAdmin) return <Navigate to="/" replace />
  return <>{children}</>
}

/*
 * Mounts the task-notification bridge inside the provider tree: subscribes to
 * task WS events and raises browser notifications (permission + user setting
 * gated, see useTaskNotifications).
 */
function TaskNotificationBridge() {
  useTaskNotifications()
  return null
}

function App() {
  const location = useLocation()
  // The login page is a bare full-screen card — no app chrome above it.
  const showChrome = location.pathname !== '/login'
  return (
    <AntdProvider>
      <RefineProvider>
        {showChrome && <Header />}
        <TaskNotificationBridge />
        <main className="appMain">
          <Routes>
            <Route path="/login" element={<Login />} />
            <Route
              path="/"
              element={
                <RequireAuth>
                  <Home />
                </RequireAuth>
              }
            />
            <Route
              path="/nodes/:nodeId"
              element={
                <RequireAuth>
                  <NodeDetail />
                </RequireAuth>
              }
            />
            <Route
              path="/workspaces/:workspaceId"
              element={
                <RequireAuth>
                  <Workspace />
                </RequireAuth>
              }
            />
            <Route
              path="/usage"
              element={
                <RequireAuth>
                  <Usage />
                </RequireAuth>
              }
            />
            <Route
              path="/sessions/:sessionId"
              element={
                <RequireAuth>
                  <Suspense fallback={<LoadingSkeleton count={2} />}>
                    <Session />
                  </Suspense>
                </RequireAuth>
              }
            />
            <Route
              path="/users"
              element={
                <RequireAuth>
                  <RequireAdmin>
                    <Users />
                  </RequireAdmin>
                </RequireAuth>
              }
            />
          </Routes>
        </main>
      </RefineProvider>
    </AntdProvider>
  )
}

export default App
