// The bar across the top of every screen: search, the New menu, theme,
// notifications and the account menu.
// Developed by X Project.

import { useCallback, useEffect, useRef, useState, type FormEvent, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import { useLocation, useNavigate } from 'react-router-dom'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { api, subscribe } from '../lib/api'
import { ago, counted, initials, truncateMiddle } from '../lib/format'
import { useSession } from '../lib/session'
import { useApp } from '../state/app'
import { fromInput, useTransfers } from '../state/transfers'
import { Icon, type IconName } from '../components/Icon'
import {
  Button,
  Field,
  IconButton,
  Menu,
  Modal,
  Select,
  Toggle,
  useToast,
  type MenuItem,
} from '../components/ui'
import { filesLink, routeFolder, useDrawer } from './Sidebar'
import type { SortField, ViewMode } from '../lib/types'

/** AppLayout asks for the search field with this event, so Ctrl K works anywhere. */
const FOCUS_SEARCH_EVENT = 'storix:focus-search'

interface Anchor {
  x: number
  y: number
}

function anchorOf(element: HTMLElement): Anchor {
  const rect = element.getBoundingClientRect()
  return { x: rect.right, y: rect.bottom + 8 }
}

function isTyping(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) return false
  const tag = target.tagName
  return tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT' || target.isContentEditable
}

/**
 * useTopbarNarrow reports whether the bar is too narrow to carry a search field
 * next to everything else. Below this width the field becomes an icon that
 * opens a row of its own.
 */
function useTopbarNarrow(): boolean {
  const [narrow, setNarrow] = useState(() => window.matchMedia('(max-width: 639px)').matches)
  useEffect(() => {
    const media = window.matchMedia('(max-width: 639px)')
    const update = () => setNarrow(media.matches)
    update()
    media.addEventListener('change', update)
    return () => media.removeEventListener('change', update)
  }, [])
  return narrow
}

function messageOf(error: unknown, fallback: string): string {
  return error instanceof Error && error.message ? error.message : fallback
}

// ---- notifications ----------------------------------------------------------

interface Note {
  id: number
  title: string
  icon: IconName
  at: string
}

let noteCounter = 0

function stringField(data: unknown, key: string): string {
  if (typeof data !== 'object' || data === null) return ''
  const value = (data as Record<string, unknown>)[key]
  return typeof value === 'string' ? value : ''
}

/** describe turns a stream event into a line a person can read, or nothing. */
function describe(type: string, data: unknown): { title: string; icon: IconName } | null {
  const title = stringField(data, 'title')
  const name = stringField(data, 'name')
  switch (type) {
    case 'job.created':
      return { title: title ? `${title} started` : 'An operation started', icon: 'activity' }
    case 'job.done':
      return { title: title ? `${title} finished` : 'An operation finished', icon: 'check-circle' }
    case 'job.failed': {
      const reason = stringField(data, 'error')
      return { title: `${title || 'An operation'} failed${reason ? `: ${reason}` : ''}`, icon: 'alert' }
    }
    case 'upload.done':
      return { title: name ? `Uploaded ${name}` : 'An upload finished', icon: 'cloud-upload' }
    case 'system.notice': {
      const text = stringField(data, 'message')
      return text ? { title: text, icon: 'info' } : null
    }
    default:
      return null
  }
}

// ---- account popover --------------------------------------------------------

/**
 * Popover is the dropdown menu shell for the account card. It reuses the menu
 * styling from the design system and only adds the header the menu list cannot
 * express.
 */
