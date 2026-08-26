// The file browser. This is the screen the product lives in: a listing, a
// selection, a toolbar and every operation the server can run on a path.
// Developed by X Project.

import {
  Suspense,
  lazy,
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type DragEvent,
  type ReactNode,
} from 'react'
import { useNavigate, useParams, useSearchParams } from 'react-router-dom'
import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ApiError, api } from '../lib/api'
import type { Crumb, Entry, Mount, SortField } from '../lib/types'
import { baseName, bytes, counted, parentPath } from '../lib/format'
import { useSession } from '../lib/session'
import { useApp } from '../state/app'
import { fromInput, useTransfers } from '../state/transfers'
import { Icon, type IconName } from '../components/Icon'
import {
  Button,
  ConfirmDialog,
  EmptyState,
  IconButton,
  Menu,
  Spinner,
  useToast,
  type MenuItem,
} from '../components/ui'
import { useCompact } from '../layout/Sidebar'
import { Breadcrumbs } from '../components/Breadcrumbs'
import { FileList, STORIX_DRAG_TYPE, type SelectModifiers } from '../components/FileList'
import { FileGallery, FileGrid } from '../components/FileGrid'
import {
  CompressDialog,
  ExtractDialog,
  NewFileDialog,
  NewFolderDialog,
  PathPickerDialog,
  PropertiesDialog,
  RenameDialog,
} from '../components/dialogs'
import { BulkRenameDialog } from '../components/BulkRenameDialog'
import { ShareDialog } from '../components/ShareDialog'
import DetailsPanel from '../components/DetailsPanel'

const CodeEditor = lazy(() => import('../components/CodeEditor'))

const ARCHIVE_EXTENSIONS = new Set([
  'zip',
  'tar',
  'gz',
  'tgz',
  'bz2',
  'tbz',
  'xz',
  'txz',
  'zst',
  '7z',
  'rar',
])

const MOUNT_ICONS: Record<string, IconName> = {
  folder: 'folder',
  home: 'home',
  drive: 'drive',
  server: 'server',
  database: 'database',
  archive: 'archive',
  users: 'users',
  user: 'user',
  globe: 'globe',
  star: 'star',
  code: 'code',
  image: 'image',
  video: 'video',
  music: 'music',
  monitor: 'monitor',
  terminal: 'terminal',
  shield: 'shield',
  cpu: 'cpu',
  file: 'file',
  lock: 'lock',
}

function mountIcon(name: string): IconName {
  return MOUNT_ICONS[name] ?? 'drive'
}

function isArchive(entry: Entry): boolean {
  if (entry.isDir) return false
  if (entry.kind === 'archive') return true
  return ARCHIVE_EXTENSIONS.has(entry.ext.replace(/^\./, '').toLowerCase())
}

function reason(error: unknown): string {
  if (error instanceof ApiError) return error.message
  if (error instanceof Error) return error.message
  return 'The server did not accept the request'
}

function toRoute(path: string): string {
  if (!path) return '/files'
  // The root volume needs a segment of its own so the route keeps a splat.
  if (path === '/') return '/files//'
  return '/files' + path.split('/').map(encodeURIComponent).join('/')
}

/** fromRoute turns the part of the URL after /files back into a server path. */
function fromRoute(splat: string): string {
  if (!splat) return ''
  const merged = ('/' + splat).replace(/\/{2,}/g, '/')
  return merged.length > 1 ? merged.replace(/\/+$/, '') : '/'
}

function triggerDownload(url: string): void {
  const anchor = document.createElement('a')
  anchor.href = url
  anchor.rel = 'noopener'
  document.body.appendChild(anchor)
  anchor.click()
  anchor.remove()
}

