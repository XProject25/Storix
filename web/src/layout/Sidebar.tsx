// The primary navigation rail: where you are, where you can go, and how much
// room is left on the volume.
//
// This file also holds the small pieces of shell state the three layout files
// share. It lives here because Sidebar imports neither AppLayout nor Topbar,
// so nothing in the shell ends up importing itself.
// Developed by X Project.

import { useEffect, useMemo, useState } from 'react'
import { Link, NavLink, useLocation } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { create } from 'zustand'
import clsx from 'clsx'
import { api } from '../lib/api'
import { baseName, bytes, percent } from '../lib/format'
import { useSession } from '../lib/session'
import { useApp } from '../state/app'
import { useTransfers } from '../state/transfers'
import { Icon, type IconName } from '../components/Icon'
import { Logo, LogoMark } from '../components/Logo'
import { Progress, SectionTitle, Skeleton } from '../components/ui'
import type { Permission } from '../lib/types'

// ---- shared shell state -----------------------------------------------------

interface DrawerState {
  open: boolean
  setOpen: (open: boolean) => void
  toggle: () => void
}

/**
 * useDrawer tracks the narrow screen navigation drawer. It is deliberately not
 * persisted: a drawer that reopens itself after a reload would cover the page.
 */
export const useDrawer = create<DrawerState>((set) => ({
  open: false,
  setOpen: (open) => set({ open }),
  toggle: () => set((state) => ({ open: !state.open })),
}))

/** useCompact reports whether the viewport is too narrow for a fixed sidebar. */
export function useCompact(): boolean {
  const [compact, setCompact] = useState(() => window.matchMedia('(max-width: 1023px)').matches)
  useEffect(() => {
    const media = window.matchMedia('(max-width: 1023px)')
    const update = () => setCompact(media.matches)
    update()
    media.addEventListener('change', update)
    return () => media.removeEventListener('change', update)
  }, [])
  return compact
}

/** filesLink builds the browser route for a folder, keeping odd names intact. */
export function filesLink(path: string): string {
  const clean = path.replace(/\/+$/, '')
  if (!clean) return '/files'
  return '/files' + clean.split('/').map(encodeURIComponent).join('/')
}

/**
 * routeFolder is the inverse: the folder the current route is showing, or an
 * empty string when the route is not a folder view.
 */
export function routeFolder(pathname: string): string {
  if (pathname !== '/files' && !pathname.startsWith('/files/')) return ''
  const rest = pathname.slice('/files'.length)
  if (!rest || rest === '/') return '/'
  return rest
    .split('/')
    .map((segment) => {
      try {
        return decodeURIComponent(segment)
      } catch {
        return segment
      }
    })
    .join('/')
}

// ---- navigation model -------------------------------------------------------

interface NavItem {
  to: string
  label: string
  icon: IconName
  end?: boolean
  permission?: Permission
}

const OVERVIEW: NavItem[] = [
  { to: '/', label: 'Dashboard', icon: 'home', end: true },
  { to: '/files', label: 'Files', icon: 'folder-open' },
  { to: '/recent', label: 'Recent', icon: 'clock' },
  { to: '/favorites', label: 'Favorites', icon: 'star' },
  { to: '/shares', label: 'Shares', icon: 'link' },
  { to: '/transfers', label: 'Transfers', icon: 'upload' },
  { to: '/trash', label: 'Trash', icon: 'trash' },
]

const SYSTEM: NavItem[] = [
  { to: '/users', label: 'Users', icon: 'users', permission: 'users' },
  { to: '/settings', label: 'Settings', icon: 'settings', permission: 'settings' },
]

function matches(pathname: string, to: string, end?: boolean): boolean {
  if (end) return pathname === to
  return pathname === to || pathname.startsWith(to + '/')
}

// ---- rows -------------------------------------------------------------------

function Badge({ value }: { value: number }) {
  return (
    <span className="ml-auto min-w-[20px] rounded-lg bg-primary/15 px-1.5 py-0.5 text-center text-[11px] font-medium leading-none text-primary">
      {value > 99 ? '99+' : value}
    </span>
  )
}