function Popover({ anchor, onClose, children }: { anchor: Anchor; onClose: () => void; children: ReactNode }) {
  const ref = useRef<HTMLDivElement>(null)
  useEffect(() => {
    const onDown = (event: MouseEvent) => {
      if (!ref.current?.contains(event.target as Node)) onClose()
    }
    const onKey = (event: KeyboardEvent) => {
      if (event.key === 'Escape') onClose()
    }
    document.addEventListener('mousedown', onDown)
    document.addEventListener('keydown', onKey)
    window.addEventListener('resize', onClose)
    return () => {
      document.removeEventListener('mousedown', onDown)
      document.removeEventListener('keydown', onKey)
      window.removeEventListener('resize', onClose)
    }
  }, [onClose])

  const width = 248
  const left = Math.max(8, Math.min(anchor.x - width, window.innerWidth - width - 8))
  return createPortal(
    <div ref={ref} className="sx-menu fixed z-[60]" style={{ left, top: anchor.y, width }} role="menu">
      {children}
    </div>,
    document.body,
  )
}

function PopoverItem({
  icon,
  label,
  onSelect,
}: {
  icon: IconName
  label: string
  onSelect: () => void
}) {
  return (
    <button type="button" role="menuitem" className="sx-menu-item w-full text-left" onClick={onSelect}>
      <Icon name={icon} size={16} className="shrink-0 opacity-80" />
      <span className="flex-1 truncate">{label}</span>
    </button>
  )
}

// ---- topbar -----------------------------------------------------------------