export default function FilesPage() {
  const params = useParams()
  const navigate = useNavigate()
  const [searchParams] = useSearchParams()
  const queryClient = useQueryClient()
  const toast = useToast()
  const { can, me } = useSession()
  const app = useApp()
  const enqueue = useTransfers((state) => state.enqueue)
  // Under 1024px the details panel becomes a sheet and the dock spans the foot
  // of the screen, so the listing has to know about both.
  const compact = useCompact()
  const docked = useTransfers((state) => state.items.length > 0)

  const path = fromRoute(params['*'] ?? '')
  const searchTerm = (searchParams.get('q') ?? '').trim()
  const searching = searchTerm.length > 0

  // ---- state ----------------------------------------------------------------

  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [anchor, setAnchor] = useState<string | null>(null)
  const [focused, setFocused] = useState<string | null>(null)
  const [renamingPath, setRenamingPath] = useState<string | null>(null)
  const [menu, setMenu] = useState<{ x: number; y: number; entry: Entry | null } | null>(null)
  const [toolMenu, setToolMenu] = useState<{ x: number; y: number; kind: 'upload' | 'more' } | null>(null)
  const [newFolderOpen, setNewFolderOpen] = useState(false)
  const [newFileOpen, setNewFileOpen] = useState(false)
  const [renameTarget, setRenameTarget] = useState<Entry | null>(null)
  const [bulkRenameOpen, setBulkRenameOpen] = useState(false)
  const [picker, setPicker] = useState<{ mode: 'move' | 'copy'; sources: string[] } | null>(null)
  const [compressOpen, setCompressOpen] = useState(false)
  const [extractTarget, setExtractTarget] = useState<Entry | null>(null)
  const [propertiesTarget, setPropertiesTarget] = useState<Entry | null>(null)
  const [shareTarget, setShareTarget] = useState<Entry | null>(null)
  const [confirmDelete, setConfirmDelete] = useState<string[] | null>(null)
  const [editorPath, setEditorPath] = useState<string | null>(null)
  // The sheet covers the listing, so on a narrow screen the details only appear
  // when they are asked for, never because the panel was left open on a desk.
  const [sheetOpen, setSheetOpen] = useState(false)

  const selectionBase = useRef<Set<string>>(new Set())
  const fileInput = useRef<HTMLInputElement>(null)
  const folderInput = useRef<HTMLInputElement>(null)

  const dialogOpen =
    newFolderOpen ||
    newFileOpen ||
    renameTarget !== null ||
    bulkRenameOpen ||
    picker !== null ||
    compressOpen ||
    extractTarget !== null ||
    propertiesTarget !== null ||
    shareTarget !== null ||
    confirmDelete !== null ||
    menu !== null ||
    toolMenu !== null ||
    editorPath !== null ||
    renamingPath !== null

  // ---- data -----------------------------------------------------------------

  const listQuery = useQuery({
    queryKey: ['list', path, app.showHidden, app.sort, app.order],
    queryFn: () => api.list({ path, hidden: app.showHidden, sort: app.sort, order: app.order }),
    placeholderData: keepPreviousData,
  })

  const searchQuery = useQuery({
    queryKey: ['search', searchTerm, path, app.showHidden],
    queryFn: () => api.search({ q: searchTerm, path: path || undefined, hidden: app.showHidden, limit: 500 }),
    enabled: searching,
  })

  const listing = listQuery.data
  const entries = useMemo<Entry[]>(
    () => (searching ? (searchQuery.data?.entries ?? []) : (listing?.entries ?? [])),
    [searching, searchQuery.data, listing],
  )
  const isRoot = !searching && (path === '' || listing?.isRoot === true)
  const readOnly = listing?.readOnly === true
  const loading = searching ? searchQuery.isLoading : listQuery.isLoading
  const failure = searching ? searchQuery.error : listQuery.error

  const canUpload = can('upload') && !readOnly && !isRoot
  const canCreate = can('create') && !readOnly && !isRoot
  const canDelete = can('delete') && !readOnly
  const canMove = can('move') && !readOnly

  const selectedEntries = useMemo(() => entries.filter((entry) => selected.has(entry.path)), [entries, selected])
  const selectedPaths = useMemo(() => selectedEntries.map((entry) => entry.path), [selectedEntries])
  const selectedBytes = useMemo(
    () => selectedEntries.reduce((total, entry) => total + (entry.isDir ? 0 : entry.size), 0),
    [selectedEntries],
  )
  const singleArchive = selectedEntries.length === 1 && isArchive(selectedEntries[0])

  const crumbs = useMemo<Crumb[]>(() => {
    const trail = listing?.breadcrumbs ?? []
    const home: Crumb = { name: 'All files', path: '' }
    if (isRoot || trail.length === 0) return [home]
    return (me?.mounts.length ?? 0) > 1 ? [home, ...trail] : trail
  }, [isRoot, listing, me])

  const detailEntry = useMemo<Entry | null>(() => {
    if (focused) {
      const match = entries.find((entry) => entry.path === focused)
      if (match) return match
    }
    if (selectedEntries.length === 1) return selectedEntries[0]
    return null
  }, [entries, focused, selectedEntries])

  useEffect(() => {
    setSelected(new Set())
    selectionBase.current = new Set()
    setAnchor(null)
    setFocused(null)
    setRenamingPath(null)
    setSheetOpen(false)
  }, [path, searchTerm])

  useEffect(() => {
    if (!compact) setSheetOpen(false)
  }, [compact])

  useEffect(() => {
    if (path) useApp.getState().setLastPath(path)
  }, [path])

  useEffect(() => {
    const node = folderInput.current
    if (!node) return
    node.setAttribute('webkitdirectory', '')
    node.setAttribute('directory', '')
  }, [])

  const invalidateListing = useCallback(() => {
    void queryClient.invalidateQueries({ queryKey: ['list'] })
    void queryClient.invalidateQueries({ queryKey: ['dashboard'] })
  }, [queryClient])

  useEffect(() => {
    useTransfers.getState().setOnComplete((dir) => {
      void queryClient.invalidateQueries({ queryKey: ['list', dir] })
    })
  }, [queryClient])

  // ---- selection ------------------------------------------------------------

  const applySelection = useCallback((next: Set<string>, remember = true) => {
    setSelected(next)
    if (remember) selectionBase.current = next
  }, [])

  const handleSelect = useCallback(
    (entry: Entry, modifiers: SelectModifiers) => {
      setFocused(entry.path)
      if (modifiers.shiftKey && anchor) {
        const from = entries.findIndex((item) => item.path === anchor)
        const to = entries.findIndex((item) => item.path === entry.path)
        if (from >= 0 && to >= 0) {
          const [start, end] = from < to ? [from, to] : [to, from]
          applySelection(new Set(entries.slice(start, end + 1).map((item) => item.path)))
          return
        }
      }
      if (modifiers.ctrlKey || modifiers.metaKey) {
        const next = new Set(selected)
        if (next.has(entry.path)) next.delete(entry.path)
        else next.add(entry.path)
        applySelection(next)
        setAnchor(entry.path)
        return
      }
      applySelection(new Set([entry.path]))
      setAnchor(entry.path)
    },
    [anchor, applySelection, entries, selected],
  )

  const handleMarquee = useCallback(
    (paths: string[], additive: boolean) => {
      const next = additive ? new Set([...selectionBase.current, ...paths]) : new Set(paths)
      applySelection(next, false)
      setFocused(paths.length > 0 ? (paths[paths.length - 1] ?? null) : null)
    },
    [applySelection],
  )

  const selectAll = useCallback(() => {
    applySelection(new Set(entries.map((entry) => entry.path)))
  }, [applySelection, entries])

  const clearSelection = useCallback(() => {
    applySelection(new Set())
    setAnchor(null)
  }, [applySelection])

  const focusIndex = useCallback(
    (index: number, extend: boolean) => {
      if (entries.length === 0) return
      const clamped = Math.max(0, Math.min(entries.length - 1, index))
      const entry = entries[clamped]
      if (!entry) return
      setFocused(entry.path)
      if (extend && anchor) {
        const from = entries.findIndex((item) => item.path === anchor)
        if (from >= 0) {
          const [start, end] = from < clamped ? [from, clamped] : [clamped, from]
          applySelection(new Set(entries.slice(start, end + 1).map((item) => item.path)))
          return
        }
      }
      applySelection(new Set([entry.path]))
      setAnchor(entry.path)
    },
    [anchor, applySelection, entries],
  )

  // ---- navigation and opening ----------------------------------------------

  const go = useCallback(
    (next: string) => {
      navigate(toRoute(next))
    },
    [navigate],
  )

  const openEditor = useCallback((entry: Entry) => {
    setEditorPath(entry.path)
  }, [])

  const download = useCallback(
    (items: Entry[]) => {
      if (items.length === 0) return
      const first = items[0]
      if (items.length === 1 && first && !first.isDir) {
        triggerDownload(api.downloadURL(first.path))
        return
      }
      triggerDownload(api.zipURL(items.map((item) => item.path), baseName(path) || 'storix'))
    },
    [path],
  )

  /** showDetails opens the panel beside the listing, or the sheet over it. */
  const showDetails = useCallback(() => {
    app.setDetailsOpen(true)
    setSheetOpen(true)
  }, [app])

  const openEntry = useCallback(
    (entry: Entry) => {
      if (entry.isDir) {
        go(entry.path)
        return
      }
      if (entry.editable && can('edit')) {
        openEditor(entry)
        return
      }
      if (entry.previewable) {
        applySelection(new Set([entry.path]))
        setFocused(entry.path)
        showDetails()
        return
      }
      if (can('download')) download([entry])
    },
    [applySelection, can, download, go, openEditor, showDetails],
  )

  // ---- operations -----------------------------------------------------------

  const moveMutation = useMutation({
    mutationFn: (input: { sources: string[]; dest: string }) => api.move(input.sources, input.dest),
    onSuccess: (job, input) => {
      toast.info('Moving files', `${counted(input.sources.length, 'item')} into ${baseName(input.dest) || 'the folder'}`)
      if (job?.id) void queryClient.invalidateQueries({ queryKey: ['jobs'] })
      clearSelection()
      invalidateListing()
    },
    onError: (error) => toast.error('The move did not finish', reason(error)),
  })

  const copyMutation = useMutation({
    mutationFn: (input: { sources: string[]; dest: string }) => api.copy(input.sources, input.dest),
    onSuccess: (job, input) => {
      toast.info('Copying files', `${counted(input.sources.length, 'item')} into ${baseName(input.dest) || 'the folder'}`)
      if (job?.id) void queryClient.invalidateQueries({ queryKey: ['jobs'] })
      invalidateListing()
    },
    onError: (error) => toast.error('The copy did not finish', reason(error)),
  })

  const removeMutation = useMutation({
    mutationFn: (input: { paths: string[]; permanent: boolean }) => api.remove(input.paths, input.permanent),
    onSuccess: (result, input) => {
      if (result?.job) toast.info(input.permanent ? 'Deleting' : 'Moving to trash', counted(input.paths.length, 'item'))
      else if (input.permanent) toast.success(`Deleted ${counted(input.paths.length, 'item')}`)
      else toast.success(`Moved ${counted(input.paths.length, 'item')} to trash`)
      clearSelection()
      invalidateListing()
      void queryClient.invalidateQueries({ queryKey: ['trash'] })
      void queryClient.invalidateQueries({ queryKey: ['jobs'] })
    },
    onError: (error) => toast.error('Nothing was deleted', reason(error)),
  })

  const renameMutation = useMutation({
    mutationFn: (input: { path: string; name: string }) => api.rename(input.path, input.name),
    onSuccess: (entry) => {
      setRenamingPath(null)
      if (entry?.path) {
        applySelection(new Set([entry.path]))
        setFocused(entry.path)
      }
      invalidateListing()
    },
    onError: (error) => {
      setRenamingPath(null)
      toast.error('The name was not changed', reason(error))
    },
  })

  const favoriteMutation = useMutation({
    mutationFn: async (input: { path: string; pinned: boolean }) => {
      if (input.pinned) await api.removeFavorite(input.path)
      else await api.addFavorite(input.path)
    },
    onSuccess: (_result, input) => {
      toast.success(input.pinned ? 'Removed from favourites' : 'Added to favourites')
      void queryClient.invalidateQueries({ queryKey: ['favorites'] })
      invalidateListing()
    },
    onError: (error) => toast.error('Favourites did not change', reason(error)),
  })

  const trashSelection = useCallback(
    (paths: string[]) => {
      if (paths.length === 0 || !canDelete) return
      removeMutation.mutate({ paths, permanent: false })
    },
    [canDelete, removeMutation],
  )

  const copyToClipboard = useCallback(
    (mode: 'copy' | 'cut') => {
      if (selectedPaths.length === 0) return
      app.setClipboard({ paths: selectedPaths, mode })
      toast.info(mode === 'copy' ? 'Copied' : 'Cut', `${counted(selectedPaths.length, 'item')} ready to paste`)
    },
    [app, selectedPaths, toast],
  )

  const paste = useCallback(() => {
    const clipboard = app.clipboard
    if (!clipboard || clipboard.paths.length === 0 || !path || readOnly) return
    if (clipboard.mode === 'cut') {
      moveMutation.mutate({ sources: clipboard.paths, dest: path })
      app.setClipboard(null)
      return
    }
    copyMutation.mutate({ sources: clipboard.paths, dest: path })
  }, [app, copyMutation, moveMutation, path, readOnly])

  const copyPath = useCallback(
    (value: string) => {
      const write = navigator.clipboard?.writeText(value)
      if (write) {
        void write.then(
          () => toast.success('Path copied'),
          () => toast.error('The path could not be copied'),
        )
        return
      }
      toast.error('The path could not be copied')
    },
    [toast],
  )

  const startRename = useCallback(() => {
    const target = detailEntry ?? selectedEntries[0]
    if (!target || !can('rename') || readOnly) return
    setRenamingPath(target.path)
  }, [can, detailEntry, readOnly, selectedEntries])

  const handleDetailAction = useCallback(
    (action: string, entry: Entry) => {
      switch (action) {
        case 'open':
          openEntry(entry)
          break
        case 'download':
          download([entry])
          break
        case 'compress':
          applySelection(new Set([entry.path]))
          setCompressOpen(true)
          break
        case 'share':
          setShareTarget(entry)
          break
        case 'rename':
          setRenameTarget(entry)
          break
        case 'delete':
          trashSelection([entry.path])
          break
        case 'copy':
          setPicker({ mode: 'copy', sources: [entry.path] })
          break
        case 'move':
          setPicker({ mode: 'move', sources: [entry.path] })
          break
        case 'edit':
          openEditor(entry)
          break
        case 'extract':
          setExtractTarget(entry)
          break
        default:
          break
      }
    },
    [applySelection, download, openEditor, openEntry, trashSelection],
  )

  // ---- drag and drop --------------------------------------------------------

  const handleDragStartRow = useCallback(
    (entry: Entry, event: DragEvent) => {
      let paths = selectedPaths
      if (!selected.has(entry.path)) {
        paths = [entry.path]
        applySelection(new Set(paths))
        setAnchor(entry.path)
        setFocused(entry.path)
      }
      event.dataTransfer.setData(STORIX_DRAG_TYPE, JSON.stringify(paths))
      event.dataTransfer.setData('text/plain', paths.join('\n'))
      event.dataTransfer.effectAllowed = 'copyMove'
    },
    [applySelection, selected, selectedPaths],
  )

  const dropPaths = useCallback(
    (dest: string, event: DragEvent) => {
      if (!dest) return
      let sources: string[] = []
      try {
        const raw = event.dataTransfer.getData(STORIX_DRAG_TYPE)
        sources = raw ? (JSON.parse(raw) as string[]) : []
      } catch {
        sources = []
      }
      sources = sources.filter((source) => source !== dest && parentPath(source) !== dest)
      if (sources.length === 0) return
      if (sources.some((source) => dest === source || dest.startsWith(source + '/'))) {
        toast.error('A folder cannot be moved into itself')
        return
      }
      const asCopy = event.ctrlKey || event.metaKey
      if (asCopy) {
        if (!can('copy')) return
        copyMutation.mutate({ sources, dest })
        return
      }
      if (!canMove) return
      moveMutation.mutate({ sources, dest })
    },
    [can, canMove, copyMutation, moveMutation, toast],
  )

  // ---- keyboard -------------------------------------------------------------

  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if (dialogOpen) return
      const target = event.target as HTMLElement | null
      if (target?.closest('input, textarea, select, [contenteditable="true"]')) return

      const meta = event.ctrlKey || event.metaKey
      const index = focused ? entries.findIndex((entry) => entry.path === focused) : -1

      if (meta && event.key.toLowerCase() === 'a') {
        event.preventDefault()
        selectAll()
        return
      }
      if (meta && event.key.toLowerCase() === 'c') {
        event.preventDefault()
        copyToClipboard('copy')
        return
      }
      if (meta && event.key.toLowerCase() === 'x') {
        event.preventDefault()
        copyToClipboard('cut')
        return
      }
      if (meta && event.key.toLowerCase() === 'v') {
        event.preventDefault()
        paste()
        return
      }
      switch (event.key) {
        case 'Escape':
          clearSelection()
          break
        case 'F2':
          event.preventDefault()
          startRename()
          break
        case 'Delete':
          event.preventDefault()
          if (selectedPaths.length === 0) break
          if (event.shiftKey) setConfirmDelete(selectedPaths)
          else trashSelection(selectedPaths)
          break
        case 'Enter': {
          const entry = index >= 0 ? entries[index] : selectedEntries[0]
          if (entry) {
            event.preventDefault()
            openEntry(entry)
          }
          break
        }
        case 'Backspace':
          event.preventDefault()
          if (listing?.parent) go(listing.parent)
          else if (path) go('')
          break
        case 'ArrowDown':
        case 'ArrowRight':
          event.preventDefault()
          focusIndex(index < 0 ? 0 : index + 1, event.shiftKey)
          break
        case 'ArrowUp':
        case 'ArrowLeft':
          event.preventDefault()
          focusIndex(index < 0 ? 0 : index - 1, event.shiftKey)
          break
        case 'F5':
          event.preventDefault()
          void (searching ? searchQuery.refetch() : listQuery.refetch())
          break
        case 'Home':
          event.preventDefault()
          focusIndex(0, event.shiftKey)
          break
        case 'End':
          event.preventDefault()
          focusIndex(entries.length - 1, event.shiftKey)
          break
        default:
          break
      }
    }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [
    clearSelection,
    copyToClipboard,
    dialogOpen,
    entries,
    focusIndex,
    focused,
    go,
    listQuery,
    listing,
    openEntry,
    paste,
    path,
    searchQuery,
    searching,
    selectAll,
    selectedEntries,
    selectedPaths,
    startRename,
    trashSelection,
  ])

  // ---- menus ----------------------------------------------------------------

  const sortItems = useCallback((): MenuItem[] => {
    const option = (field: SortField, label: string): MenuItem => ({
      id: `sort-${field}`,
      label,
      icon: app.sort === field ? 'check' : undefined,
      onSelect: () => app.setSort(field),
    })
    return [option('name', 'Sort by name'), option('size', 'Sort by size'), option('modified', 'Sort by modified')]
  }, [app])

  const viewItems = useCallback((): MenuItem[] => {
    const option = (mode: 'list' | 'grid' | 'gallery', label: string): MenuItem => ({
      id: `view-${mode}`,
      label,
      icon: app.view === mode ? 'check' : undefined,
      onSelect: () => app.setView(mode),
    })
    return [option('list', 'List view'), option('grid', 'Grid view'), option('gallery', 'Gallery view')]
  }, [app])

  const entryMenuItems = useCallback(
    (entry: Entry): MenuItem[] => {
      const many = selectedPaths.length > 1
      const targets = selected.has(entry.path) ? selectedEntries : [entry]
      const targetPaths = targets.map((item) => item.path)
      return [
        { id: 'open', label: 'Open', icon: 'external', onSelect: () => openEntry(entry) },
        {
          id: 'preview',
          label: 'Preview',
          icon: 'eye',
          disabled: entry.isDir || !entry.previewable,
          onSelect: () => {
            applySelection(new Set([entry.path]))
            setFocused(entry.path)
            showDetails()
          },
        },
        {
          id: 'download',
          label: many ? 'Download as zip' : 'Download',
          icon: 'download',
          disabled: !can('download'),
          onSelect: () => download(targets),
        },
        { id: 'd1', label: '', divider: true },
        {
          id: 'rename',
          label: 'Rename',
          icon: 'edit',
          shortcut: 'F2',
          disabled: !can('rename') || readOnly || many,
          onSelect: () => setRenamingPath(entry.path),
        },
        {
          id: 'rename-many',
          label: 'Rename many',
          icon: 'edit',
          disabled: !can('rename') || readOnly || !many,
          onSelect: () => setBulkRenameOpen(true),
        },
        {
          id: 'copy',
          label: 'Copy',
          icon: 'copy',
          shortcut: 'Ctrl C',
          disabled: !can('copy'),
          onSelect: () => copyToClipboard('copy'),
        },
        {
          id: 'cut',
          label: 'Cut',
          icon: 'scissors',
          shortcut: 'Ctrl X',
          disabled: !canMove,
          onSelect: () => copyToClipboard('cut'),
        },
        {
          id: 'paste',
          label: 'Paste',
          icon: 'clipboard',
          shortcut: 'Ctrl V',
          disabled: !app.clipboard || readOnly,
          onSelect: paste,
        },
        { id: 'd2', label: '', divider: true },
        {
          id: 'move-to',
          label: 'Move to',
          icon: 'move',
          disabled: !canMove,
          onSelect: () => setPicker({ mode: 'move', sources: targetPaths }),
        },
        {
          id: 'copy-to',
          label: 'Copy to',
          icon: 'folder',
          disabled: !can('copy'),
          onSelect: () => setPicker({ mode: 'copy', sources: targetPaths }),
        },
        {
          id: 'share',
          label: 'Share',
          icon: 'share',
          disabled: !can('share') || many,
          onSelect: () => setShareTarget(entry),
        },
        {
          id: 'compress',
          label: 'Compress',
          icon: 'archive',
          disabled: !can('archive') || readOnly,
          onSelect: () => {
            applySelection(new Set(targetPaths))
            setCompressOpen(true)
          },
        },
        {
          id: 'extract',
          label: 'Extract',
          icon: 'folder-open',
          disabled: !can('archive') || readOnly || many || !isArchive(entry),
          onSelect: () => setExtractTarget(entry),
        },
        { id: 'copy-path', label: 'Copy path', icon: 'link', onSelect: () => copyPath(entry.path) },
        {
          id: 'properties',
          label: 'Properties',
          icon: 'info',
          onSelect: () => setPropertiesTarget(entry),
        },
        { id: 'd3', label: '', divider: true },
        {
          id: 'trash',
          label: 'Move to trash',
          icon: 'trash',
          shortcut: 'Del',
          danger: true,
          disabled: !canDelete,
          onSelect: () => trashSelection(targetPaths),
        },
      ]
    },
    [
      app,
      applySelection,
      can,
      canDelete,
      canMove,
      copyPath,
      copyToClipboard,
      download,
      openEntry,
      paste,
      readOnly,
      selected,
      selectedEntries,
      selectedPaths,
      showDetails,
      trashSelection,
    ],
  )

  const emptyMenuItems = useCallback(
    (): MenuItem[] => [
      {
        id: 'new-folder',
        label: 'New folder',
        icon: 'folder-plus',
        disabled: !canCreate,
        onSelect: () => setNewFolderOpen(true),
      },
      {
        id: 'new-file',
        label: 'New file',
        icon: 'file-plus',
        disabled: !canCreate,
        onSelect: () => setNewFileOpen(true),
      },
      {
        id: 'upload',
        label: 'Upload files',
        icon: 'cloud-upload',
        disabled: !canUpload,
        onSelect: () => fileInput.current?.click(),
      },
      {
        id: 'paste',
        label: 'Paste',
        icon: 'clipboard',
        shortcut: 'Ctrl V',
        disabled: !app.clipboard || readOnly,
        onSelect: paste,
      },
      { id: 'select-all', label: 'Select all', icon: 'check', shortcut: 'Ctrl A', onSelect: selectAll },
      { id: 'refresh', label: 'Refresh', icon: 'refresh', onSelect: () => void listQuery.refetch() },
      { id: 'd1', label: '', divider: true },
      ...sortItems(),
      { id: 'd2', label: '', divider: true },
      ...viewItems(),
    ],
    [app.clipboard, canCreate, canUpload, listQuery, paste, readOnly, selectAll, sortItems, viewItems],
  )

  const moreMenuItems = useCallback(
    (): MenuItem[] => [
      {
        id: 'rename-many',
        label: 'Rename many',
        icon: 'edit',
        disabled: !can('rename') || readOnly || selectedPaths.length < 2,
        onSelect: () => setBulkRenameOpen(true),
      },
      {
        id: 'compress',
        label: 'Compress',
        icon: 'archive',
        disabled: !can('archive') || readOnly || selectedPaths.length === 0,
        onSelect: () => setCompressOpen(true),
      },
      {
        id: 'extract',
        label: 'Extract',
        icon: 'folder-open',
        disabled: !can('archive') || readOnly || !singleArchive,
        onSelect: () => {
          const target = selectedEntries[0]
          if (target) setExtractTarget(target)
        },
      },
      {
        id: 'share',
        label: 'Share',
        icon: 'share',
        disabled: !can('share') || selectedEntries.length !== 1,
        onSelect: () => {
          const target = selectedEntries[0]
          if (target) setShareTarget(target)
        },
      },
      {
        id: 'properties',
        label: 'Properties',
        icon: 'info',
        disabled: selectedEntries.length !== 1,
        onSelect: () => {
          const target = selectedEntries[0]
          if (target) setPropertiesTarget(target)
        },
      },
      {
        id: 'copy-path',
        label: 'Copy path',
        icon: 'link',
        onSelect: () => copyPath(selectedPaths[0] ?? path),
      },
      { id: 'd1', label: '', divider: true },
      { id: 'select-all', label: 'Select all', icon: 'check', shortcut: 'Ctrl A', onSelect: selectAll },
      {
        id: 'hidden',
        label: app.showHidden ? 'Hide hidden files' : 'Show hidden files',
        icon: app.showHidden ? 'eye-off' : 'eye',
        onSelect: () => app.toggleHidden(),
      },
      { id: 'refresh', label: 'Refresh', icon: 'refresh', shortcut: 'F5', onSelect: () => void listQuery.refetch() },
    ],
    [app, can, copyPath, listQuery, path, readOnly, selectAll, selectedEntries, selectedPaths, singleArchive],
  )

  const handleContextMenu = useCallback(
    (entry: Entry | null, event: { clientX: number; clientY: number; preventDefault: () => void }) => {
      event.preventDefault()
      if (entry && !selected.has(entry.path)) {
        applySelection(new Set([entry.path]))
        setAnchor(entry.path)
        setFocused(entry.path)
      }
      setMenu({ x: event.clientX, y: event.clientY, entry })
    },
    [applySelection, selected],
  )

  // ---- render ---------------------------------------------------------------

  const viewProps = {
    entries,
    selected,
    focused,
    onSelect: handleSelect,
    onOpen: openEntry,
    onContextMenu: handleContextMenu,
    onDragStartRow: handleDragStartRow,
    onDropOnFolder: (entry: Entry, event: DragEvent) => dropPaths(entry.path, event),
    sort: app.sort,
    order: app.order,
    onSort: (field: SortField) => app.setSort(field),
    renamingPath,
    onRenameCommit: (entry: Entry, name: string) => renameMutation.mutate({ path: entry.path, name }),
    onRenameCancel: () => setRenamingPath(null),
    loading,
    showPath: searching,
    onMarqueeSelect: handleMarquee,
  }

  const listingBody = () => {
    if (isRoot) {
      const mounts = listing?.mounts ?? []
      if (listQuery.isLoading) return <MountSkeleton />
      if (mounts.length === 0) {
        return (
          <Centered>
            <EmptyState
              icon="drive"
              title="No folders are available yet"
              message="An administrator decides which folders of this server you can open."
            />
          </Centered>
        )
      }
      return <MountPicker mounts={mounts} onOpen={go} />
    }
    if (failure) {
      return (
        <Centered>
          <EmptyState
            icon="alert"
            title="This folder could not be opened"
            message={reason(failure)}
            action={
              <Button icon="refresh" onClick={() => void (searching ? searchQuery.refetch() : listQuery.refetch())}>
                Try again
              </Button>
            }
          />
        </Centered>
      )
    }
    if (!loading && entries.length === 0) {
      if (searching) {
        return (
          <Centered>
            <EmptyState
              icon="search"
              title="No matches"
              message={`Nothing here is called ${searchTerm}. Try a shorter word.`}
              action={<Button onClick={() => go(path)}>Clear the search</Button>}
            />
          </Centered>
        )
      }
      return (
        <Centered>
          <EmptyState
            icon="folder-open"
            title="This folder is empty"
            message={
              canUpload ? 'Drop files here, or use the upload button above.' : 'There is nothing to show here yet.'
            }
            action={
              canCreate ? (
                <div className="flex gap-2">
                  <Button icon="folder-plus" onClick={() => setNewFolderOpen(true)}>
                    New folder
                  </Button>
                  {canUpload && (
                    <Button variant="primary" icon="cloud-upload" onClick={() => fileInput.current?.click()}>
                      Upload files
                    </Button>
                  )}
                </div>
              ) : undefined
            }
          />
        </Centered>
      )
    }
    if (app.view === 'grid') return <FileGrid {...viewProps} />
    if (app.view === 'gallery') return <FileGallery {...viewProps} />
    return <FileList {...viewProps} />
  }

  return (
    <div className="flex h-full min-h-0 w-full">
      <div className="flex min-w-0 flex-1 flex-col p-3 sm:p-4">
        {/* location. On a phone the trail keeps the first line to itself. */}
        <div className="mb-3 flex flex-wrap items-center gap-2">
          <Breadcrumbs
            crumbs={crumbs}
            onNavigate={go}
            onDropOnCrumb={(target, event) => dropPaths(target, event)}
            className="basis-full flex-1 sm:basis-0"
          />
          <div className="flex min-w-0 flex-1 items-center justify-end gap-2 sm:flex-none">
            {listQuery.isFetching && <Spinner size={14} className="text-faint" />}
            {!isRoot && (
              <IconButton
                icon={listing?.favorite ? 'star-filled' : 'star'}
                label={listing?.favorite ? 'Remove this folder from favourites' : 'Add this folder to favourites'}
                className="sx-touch"
                active={listing?.favorite}
                onClick={() => favoriteMutation.mutate({ path, pinned: listing?.favorite === true })}
              />
            )}
            <IconButton
              icon="refresh"
              label="Refresh"
              className="sx-touch"
              onClick={() => void listQuery.refetch()}
            />
            <div className="flex items-center gap-0.5 rounded-xl border border-line bg-elevated p-0.5">
              <IconButton
                icon="list"
                label="List view"
                size={16}
                className="sx-touch h-7 w-7"
                active={app.view === 'list'}
                onClick={() => app.setView('list')}
              />
              <IconButton
                icon="grid"
                label="Grid view"
                size={16}
                className="sx-touch h-7 w-7"
                active={app.view === 'grid'}
                onClick={() => app.setView('grid')}
              />
              <IconButton
                icon="gallery"
                label="Gallery view"
                size={16}
                className="sx-touch h-7 w-7"
                active={app.view === 'gallery'}
                onClick={() => app.setView('gallery')}
              />
            </div>
          </div>
        </div>

        {/* toolbar. Under 768px it is one scrolling row of icons, and Upload is
            the only action that keeps its word. */}
        {!isRoot && (
          <div className="no-scrollbar flex shrink-0 items-center gap-2 overflow-x-auto md:flex-wrap md:overflow-x-visible">
            <Button
              variant="primary"
              icon="cloud-upload"
              iconRight="chevron-down"
              className="shrink-0"
              disabled={!canUpload}
              onClick={(event) => {
                const rect = event.currentTarget.getBoundingClientRect()
                setToolMenu({ x: rect.left, y: rect.bottom + 6, kind: 'upload' })
              }}
            >
              Upload
            </Button>
            {/* Below 1024px the sheet is the only way to reach the details, so
                the button that opens it stays in reach without scrolling. */}
            {compact && (
              <Button
                icon="info"
                title="Details"
                aria-label="Details"
                className="shrink-0"
                onClick={() => (sheetOpen ? setSheetOpen(false) : showDetails())}
              />
            )}
            <Button
              icon="download"
              title="Download"
              aria-label="Download"
              className="shrink-0"
              disabled={!can('download') || selectedPaths.length === 0}
              onClick={() => download(selectedEntries)}
            >
              <span className="hidden md:inline">Download</span>
            </Button>
            <Button
              icon="folder-plus"
              title="New folder"
              aria-label="New folder"
              className="shrink-0"
              disabled={!canCreate}
              onClick={() => setNewFolderOpen(true)}
            >
              <span className="hidden md:inline">New folder</span>
            </Button>
            <Button
              icon="move"
              title="Move"
              aria-label="Move"
              className="shrink-0"
              disabled={!canMove || selectedPaths.length === 0}
              onClick={() => setPicker({ mode: 'move', sources: selectedPaths })}
            >
              <span className="hidden md:inline">Move</span>
            </Button>
            <Button
              icon="copy"
              title="Copy"
              aria-label="Copy"
              className="shrink-0"
              disabled={!can('copy') || selectedPaths.length === 0}
              onClick={() => setPicker({ mode: 'copy', sources: selectedPaths })}
            >
              <span className="hidden md:inline">Copy</span>
            </Button>
            <Button
              variant="danger"
              icon="trash"
              title="Delete"
              aria-label="Delete"
              className="shrink-0"
              disabled={!canDelete || selectedPaths.length === 0}
              onClick={() => trashSelection(selectedPaths)}
            >
              <span className="hidden md:inline">Delete</span>
            </Button>
            <div className="hidden flex-1 md:block" />
            <Button
              icon="more"
              title="More"
              aria-label="More"
              className="shrink-0"
              onClick={(event) => {
                const rect = event.currentTarget.getBoundingClientRect()
                setToolMenu({ x: rect.right, y: rect.bottom + 6, kind: 'more' })
              }}
            >
              <span className="hidden md:inline">More</span>
            </Button>
          </div>
        )}

        {/* selection summary */}
        {selectedPaths.length > 0 && (
          <div className="mt-3 flex flex-wrap items-center gap-x-3 gap-y-1 rounded-xl border border-primary/30 bg-primary/10 px-3 py-2">
            <Icon name="check-circle" size={16} className="shrink-0 text-primary" />
            <span className="text-sm text-ink">{counted(selectedPaths.length, 'item')} selected</span>
            {selectedBytes > 0 && <span className="text-sm text-muted">{bytes(selectedBytes)}</span>}
            <div className="flex-1" />
            <Button variant="ghost" className="shrink-0" onClick={selectAll}>
              Select all
            </Button>
            <Button variant="ghost" icon="close" className="shrink-0" onClick={clearSelection}>
              Clear
            </Button>
          </div>
        )}

        {/* search banner */}
        {searching && (
          <div className="mt-3 flex items-center gap-3 rounded-xl border border-line bg-elevated px-3 py-2">
            <Icon name="search" size={16} className="shrink-0 text-faint" />
            <span className="min-w-0 flex-1 truncate text-sm text-ink">Results for {searchTerm}</span>
            {searchQuery.data?.truncated && <span className="sx-chip hidden sm:inline-flex">First 500 shown</span>}
            <Button variant="ghost" icon="close" className="shrink-0" onClick={() => go(path)}>
              Clear
            </Button>
          </div>
        )}

        {/* listing. The dock sits at the foot of a narrow screen, so the last
            row gets room to stay clear of it. */}
        <div
          className={
            'sx-panel relative mt-3 flex min-h-0 flex-1 flex-col overflow-hidden' + (docked ? ' sx-dock-gap' : '')
          }
          onContextMenu={(event) => {
            if (isRoot) return
            if ((event.target as HTMLElement).closest('[data-row]')) return
            handleContextMenu(null, event)
          }}
        >
          {listingBody()}
        </div>
      </div>

      {app.detailsOpen && !isRoot && !compact && (
        <div className="h-full shrink-0">
          <DetailsPanel
            entry={detailEntry}
            entries={selectedEntries}
            onClose={() => app.setDetailsOpen(false)}
            onAction={handleDetailAction}
          />
        </div>
      )}

      {sheetOpen && !isRoot && compact && (
        <>
          <div
            aria-hidden="true"
            className="fixed inset-0 z-[44] animate-fade-in bg-black/55 backdrop-blur-[2px]"
            onClick={() => setSheetOpen(false)}
          />
          <DetailsPanel
            sheet
            entry={detailEntry}
            entries={selectedEntries}
            onClose={() => setSheetOpen(false)}
            onAction={handleDetailAction}
          />
        </>
      )}

      {/* hidden upload inputs */}
      <input
        ref={fileInput}
        type="file"
        multiple
        className="hidden"
        onChange={(event) => {
          const files = fromInput(event.target.files)
          if (files.length > 0) {
            enqueue(files, path)
            app.setTransfersOpen(true)
          }
          event.target.value = ''
        }}
      />
      <input
        ref={folderInput}
        type="file"
        multiple
        className="hidden"
        onChange={(event) => {
          const files = fromInput(event.target.files)
          if (files.length > 0) {
            enqueue(files, path)
            app.setTransfersOpen(true)
          }
          event.target.value = ''
        }}
      />

      {/* menus */}
      {menu && (
        <Menu
          items={menu.entry ? entryMenuItems(menu.entry) : emptyMenuItems()}
          x={menu.x}
          y={menu.y}
          onClose={() => setMenu(null)}
        />
      )}
      {toolMenu && (
        <Menu
          items={
            toolMenu.kind === 'upload'
              ? [
                  {
                    id: 'files',
                    label: 'Upload files',
                    icon: 'file-plus',
                    onSelect: () => fileInput.current?.click(),
                  },
                  {
                    id: 'folder',
                    label: 'Upload a folder',
                    icon: 'folder-plus',
                    onSelect: () => folderInput.current?.click(),
                  },
                ]
              : moreMenuItems()
          }
          x={toolMenu.x}
          y={toolMenu.y}
          anchorRight={toolMenu.kind === 'more'}
          onClose={() => setToolMenu(null)}
        />
      )}

      {/* dialogs */}
      <NewFolderDialog
        open={newFolderOpen}
        path={path}
        onClose={() => setNewFolderOpen(false)}
        onCreated={() => {
          setNewFolderOpen(false)
          invalidateListing()
        }}
      />
      <NewFileDialog
        open={newFileOpen}
        path={path}
        onClose={() => setNewFileOpen(false)}
        onCreated={(entry: Entry) => {
          setNewFileOpen(false)
          invalidateListing()
          if (entry?.path) {
            applySelection(new Set([entry.path]))
            setFocused(entry.path)
          }
        }}
      />
      {renameTarget && (
        <RenameDialog
          open
          entry={renameTarget}
          onClose={() => setRenameTarget(null)}
          onDone={() => {
            setRenameTarget(null)
            invalidateListing()
          }}
        />
      )}
      {bulkRenameOpen && (
        <BulkRenameDialog
          open
          entries={selectedEntries}
          onClose={() => setBulkRenameOpen(false)}
          onDone={() => {
            clearSelection()
            invalidateListing()
          }}
        />
      )}
      {picker && (
        <PathPickerDialog
          open
          title={picker.mode === 'move' ? 'Move to' : 'Copy to'}
          confirmLabel={picker.mode === 'move' ? 'Move here' : 'Copy here'}
          initialPath={path}
          onClose={() => setPicker(null)}
          onPick={(dest: string) => {
            const sources = picker.sources
            setPicker(null)
            if (sources.length === 0) return
            if (picker.mode === 'move') moveMutation.mutate({ sources, dest })
            else copyMutation.mutate({ sources, dest })
          }}
        />
      )}
      {compressOpen && (
        <CompressDialog
          open
          sources={selectedPaths}
          dest={path}
          onClose={() => setCompressOpen(false)}
          onStarted={() => {
            setCompressOpen(false)
            toast.info('Creating the archive', 'Progress appears in transfers')
            invalidateListing()
            void queryClient.invalidateQueries({ queryKey: ['jobs'] })
          }}
        />
      )}
      {extractTarget && (
        <ExtractDialog
          open
          path={extractTarget.path}
          onClose={() => setExtractTarget(null)}
          onStarted={() => {
            setExtractTarget(null)
            toast.info('Extracting the archive', 'Progress appears in transfers')
            invalidateListing()
            void queryClient.invalidateQueries({ queryKey: ['jobs'] })
          }}
        />
      )}
      {propertiesTarget && (
        <PropertiesDialog
          open
          entry={propertiesTarget}
          onClose={() => setPropertiesTarget(null)}
          onChanged={() => invalidateListing()}
        />
      )}
      {shareTarget && (
        <ShareDialog
          open
          path={shareTarget.path}
          isDir={shareTarget.isDir}
          onClose={() => setShareTarget(null)}
          onCreated={() => void queryClient.invalidateQueries({ queryKey: ['shares'] })}
        />
      )}
      {editorPath && (
        <div className="fixed inset-0 z-[55] flex animate-fade-in items-center justify-center bg-black/55 p-4 backdrop-blur-[2px]">
          <div className="sx-panel flex h-full w-full max-w-6xl animate-slide-up flex-col overflow-hidden">
            <Suspense
              fallback={
                <div className="flex h-full w-full items-center justify-center gap-2 text-sm text-faint">
                  <Spinner size={16} className="text-primary" />
                  Opening the editor
                </div>
              }
            >
              <CodeEditor
                path={editorPath}
                onClose={() => setEditorPath(null)}
                onSaved={() => invalidateListing()}
              />
            </Suspense>
          </div>
        </div>
      )}

      <ConfirmDialog
        open={confirmDelete !== null}
        danger
        title="Delete permanently"
        confirmLabel="Delete permanently"
        message={
          <>
            {counted(confirmDelete?.length ?? 0, 'item')} will be removed from the disk straight away, without going to
            the trash. This cannot be undone.
          </>
        }
        busy={removeMutation.isPending}
        onCancel={() => setConfirmDelete(null)}
        onConfirm={() => {
          const paths = confirmDelete ?? []
          setConfirmDelete(null)
          if (paths.length > 0) removeMutation.mutate({ paths, permanent: true })
        }}
      />
    </div>
  )
}

