// The application shell. Everything a signed in person sees is rendered
// inside this frame: navigation on the left, the bar on top, the page in the
// middle, transfers docked bottom right.
// Developed by X Project.

import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Outlet, useLocation } from 'react-router-dom'
import { useQueryClient } from '@tanstack/react-query'
import { useStorixEvents } from '../state/events'
import { baseName, counted, truncateMiddle } from '../lib/format'
import { useSession } from '../lib/session'
import { useApp } from '../state/app'
import { collectFiles, useTransfers } from '../state/transfers'
import { Icon } from '../components/Icon'
import { Modal, SectionTitle, useToast } from '../components/ui'
import TransferDock from '../components/TransferDock'
import Sidebar, { routeFolder, useCompact, useDrawer } from './Sidebar'
import Topbar from './Topbar'

/** Topbar listens for this event, so Ctrl K reaches the search field. */
const FOCUS_SEARCH_EVENT = 'storix:focus-search'

const TITLES: Array<{ match: string; title: string; end?: boolean }> = [
  { match: '/', title: 'Dashboard', end: true },
  { match: '/files', title: 'Files' },
  { match: '/recent', title: 'Recent' },
  { match: '/favorites', title: 'Favorites' },
  { match: '/shares', title: 'Shares' },
  { match: '/transfers', title: 'Transfers' },
  { match: '/trash', title: 'Trash' },
  { match: '/users', title: 'Users' },
  { match: '/settings', title: 'Settings' },
]

function titleFor(pathname: string): string {
  for (const entry of TITLES) {
    if (entry.end ? pathname === entry.match : pathname === entry.match || pathname.startsWith(entry.match + '/')) {
      return entry.title
    }
  }
  return 'Storix'
}

const SHORTCUTS: Array<{ group: string; rows: Array<[string, string]> }> = [
  {
    group: 'Getting around',
    rows: [
      ['Ctrl K', 'Search files and folders'],
      ['/', 'Search files and folders'],
      ['Ctrl B', 'Show or hide the sidebar'],
      ['?', 'Show this list'],
      ['Alt Left', 'Go back'],
      ['Alt Right', 'Go forward'],
      ['Backspace', 'Go to the folder above'],
      ['Esc', 'Close whatever is open'],
    ],
  },
  {
    group: 'Looking at a folder',
    rows: [
      ['Enter', 'Open the selected item'],
      ['Space', 'Preview without opening'],
      ['Ctrl 1', 'List view'],
      ['Ctrl 2', 'Grid view'],
      ['Ctrl 3', 'Gallery view'],
      ['Ctrl H', 'Show hidden files'],
      ['Ctrl I', 'Show or hide details'],
      ['F5', 'Reload the folder'],
    ],
  },
  {
    group: 'Selecting',
    rows: [
      ['Ctrl A', 'Select everything'],
      ['Ctrl Click', 'Add one item to the selection'],
      ['Shift Click', 'Select a range'],
      ['Arrow keys', 'Move through the list'],
      ['Esc', 'Clear the selection'],
    ],
  },
  {
    group: 'Working with files',
    rows: [
      ['Ctrl X', 'Cut'],
      ['Ctrl C', 'Copy'],
      ['Ctrl V', 'Paste here'],
      ['F2', 'Rename'],
      ['Delete', 'Move to trash'],
      ['Shift Delete', 'Delete for good'],
      ['Ctrl D', 'Download'],
      ['Ctrl E', 'Edit as text'],
      ['Ctrl Shift N', 'New folder'],
      ['Ctrl U', 'Upload files'],
    ],
  },
]

function isTyping(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) return false
  const tag = target.tagName
  return tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT' || target.isContentEditable
}