export default function Topbar({ title }: { title?: string }) {
  const navigate = useNavigate()
  const location = useLocation()
  const queryClient = useQueryClient()
  const toast = useToast()
  const { me, user, can, signOut } = useSession()

  const theme = useApp((state) => state.theme)
  const toggleTheme = useApp((state) => state.toggleTheme)
  const lastPath = useApp((state) => state.lastPath)
  const setTransfersOpen = useApp((state) => state.setTransfersOpen)
  const enqueue = useTransfers((state) => state.enqueue)
  const toggleDrawer = useDrawer((state) => state.toggle)

  const searchRef = useRef<HTMLInputElement>(null)
  const filesRef = useRef<HTMLInputElement>(null)
  const folderRef = useRef<HTMLInputElement>(null)

  const narrow = useTopbarNarrow()

  const [term, setTerm] = useState('')
  const [searchOpen, setSearchOpen] = useState(false)
  const [newMenu, setNewMenu] = useState<Anchor | null>(null)
  const [bellMenu, setBellMenu] = useState<Anchor | null>(null)
  const [account, setAccount] = useState<Anchor | null>(null)
  const [notes, setNotes] = useState<Note[]>([])
  const [unread, setUnread] = useState(0)
  const [createKind, setCreateKind] = useState<'folder' | 'file' | null>(null)
  const [createName, setCreateName] = useState('')
  const [createError, setCreateError] = useState('')
  const [prefsOpen, setPrefsOpen] = useState(false)
  const [passwordOpen, setPasswordOpen] = useState(false)

  const mounts = me?.mounts ?? []
  const routeFolderPath = routeFolder(location.pathname)
  const target = routeFolderPath || lastPath || (mounts.length > 0 ? mounts[0].path : '/')

  // ---- search ---------------------------------------------------------------

  useEffect(() => {
    setTerm(new URLSearchParams(location.search).get('q') ?? '')
  }, [location.search])

  useEffect(() => {
    const focus = () => {
      // On a narrow bar the field lives in a row that has to open first. The
      // effect below focuses it once it is on screen.
      setSearchOpen(true)
      searchRef.current?.focus()
      searchRef.current?.select()
    }
    const onKey = (event: KeyboardEvent) => {
      if (event.key !== '/' || event.ctrlKey || event.metaKey || event.altKey) return
      if (isTyping(event.target)) return
      event.preventDefault()
      focus()
    }
    window.addEventListener(FOCUS_SEARCH_EVENT, focus)
    document.addEventListener('keydown', onKey)
    return () => {
      window.removeEventListener(FOCUS_SEARCH_EVENT, focus)
      document.removeEventListener('keydown', onKey)
    }
  }, [])

  useEffect(() => {
    if (!searchOpen) return
    searchRef.current?.focus()
    searchRef.current?.select()
  }, [searchOpen])

  // Changing width moves the field between the bar and a row of its own, so the
  // overlay never survives the change.
  useEffect(() => {
    setSearchOpen(false)
  }, [narrow])

  const submitSearch = (event: FormEvent) => {
    event.preventDefault()
    const query = term.trim()
    if (!query) return
    searchRef.current?.blur()
    setSearchOpen(false)
    navigate(`/files?q=${encodeURIComponent(query)}`)
  }

  // ---- notifications --------------------------------------------------------

  useEffect(() => {
    const stop = subscribe((type, data) => {
      const described = describe(type, data)
      if (!described) return
      noteCounter += 1
      const note: Note = { id: noteCounter, title: described.title, icon: described.icon, at: new Date().toISOString() }
      setNotes((current) => [note, ...current].slice(0, 20))
      setUnread((count) => Math.min(99, count + 1))
    })
    return stop
  }, [])

  const noteItems: MenuItem[] =
    notes.length === 0
      ? [{ id: 'empty', label: 'No activity yet', icon: 'bell', disabled: true }]
      : notes.map((note) => ({
          id: String(note.id),
          label: note.title,
          icon: note.icon,
          shortcut: ago(note.at),
          onSelect: () => navigate('/transfers'),
        }))

  const bellItems: MenuItem[] = [
    ...noteItems,
    { id: 'divider', label: '', divider: true },
    { id: 'transfers', label: 'Open transfers', icon: 'activity', onSelect: () => navigate('/transfers') },
  ]

  // ---- uploads --------------------------------------------------------------

  useEffect(() => {
    // React has no typed attribute for folder pickers, so it is set directly.
    folderRef.current?.setAttribute('webkitdirectory', '')
  }, [])

  const startUpload = useCallback(
    (files: Array<{ file: File; relativePath: string }>) => {
      if (files.length === 0) return
      enqueue(files, target)
      setTransfersOpen(true)
      toast.info(`Uploading ${counted(files.length, 'file')}`, truncateMiddle(target, 44))
    },
    [enqueue, setTransfersOpen, target, toast],
  )

  // ---- create ---------------------------------------------------------------

  const create = useMutation({
    mutationFn: (input: { kind: 'folder' | 'file'; name: string }) =>
      input.kind === 'folder' ? api.mkdir(target, input.name) : api.touch(target, input.name),
    onSuccess: (_entry, input) => {
      void queryClient.invalidateQueries({ queryKey: ['list', target] })
      void queryClient.invalidateQueries({ queryKey: ['dashboard'] })
      toast.success(input.kind === 'folder' ? 'Folder created' : 'File created', input.name)
      setCreateKind(null)
      setCreateName('')
      if (routeFolderPath !== target) navigate(filesLink(target))
    },
    onError: (error) => setCreateError(messageOf(error, 'That did not work')),
  })

  const submitCreate = () => {
    const name = createName.trim()
    if (!createKind) return
    if (!name) {
      setCreateError('Give it a name first')
      return
    }
    if (name.includes('/')) {
      setCreateError('A name cannot contain a slash')
      return
    }
    setCreateError('')
    create.mutate({ kind: createKind, name })
  }

  const openCreate = (kind: 'folder' | 'file') => {
    setCreateKind(kind)
    setCreateName('')
    setCreateError('')
  }

  const canCreate = can('create')
  const canUpload = can('upload')
  const newItems: MenuItem[] = [
    {
      id: 'new-folder',
      label: 'New folder',
      icon: 'folder-plus',
      shortcut: 'Ctrl Shift N',
      disabled: !canCreate,
      onSelect: () => openCreate('folder'),
    },
    { id: 'new-file', label: 'New file', icon: 'file-plus', disabled: !canCreate, onSelect: () => openCreate('file') },
    { id: 'new-divider', label: '', divider: true },
    {
      id: 'upload-files',
      label: 'Upload files',
      icon: 'cloud-upload',
      shortcut: 'Ctrl U',
      disabled: !canUpload,
      onSelect: () => filesRef.current?.click(),
    },
    {
      id: 'upload-folder',
      label: 'Upload folder',
      icon: 'upload',
      disabled: !canUpload,
      onSelect: () => folderRef.current?.click(),
    },
  ]

  // ---- account --------------------------------------------------------------

  const displayName = user?.displayName || user?.username || 'Account'
  const username = user?.username ?? ''

  const handleSignOut = async () => {
    setAccount(null)
    await signOut()
    navigate('/login', { replace: true })
  }

  const searchField = (
    <div className="relative min-w-0 flex-1">
      <span className="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-faint">
        <Icon name="search" size={16} />
      </span>
      <input
        ref={searchRef}
        type="search"
        value={term}
        onChange={(event) => setTerm(event.target.value)}
        onKeyDown={(event) => {
          if (event.key === 'Escape') {
            setTerm('')
            setSearchOpen(false)
            event.currentTarget.blur()
          }
        }}
        placeholder="Search files and folders"
        aria-label="Search files and folders"
        className="sx-input pl-9 pr-3 sm:pr-20"
      />
      <span className="sx-chip pointer-events-none absolute right-1.5 top-1/2 hidden -translate-y-1/2 text-[10px] text-faint sm:inline-flex">
        Ctrl K
      </span>
    </div>
  )

  return (
    <header className="relative flex h-[60px] shrink-0 items-center gap-2 border-b border-line bg-surface px-3 sm:px-4">
      <IconButton icon="menu" label="Show navigation" className="sx-touch lg:hidden" onClick={() => toggleDrawer()} />

      {title && <h1 className="hidden shrink-0 truncate text-sm font-medium text-ink sm:block">{title}</h1>}

      {narrow ? (
        <div className="flex-1" />
      ) : (
        <form onSubmit={submitSearch} className="mx-auto w-full max-w-[520px] flex-1" role="search">
          {searchField}
        </form>
      )}

      <div className="flex shrink-0 items-center gap-1">
        {narrow && (
          <IconButton
            icon="search"
            label="Search files and folders"
            className="sx-touch"
            onClick={() => setSearchOpen(true)}
          />
        )}

        {(canCreate || canUpload) && (
          <Button
            variant="primary"
            icon="plus"
            className="hidden sm:inline-flex"
            aria-haspopup="menu"
            onClick={(event) => setNewMenu(anchorOf(event.currentTarget))}
          >
            New
          </Button>
        )}
        {(canCreate || canUpload) && (
          <IconButton
            icon="plus"
            label="New"
            className="sx-touch sm:hidden"
            onClick={(event) => setNewMenu(anchorOf(event.currentTarget))}
          />
        )}

        <IconButton
          icon={theme === 'dark' ? 'sun' : 'moon'}
          label={theme === 'dark' ? 'Switch to the light theme' : 'Switch to the dark theme'}
          className="sx-touch"
          onClick={() => toggleTheme()}
        />

        <span className="relative inline-flex">
          <IconButton
            icon="bell"
            label={unread > 0 ? `Notifications, ${unread} new` : 'Notifications'}
            className="sx-touch"
            onClick={(event) => {
              setUnread(0)
              setBellMenu(anchorOf(event.currentTarget))
            }}
          />
          {unread > 0 && (
            <span className="pointer-events-none absolute right-1.5 top-1.5 h-2 w-2 rounded-full bg-primary ring-2 ring-surface" />
          )}
        </span>

        <button
          type="button"
          aria-label="Account"
          aria-haspopup="menu"
          onClick={(event) => setAccount(anchorOf(event.currentTarget))}
          className="sx-touch ml-1 flex h-9 w-9 items-center justify-center rounded-xl text-[12px] font-semibold text-white transition-transform hover:brightness-110"
          style={{
            backgroundImage: 'linear-gradient(135deg, rgb(var(--sx-secondary)), rgb(var(--sx-accent)))',
          }}
        >
          {initials(displayName)}
        </button>
      </div>

      {narrow && searchOpen && (
        <form
          onSubmit={submitSearch}
          role="search"
          className="absolute inset-x-0 top-0 z-20 flex h-[60px] animate-fade-in items-center gap-2 border-b border-line bg-surface px-3"
        >
          {searchField}
          <IconButton
            icon="close"
            label="Close search"
            className="sx-touch shrink-0"
            onClick={() => setSearchOpen(false)}
          />
        </form>
      )}

      <input
        ref={filesRef}
        type="file"
        multiple
        className="hidden"
        onChange={(event) => {
          startUpload(fromInput(event.target.files))
          event.target.value = ''
        }}
      />
      <input
        ref={folderRef}
        type="file"
        multiple
        className="hidden"
        onChange={(event) => {
          startUpload(fromInput(event.target.files))
          event.target.value = ''
        }}
      />

      {newMenu && <Menu items={newItems} x={newMenu.x} y={newMenu.y} anchorRight onClose={() => setNewMenu(null)} />}
      {bellMenu && (
        <Menu items={bellItems} x={bellMenu.x} y={bellMenu.y} anchorRight onClose={() => setBellMenu(null)} />
      )}

      {account && (
        <Popover anchor={account} onClose={() => setAccount(null)}>
          <div className="px-3 py-2.5">
            <div className="truncate text-sm font-medium text-ink">{displayName}</div>
            {username && <div className="mt-0.5 truncate text-xs text-faint">{username}</div>}
          </div>
          <div className="sx-divider" />
          <PopoverItem
            icon="settings"
            label="Preferences"
            onSelect={() => {
              setAccount(null)
              setPrefsOpen(true)
            }}
          />
          <PopoverItem
            icon="key"
            label="Change password"
            onSelect={() => {
              setAccount(null)
              setPasswordOpen(true)
            }}
          />
          <div className="sx-divider" />
          <PopoverItem icon="logout" label="Sign out" onSelect={() => void handleSignOut()} />
        </Popover>
      )}

      <Modal
        open={createKind !== null}
        onClose={() => setCreateKind(null)}
        title={createKind === 'file' ? 'New file' : 'New folder'}
        icon={createKind === 'file' ? 'file-plus' : 'folder-plus'}
        description={`It will be created in ${truncateMiddle(target, 40)}`}
        footer={
          <>
            <Button onClick={() => setCreateKind(null)} disabled={create.isPending}>
              Cancel
            </Button>
            <Button variant="primary" onClick={submitCreate} loading={create.isPending}>
              Create
            </Button>
          </>
        }
      >
        <Field
          label="Name"
          autoFocus
          value={createName}
          error={createError}
          onChange={(event) => setCreateName(event.target.value)}
          onKeyDown={(event) => {
            if (event.key === 'Enter') {
              event.preventDefault()
              submitCreate()
            }
          }}
          placeholder={createKind === 'file' ? 'notes.txt' : 'Reports'}
        />
      </Modal>

      <PreferencesModal open={prefsOpen} onClose={() => setPrefsOpen(false)} />
      <PasswordModal open={passwordOpen} onClose={() => setPasswordOpen(false)} />
    </header>
  )
}

