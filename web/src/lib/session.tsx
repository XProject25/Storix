// Session context: who is signed in, what they may do and where they may go.
// Developed by X Project.

import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from 'react'
import { ApiError, api, setCSRF } from './api'
import type { Me, Permission, User } from './types'
import { applyTheme, useApp } from '../state/app'

interface SessionValue {
  ready: boolean
  me: Me | null
  user: User | null
  setupRequired: boolean
  version: string
  can: (permission: Permission) => boolean
  isAdmin: boolean
  refresh: () => Promise<void>
  signIn: (me: Me) => void
  signOut: () => Promise<void>
}

const SessionContext = createContext<SessionValue | null>(null)

export function SessionProvider({ children }: { children: ReactNode }) {
  const [me, setMe] = useState<Me | null>(null)
  const [ready, setReady] = useState(false)
  const [setupRequired, setSetupRequired] = useState(false)
  const [version, setVersion] = useState('')
  const setTheme = useApp((state) => state.setTheme)

  const load = useCallback(async () => {
    try {
      const status = await api.status()
      setVersion(status.version)
      setSetupRequired(status.setupRequired)
      if (status.setupRequired) {
        setMe(null)
        return
      }
    } catch {
      // The status probe is best effort; the session probe below decides.
    }
    try {
      const session = await api.me()
      setCSRF(session.csrf)
      setMe(session)
      if (session.user.theme === 'light' || session.user.theme === 'dark') {
        setTheme(session.user.theme)
      }
    } catch (error) {
      if (error instanceof ApiError && error.is('setup_required')) setSetupRequired(true)
      setMe(null)
    }
  }, [setTheme])

  useEffect(() => {
    applyTheme(useApp.getState().theme)
    void load().finally(() => setReady(true))
  }, [load])

  const value = useMemo<SessionValue>(
    () => ({
      ready,
      me,
      user: me?.user ?? null,
      setupRequired,
      version,
      isAdmin: me?.user.role === 'admin',
      can: (permission) => {
        if (!me) return false
        if (me.user.role === 'admin') return true
        return me.permissions.includes(permission)
      },
      refresh: load,
      signIn: (session) => {
        setCSRF(session.csrf)
        setMe(session)
        setSetupRequired(false)
      },
      signOut: async () => {
        try {
          await api.logout()
        } finally {
          setMe(null)
        }
      },
    }),
    [ready, me, setupRequired, version, load],
  )

  return <SessionContext.Provider value={value}>{children}</SessionContext.Provider>
}

export function useSession(): SessionValue {
  const context = useContext(SessionContext)
  if (!context) throw new Error('useSession must be used inside SessionProvider')
  return context
}