function ShortcutsModal({ open, onClose }: { open: boolean; onClose: () => void }) {
  return (
    <Modal
      open={open}
      onClose={onClose}
      title="Keyboard shortcuts"
      icon="help"
      description="Storix is built to be driven from the keyboard."
      width={760}
    >
      <div className="sx-scroll -mr-2 max-h-[62vh] pr-2">
        <div className="grid gap-x-10 gap-y-7 sm:grid-cols-2">
          {SHORTCUTS.map((section) => (
            <section key={section.group}>
              <SectionTitle>{section.group}</SectionTitle>
              <dl className="space-y-2">
                {section.rows.map(([keys, what]) => (
                  <div key={`${section.group}-${keys}-${what}`} className="flex items-center justify-between gap-4">
                    <dt className="min-w-0 truncate text-sm text-muted">{what}</dt>
                    <dd className="flex shrink-0 items-center gap-1">
                      {keys.split(' ').map((key) => (
                        <kbd
                          key={key}
                          className="rounded-lg border border-line bg-elevated px-1.5 py-0.5 font-mono text-[11px] text-ink"
                        >
                          {key}
                        </kbd>
                      ))}
                    </dd>
                  </div>
                ))}
              </dl>
            </section>
          ))}
        </div>
      </div>
    </Modal>
  )
}