// ---- preferences ------------------------------------------------------------

function PreferencesModal({ open, onClose }: { open: boolean; onClose: () => void }) {
  const toast = useToast()
  const app = useApp()

  const save = useMutation({
    mutationFn: (body: Record<string, unknown>) => api.savePreferences(body),
    onError: () => toast.error('Preferences were not saved', 'They still apply on this device'),
  })

  const done = () => {
    save.mutate({
      theme: app.theme,
      view: app.view,
      sort: app.sort,
      order: app.order,
      showHidden: app.showHidden,
      foldersFirst: app.foldersFirst,
    })
    onClose()
  }

  return (
    <Modal
      open={open}
      onClose={onClose}
      title="Preferences"
      icon="settings"
      description="These settings follow your account."
      footer={
        <Button variant="primary" onClick={done} loading={save.isPending}>
          Done
        </Button>
      }
    >
      <div className="space-y-4">
        <Select
          label="Theme"
          value={app.theme}
          onChange={(value) => app.setTheme(value === 'light' ? 'light' : 'dark')}
          options={[
            { value: 'dark', label: 'Dark' },
            { value: 'light', label: 'Light' },
          ]}
        />
        <Select
          label="Default view"
          value={app.view}
          onChange={(value) => app.setView(value as ViewMode)}
          options={[
            { value: 'list', label: 'List' },
            { value: 'grid', label: 'Grid' },
            { value: 'gallery', label: 'Gallery' },
          ]}
        />
        <Select
          label="Sort files by"
          value={app.sort}
          onChange={(value) => app.setSort(value as SortField, app.order)}
          options={[
            { value: 'name', label: 'Name' },
            { value: 'modified', label: 'Last changed' },
            { value: 'size', label: 'Size' },
            { value: 'kind', label: 'Kind' },
          ]}
        />
        <div className="space-y-3 border-t border-line pt-4">
          <Toggle
            checked={app.foldersFirst}
            onChange={app.setFoldersFirst}
            label="Show folders before files"
          />
          <Toggle
            checked={app.showHidden}
            onChange={() => app.toggleHidden()}
            label="Show hidden files"
            hint="Files whose name starts with a dot."
          />
        </div>
      </div>
    </Modal>
  )
}