function NavRow({
  to,
  label,
  icon,
  end,
  active,
  rail,
  badge,
  readOnly,
  onNavigate,
}: {
  to: string
  label: string
  icon: IconName
  end?: boolean
  active: boolean
  rail: boolean
  badge?: number
  readOnly?: boolean
  onNavigate?: () => void
}) {
  const showBadge = typeof badge === 'number' && badge > 0
  return (
    <NavLink
      to={to}
      end={end}
      onClick={onNavigate}
      data-active={active ? 'true' : undefined}
      aria-label={rail ? label : undefined}
      title={rail ? label : undefined}
      className={clsx('sx-nav-item', rail ? 'relative justify-center px-0' : 'w-full')}
    >
      <Icon name={icon} size={18} className="shrink-0" />
      {rail ? (
        showBadge ? <span className="absolute right-2 top-2 h-2 w-2 rounded-full bg-primary" /> : null
      ) : (
        <>
          <span className="min-w-0 flex-1 truncate">{label}</span>
          {readOnly && <Icon name="lock" size={13} className="shrink-0 text-faint" />}
          {showBadge && <Badge value={badge} />}
        </>
      )}
    </NavLink>
  )
}

// ---- storage meter ----------------------------------------------------------

function StorageMeter({ path, rail }: { path: string | null; rail: boolean }) {
  const disk = useQuery({
    queryKey: ['disk', path ?? ''],
    queryFn: () => api.disk(path ?? '/'),
    enabled: Boolean(path),
    staleTime: 60_000,
  })

  if (!path) {
    if (rail) return null
    return (
      <div className="rounded-2xl border border-line bg-elevated/60 px-3 py-2.5 text-[11px] text-faint">
        No folders are connected yet
      </div>
    )
  }

  if (disk.isLoading) {
    return rail ? <Skeleton className="h-1.5 w-full" /> : <Skeleton className="h-[62px] w-full rounded-2xl" />
  }

  if (disk.isError || !disk.data) {
    if (rail) return null
    return (
      <div className="rounded-2xl border border-line bg-elevated/60 px-3 py-2.5 text-[11px] text-faint">
        Storage usage is unavailable
      </div>
    )
  }

  const usage = disk.data
  const label = `${bytes(usage.used)} of ${bytes(usage.total)} used`

  if (rail) {
    return (
      <div className="px-1" title={label}>
        <Progress value={usage.percent} />
        <div className="mt-1.5 text-center text-[10px] text-faint">{percent(usage.percent)}</div>
      </div>
    )
  }

  return (
    <div className="rounded-2xl border border-line bg-elevated/60 px-3 py-2.5">
      <div className="flex items-center justify-between">
        <span className="flex items-center gap-2 text-xs text-muted">
          <Icon name="drive" size={14} className="text-faint" />
          Storage
        </span>
        <span className="text-[11px] font-medium text-muted">{percent(usage.percent)}</span>
      </div>
      <Progress value={usage.percent} className="mt-2" />
      <div className="mt-2 text-[11px] text-faint">
        {bytes(usage.used)} of {bytes(usage.total)}
      </div>
    </div>
  )
}

// ---- sidebar ----------------------------------------------------------------