export default function AppLayout() {
  const location = useLocation()
  const queryClient = useQueryClient()
  const toast = useToast()
  const { can } = useSession()

  const compact = useCompact()
  const drawerOpen = useDrawer((state) => state.open)
  const setDrawer = useDrawer((state) => state.setOpen)
  const toggleDrawer = useDrawer((state) => state.toggle)
  const toggleSidebar = useApp((state) => state.toggleSidebar)
  const setTransfersOpen = useApp((state) => state.setTransfersOpen)
  const enqueue = useTransfers((state) => state.enqueue)
  const setOnComplete = useTransfers((state) => state.setOnComplete)

  const [shortcutsOpen, setShortcutsOpen] = useState(false)
  const [dragging, setDragging] = useState(false)

  const folder = routeFolder(location.pathname)
  const canUpload = can('upload')
  const droppable = Boolean(folder) && canUpload

  // Handlers below are registered once, so the live values travel in refs.
  const folderRef = useRef(folder)
  const droppableRef = useRef(droppable)
  const compactRef = useRef(compact)
  useEffect(() => {
    folderRef.current = folder
    droppableRef.current = droppable
    compactRef.current = compact
  }, [folder, droppable, compact])

  // The drawer covers the page on a narrow screen, so the page behind it is
  // held still. Sidebar owns the backdrop and the Escape key; nothing here
  // takes the focus away from the drawer or gives it back.
  useEffect(() => {
    document.body.classList.toggle('sx-locked', compact && drawerOpen)
    return () => document.body.classList.remove('sx-locked')
  }, [compact, drawerOpen])

  // ---- live updates ---------------------------------------------------------

  useStorixEvents(
    useCallback(
      (type: string) => {
        const invalidate = (key: string) => void queryClient.invalidateQueries({ queryKey: [key] })
      switch (type) {
        case 'fs.changed':
          invalidate('list')
          invalidate('dashboard')
          break
        case 'job.created':
        case 'job.progress':
        case 'job.done':
        case 'job.failed':
          invalidate('jobs')
          break
        case 'upload.done':
          invalidate('list')
          invalidate('dashboard')
          break
        case 'share.changed':
          invalidate('shares')
          break
          default:
            break
        }
      },
      [queryClient],
    ),
  )

  useEffect(() => {
    setOnComplete((dir) => {
      void queryClient.invalidateQueries({ queryKey: ['list', dir] })
      void queryClient.invalidateQueries({ queryKey: ['dashboard'] })
    })
  }, [setOnComplete, queryClient])

  // ---- keyboard -------------------------------------------------------------

  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      const modifier = event.ctrlKey || event.metaKey
      if (modifier && event.key.toLowerCase() === 'k') {
        event.preventDefault()
        window.dispatchEvent(new Event(FOCUS_SEARCH_EVENT))
        return
      }
      if (modifier && event.key.toLowerCase() === 'b') {
        event.preventDefault()
        if (compactRef.current) toggleDrawer()
        else toggleSidebar()
        return
      }
      if (event.key === '?' && !modifier && !isTyping(event.target)) {
        event.preventDefault()
        setShortcutsOpen(true)
      }
    }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [toggleDrawer, toggleSidebar])

  // ---- drag and drop --------------------------------------------------------

  useEffect(() => {
    let depth = 0
    const carriesFiles = (event: DragEvent) => Array.from(event.dataTransfer?.types ?? []).includes('Files')

    const onEnter = (event: DragEvent) => {
      if (!carriesFiles(event)) return
      depth += 1
      setDragging(true)
    }
    const onOver = (event: DragEvent) => {
      if (!carriesFiles(event)) return
      event.preventDefault()
      if (event.dataTransfer) event.dataTransfer.dropEffect = droppableRef.current ? 'copy' : 'none'
    }
    const onLeave = (event: DragEvent) => {
      if (!carriesFiles(event)) return
      depth = Math.max(0, depth - 1)
      if (depth === 0) setDragging(false)
    }
    const onDrop = (event: DragEvent) => {
      if (!carriesFiles(event)) return
      event.preventDefault()
      depth = 0
      setDragging(false)
      const dir = folderRef.current
      if (!droppableRef.current || !dir || !event.dataTransfer) return
      void collectFiles(event.dataTransfer).then((files) => {
        if (files.length === 0) return
        enqueue(files, dir)
        setTransfersOpen(true)
        toast.info(`Uploading ${counted(files.length, 'file')}`, truncateMiddle(dir, 44))
      })
    }

    window.addEventListener('dragenter', onEnter)
    window.addEventListener('dragover', onOver)
    window.addEventListener('dragleave', onLeave)
    window.addEventListener('drop', onDrop)
    return () => {
      window.removeEventListener('dragenter', onEnter)
      window.removeEventListener('dragover', onOver)
      window.removeEventListener('dragleave', onLeave)
      window.removeEventListener('drop', onDrop)
    }
  }, [enqueue, setTransfersOpen, toast])

  const overlay = useMemo(() => {
    if (!canUpload) {
      return {
        title: 'Uploading is not available for your account',
        detail: 'Ask an administrator for upload access.',
      }
    }
    if (!folder) {
      return {
        title: 'Open a folder first',
        detail: 'Uploads go to the folder you are looking at. Go to Files, open one, then drop again.',
      }
    }
    return {
      title: `Drop files to upload to ${baseName(folder) || 'the top level'}`,
      detail: truncateMiddle(folder, 52),
    }
  }, [canUpload, folder])

  return (
    <div className="flex h-full w-full overflow-hidden bg-bg">
      <Sidebar onNavigate={() => setDrawer(false)} />

      <div className="flex min-w-0 flex-1 flex-col">
        <Topbar title={titleFor(location.pathname)} />
        <main className="sx-scroll min-h-0 flex-1">
          <Outlet />
        </main>
      </div>

      <TransferDock />

      {dragging && (
        <div className="fixed inset-0 z-[65] flex animate-fade-in items-center justify-center bg-bg/85 p-8 backdrop-blur-sm">
          <div
            className={
              'flex w-full max-w-lg flex-col items-center rounded-3xl border-2 border-dashed px-8 py-14 text-center ' +
              (droppable ? 'border-primary/60 bg-surface/70' : 'border-line bg-surface/70')
            }
          >
            <span
              className={
                'flex h-16 w-16 items-center justify-center rounded-2xl ' +
                (droppable ? 'bg-primary/12 text-primary' : 'bg-elevated text-faint')
              }
            >
              <Icon name="cloud-upload" size={30} />
            </span>
            <h2 className="mt-5 text-lg font-medium text-ink">{overlay.title}</h2>
            <p className="mt-2 max-w-sm text-sm text-muted">{overlay.detail}</p>
          </div>
        </div>
      )}

      <ShortcutsModal open={shortcutsOpen} onClose={() => setShortcutsOpen(false)} />
    </div>
  )
}