// ---- change password --------------------------------------------------------

function PasswordModal({ open, onClose }: { open: boolean; onClose: () => void }) {
  const toast = useToast()
  const [current, setCurrent] = useState('')
  const [next, setNext] = useState('')
  const [confirm, setConfirm] = useState('')
  const [error, setError] = useState('')

  useEffect(() => {
    if (!open) return
    setCurrent('')
    setNext('')
    setConfirm('')
    setError('')
  }, [open])

  const change = useMutation({
    mutationFn: () => api.changePassword({ current, new: next }),
    onSuccess: () => {
      toast.success('Password changed')
      onClose()
    },
    onError: (failure) => setError(messageOf(failure, 'The password could not be changed')),
  })

  const submit = () => {
    if (!current || !next) {
      setError('Fill in both passwords')
      return
    }
    if (next.length < 8) {
      setError('Use at least 8 characters')
      return
    }
    if (next !== confirm) {
      setError('The two new passwords do not match')
      return
    }
    setError('')
    change.mutate()
  }

  return (
    <Modal
      open={open}
      onClose={onClose}
      title="Change password"
      icon="key"
      footer={
        <>
          <Button onClick={onClose} disabled={change.isPending}>
            Cancel
          </Button>
          <Button variant="primary" onClick={submit} loading={change.isPending}>
            Change password
          </Button>
        </>
      }
    >
      <div className="space-y-3">
        <Field
          label="Current password"
          type="password"
          autoComplete="current-password"
          value={current}
          onChange={(event) => setCurrent(event.target.value)}
        />
        <Field
          label="New password"
          type="password"
          autoComplete="new-password"
          hint="At least 8 characters."
          value={next}
          onChange={(event) => setNext(event.target.value)}
        />
        <Field
          label="Repeat new password"
          type="password"
          autoComplete="new-password"
          value={confirm}
          error={error}
          onChange={(event) => setConfirm(event.target.value)}
          onKeyDown={(event) => {
            if (event.key === 'Enter') {
              event.preventDefault()
              submit()
            }
          }}
        />
      </div>
    </Modal>
  )
}