export default function Sidebar({ onNavigate }: { onNavigate?: () => void }) {
  const location = useLocation()
  const { me, can, version } = useSession()
  const sidebarOpen = useApp((state) => state.sidebarOpen)
  const compact = useCompact()
  const drawerOpen = useDrawer((state) => state.open)
  const setDrawer = useDrawer((state) => state.setOpen)
  const items = useTransfers((state) => state.items)

  const rail = !compact && !sidebarOpen
  const mounts = useMemo(() => me?.mounts ?? [], [me])
  const pathname = location.pathname

  // The drawer never survives a navigation or a return to the wide layout.
  useEffect(() => {
    setDrawer(false)
  }, [pathname, compact, setDrawer])

  useEffect(() => {
    if (!compact || !drawerOpen) return
    const onKey = (event: KeyboardEvent) => {
      if (event.key === 'Escape') setDrawer(false)
    }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [compact, drawerOpen, setDrawer])

  const inFlight = items.filter(
    (item) => item.status === 'uploading' || item.status === 'queued' || item.status === 'paused',
  ).length

  const trash = useQuery({
    queryKey: ['trash'],
    queryFn: () => api.trash(),
    staleTime: 60_000,
  })
  const trashCount = trash.data?.count ?? 0

  const currentFolder = routeFolder(pathname)
  const activeMount = useMemo(() => {
    if (!currentFolder) return ''
    let best = ''
    for (const mount of mounts) {
      const root = mount.path.replace(/\/+$/, '')
      const inside = currentFolder === mount.path || currentFolder.startsWith(root + '/') || root === ''
      if (inside && mount.path.length >= best.length) best = mount.path
    }
    return best
  }, [currentFolder, mounts])

  const systemItems = SYSTEM.filter((item) => (item.permission ? can(item.permission) : true))
  const firstMount = mounts.length > 0 ? mounts[0] : null

  const body = (
    <div
      className={clsx(
        'flex h-full flex-col border-r border-line bg-surface transition-[width] duration-200',
        rail ? 'w-[68px] px-2' : 'w-[260px] px-3',
      )}
    >
      <div className={clsx('flex h-[60px] shrink-0 items-center', rail && 'justify-center')}>
        <Link to="/" onClick={onNavigate} aria-label="Storix home" className="rounded-xl">
          {rail ? <LogoMark size={28} /> : <Logo size={28} />}
        </Link>
      </div>

      <nav className="sx-scroll -mx-1 min-h-0 flex-1 px-1 pb-2">
        <div className={clsx(rail ? 'mb-1' : 'mb-1 mt-1')}>
          {rail ? <div className="sx-divider mx-2" /> : <SectionTitle>Overview</SectionTitle>}
          <div className="space-y-0.5">
            {OVERVIEW.map((item) => (
              <NavRow
                key={item.to}
                to={item.to}
                label={item.label}
                icon={item.icon}
                end={item.end}
                rail={rail}
                active={matches(pathname, item.to, item.end)}
                badge={item.to === '/transfers' ? inFlight : item.to === '/trash' ? trashCount : undefined}
                onNavigate={onNavigate}
              />
            ))}
          </div>
        </div>

        <div className="mt-5">
          {rail ? <div className="sx-divider mx-2" /> : <SectionTitle>Storage</SectionTitle>}
          <div className="space-y-0.5">
            {mounts.map((mount) => (
              <NavRow
                key={mount.path}
                to={filesLink(mount.path)}
                label={mount.label || baseName(mount.path) || mount.path}
                icon="folder"
                rail={rail}
                readOnly={mount.readOnly}
                active={activeMount === mount.path}
                onNavigate={onNavigate}
              />
            ))}
            {mounts.length === 0 && !rail && (
              <p className="px-3 py-1 text-xs text-faint">
                {can('settings')
                  ? 'No folders yet. Add one in Settings.'
                  : 'No folders have been shared with you yet.'}
              </p>
            )}
          </div>
        </div>

        {systemItems.length > 0 && (
          <div className="mt-5">
            {rail ? <div className="sx-divider mx-2" /> : <SectionTitle>System</SectionTitle>}
            <div className="space-y-0.5">
              {systemItems.map((item) => (
                <NavRow
                  key={item.to}
                  to={item.to}
                  label={item.label}
                  icon={item.icon}
                  rail={rail}
                  active={matches(pathname, item.to)}
                  onNavigate={onNavigate}
                />
              ))}
            </div>
          </div>
        )}
      </nav>

      <div className="shrink-0 border-t border-line pb-3 pt-3">
        <StorageMeter path={firstMount ? firstMount.path : null} rail={rail} />
        {rail ? (
          <div className="mt-3 text-center text-[10px] text-faint" title={`Storix ${version}. Developed by X Project.`}>
            {version || 'Storix'}
          </div>
        ) : (
          <div className="mt-3 px-1">
            <div className="text-xs text-muted">{version ? `Storix ${version}` : 'Storix'}</div>
            <div className="mt-0.5 text-[11px] text-faint">Developed by X Project</div>
          </div>
        )}
      </div>
    </div>
  )

  if (compact) {
    if (!drawerOpen) return null
    return (
      <>
        <div
          className="fixed inset-0 z-40 bg-black/55 backdrop-blur-[2px] animate-fade-in"
          onClick={() => setDrawer(false)}
          aria-hidden="true"
        />
        <aside className="fixed inset-y-0 left-0 z-50 animate-slide-in shadow-pop">{body}</aside>
      </>
    )
  }

  return <aside className="shrink-0">{body}</aside>
}
