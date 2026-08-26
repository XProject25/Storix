// Route table for the Storix interface.
// Developed by X Project.

import { Suspense, lazy } from 'react'
import { Navigate, Route, Routes, useLocation } from 'react-router-dom'
import { useSession } from './lib/session'
import { Spinner } from './components/ui'
import { Logo } from './components/Logo'
import AppLayout from './layout/AppLayout'

const DashboardPage = lazy(() => import('./pages/DashboardPage'))
const FilesPage = lazy(() => import('./pages/FilesPage'))
const RecentPage = lazy(() => import('./pages/RecentPage'))
const FavoritesPage = lazy(() => import('./pages/FavoritesPage'))
const SharesPage = lazy(() => import('./pages/SharesPage'))
const TransfersPage = lazy(() => import('./pages/TransfersPage'))
const TrashPage = lazy(() => import('./pages/TrashPage'))
const UsersPage = lazy(() => import('./pages/UsersPage'))
const SettingsPage = lazy(() => import('./pages/SettingsPage'))
const LoginPage = lazy(() => import('./pages/LoginPage'))
const SetupPage = lazy(() => import('./pages/SetupPage'))
const PublicSharePage = lazy(() => import('./pages/PublicSharePage'))

/** Splash covers the first paint while the session is resolved. */
function Splash() {
  return (
    <div className="flex h-full w-full flex-col items-center justify-center gap-5 bg-bg">
      <Logo size={44} />
      <Spinner size={20} className="text-primary" />
    </div>
  )
}

function Protected({ children }: { children: React.ReactNode }) {
  const { ready, user, setupRequired } = useSession()
  const location = useLocation()
  if (!ready) return <Splash />
  if (setupRequired) return <Navigate to="/setup" replace />
  if (!user) return <Navigate to="/login" replace state={{ from: location.pathname + location.search }} />
  return <>{children}</>
}

export default function App() {
  const { ready, setupRequired, user } = useSession()

  return (
    <Suspense fallback={<Splash />}>
      <Routes>
        <Route path="/s/:token" element={<PublicSharePage />} />
        <Route
          path="/setup"
          element={ready && !setupRequired ? <Navigate to="/" replace /> : <SetupPage />}
        />
        <Route
          path="/login"
          element={ready && user ? <Navigate to="/" replace /> : ready && setupRequired ? <Navigate to="/setup" replace /> : <LoginPage />}
        />
        <Route
          path="/"
          element={
            <Protected>
              <AppLayout />
            </Protected>
          }
        >
          <Route index element={<DashboardPage />} />
          <Route path="files/*" element={<FilesPage />} />
          <Route path="recent" element={<RecentPage />} />
          <Route path="favorites" element={<FavoritesPage />} />
          <Route path="shares" element={<SharesPage />} />
          <Route path="transfers" element={<TransfersPage />} />
          <Route path="trash" element={<TrashPage />} />
          <Route path="users" element={<UsersPage />} />
          <Route path="settings" element={<SettingsPage />} />
          <Route path="*" element={<Navigate to="/" replace />} />
        </Route>
      </Routes>
    </Suspense>
  )
}