// ---- small pieces ------------------------------------------------------------

/** Centered fills the listing panel so a message sits in the middle of it. */
function Centered({ children }: { children: ReactNode }) {
  return <div className="flex min-h-0 flex-1 items-center justify-center">{children}</div>
}

// ---- mount picker ------------------------------------------------------------

function MountSkeleton() {
  return (
    <div className="grid gap-3 p-4 sm:grid-cols-2 xl:grid-cols-3">
      {Array.from({ length: 6 }).map((_, index) => (
        <div key={index} className="h-20 rounded-2xl border border-line/70 bg-elevated" />
      ))}
    </div>
  )
}

function MountPicker({ mounts, onOpen }: { mounts: Mount[]; onOpen: (path: string) => void }) {
  return (
    <div className="sx-scroll min-h-0 flex-1 p-4">
      <p className="mb-3 text-sm text-muted">Choose where to start.</p>
      <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
        {mounts.map((mount) => (
          <button
            key={mount.path}
            type="button"
            onClick={() => onOpen(mount.path)}
            className="flex items-center gap-3 rounded-2xl border border-line bg-elevated p-4 text-left transition-colors hover:border-primary/45 hover:bg-line/40"
          >
            <span className="flex h-11 w-11 shrink-0 items-center justify-center rounded-xl bg-primary/12 text-primary">
              <Icon name={mountIcon(mount.icon)} size={21} />
            </span>
            <span className="min-w-0 flex-1">
              <span className="block truncate text-sm font-medium text-ink">{mount.label}</span>
              <span className="block truncate font-mono text-xs text-faint">{mount.path}</span>
            </span>
            {mount.readOnly && <span className="sx-chip shrink-0">Read only</span>}
          </button>
        ))}
      </div>
    </div>
  )
}
