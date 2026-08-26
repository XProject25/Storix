// The right hand inspector. It answers "what is this file" in plain words
// first and keeps the Unix facts one click away.
// Developed by X Project.

import clsx from 'clsx'
import {
  useCallback,
  useMemo,
  useRef,
  useState,
  type KeyboardEvent as ReactKeyboardEvent,
  type PointerEvent as ReactPointerEvent,
  type ReactNode,
} from 'react'
import { useQuery } from '@tanstack/react-query'
import { api } from '../lib/api'
import { bytes, counted, dateTime, modeToText, parentPath, smartDate, truncateMiddle } from '../lib/format'
import { useSession } from '../lib/session'
import type { Entry, Kind } from '../lib/types'
import { useApp } from '../state/app'
import { Icon, colourForKind, iconForKind } from './Icon'
import { Button, EmptyState, IconButton, Menu, Skeleton, Spinner, type MenuItem } from './ui'
import PreviewViewer, { KIND_LABELS, kindLabel } from './PreviewViewer'

export interface DetailsPanelProps {
  entry: Entry | null
  entries: Entry[]
  onClose: () => void
  onAction: (action: string, entry: Entry) => void
}

type TabId = 'details' | 'preview' | 'activity' | 'shares'

const MIN_WIDTH = 280
const MAX_WIDTH = 560

// ---- small pieces -----------------------------------------------------------

function Row({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="flex items-start gap-3 py-1.5">
      <span className="w-24 shrink-0 text-xs text-faint">{label}</span>
      <span className="min-w-0 flex-1 text-xs text-ink">{children}</span>
    </div>
  )
}

function Mono({ children }: { children: ReactNode }) {
  return <span className="font-mono text-[11px] text-ink">{children}</span>
}

function LoadingRows({ rows = 5 }: { rows?: number }) {
  return (
    <div className="space-y-3 p-4">
      {Array.from({ length: rows }, (_, index) => (
        <div key={index} className="flex items-center gap-3">
          <Skeleton className="h-3 w-20 shrink-0" />
          <Skeleton className="h-3 flex-1" />
        </div>
      ))}
    </div>
  )
}

function ErrorNote({ title, message, onRetry }: { title: string; message?: string; onRetry?: () => void }) {
  return (
    <div className="flex flex-col items-center gap-2.5 px-4 py-10 text-center">
      <span className="flex h-11 w-11 items-center justify-center rounded-2xl bg-danger/12 text-danger">
        <Icon name="alert" size={20} />
      </span>
      <h3 className="text-sm font-medium text-ink">{title}</h3>
      {message && <p className="text-xs text-muted">{message}</p>}
      {onRetry && (
        <Button icon="refresh" onClick={onRetry}>
          Try again
        </Button>
      )}
    </div>
  )
}

// ---- multiple selection -----------------------------------------------------

function SelectionSummary({ entries }: { entries: Entry[] }) {
  const summary = useMemo(() => {
    let total = 0
    let folders = 0
    const kinds = new Map<Kind, number>()
    for (const item of entries) {
      total += item.size
      if (item.isDir) folders += 1
      const key: Kind = item.isDir ? 'folder' : item.kind
      kinds.set(key, (kinds.get(key) ?? 0) + 1)
    }
    const breakdown = Array.from(kinds.entries()).sort((a, b) => b[1] - a[1])
    return { total, folders, breakdown }
  }, [entries])

  return (
    <div className="sx-scroll min-h-0 flex-1 px-4 py-4">
      <div className="flex flex-col items-center gap-2 rounded-2xl bg-elevated px-4 py-6 text-center">
        <span className="flex h-12 w-12 items-center justify-center rounded-2xl bg-surface text-primary">
          <Icon name="copy" size={22} />
        </span>
        <p className="text-sm font-medium text-ink">{counted(entries.length, 'item')} selected</p>
        <p className="text-xs text-muted">
          {bytes(summary.total)}
          {summary.folders > 0 ? `, folder sizes not counted` : ''}
        </p>
      </div>

      <div className="mt-5">
        <h3 className="mb-2 text-[11px] font-semibold uppercase tracking-[0.14em] text-faint">What is selected</h3>
        <div className="space-y-1">
          {summary.breakdown.map(([kind, count]) => (
            <div key={kind} className="flex items-center gap-3 rounded-xl px-2 py-1.5">
              <span
                className={clsx(
                  'flex h-7 w-7 shrink-0 items-center justify-center rounded-lg bg-elevated',
                  colourForKind(kind, kind === 'folder'),
                )}
              >
                <Icon name={iconForKind(kind, kind === 'folder')} size={14} />
              </span>
              <span className="min-w-0 flex-1 truncate text-xs text-ink">{KIND_LABELS[kind]}</span>
              <span className="text-xs text-muted">{count.toLocaleString()}</span>
            </div>
          ))}
        </div>
      </div>

      {summary.folders > 0 && (
        <p className="mt-4 text-xs text-faint">
          Folder sizes are worked out one at a time. Select a single folder to measure it.
        </p>
      )}
    </div>
  )
}

// ---- tabs -------------------------------------------------------------------

function DetailsTab({ entry, active }: { entry: Entry; active: boolean }) {
  const [advanced, setAdvanced] = useState(false)

  const usage = useQuery({
    queryKey: ['du', entry.path],
    queryFn: () => api.du(entry.path),
    enabled: active && entry.isDir,
    staleTime: 60_000,
  })

  const location = parentPath(entry.path) || '/'

  let size: ReactNode
  if (!entry.isDir) {
    size = bytes(entry.size)
  } else if (usage.isFetching) {
    size = (
      <span className="inline-flex items-center gap-2 text-muted">
        <Spinner size={13} />
        Measuring
      </span>
    )
  } else if (usage.isError) {
    size = (
      <button type="button" className="text-primary hover:underline" onClick={() => void usage.refetch()}>
        Could not measure, try again
      </button>
    )
  } else if (usage.data) {
    size = `${bytes(usage.data.bytes)} in ${counted(usage.data.items, 'item')}`
  } else {
    size = 'Not measured yet'
  }

  return (
    <div className="px-4 py-3">
      <Row label="Type">
        {kindLabel(entry)}
        {entry.ext && !entry.isDir ? ` (.${entry.ext})` : ''}
      </Row>
      <Row label="Size">{size}</Row>
      <Row label="Location">
        <span className="block break-words" title={location}>
          {truncateMiddle(location, 40)}
        </span>
      </Row>
      <Row label="Modified">
        <span title={dateTime(entry.modified)}>{smartDate(entry.modified)}</span>
      </Row>

      <div className="mt-3 rounded-xl bg-elevated px-3 py-2.5">
        <h3 className="text-[11px] font-semibold uppercase tracking-[0.14em] text-faint">Who can access this</h3>
        <p className="mt-1.5 text-xs text-ink">{modeToText(entry.modeOctal)}</p>
        {entry.readOnly && <p className="mt-1.5 text-xs text-warning">You can read this but not change it.</p>}
      </div>

      <div className="mt-3 border-t border-line pt-2">
        <button
          type="button"
          aria-expanded={advanced}
          onClick={() => setAdvanced((value) => !value)}
          className="flex w-full items-center gap-2 rounded-xl px-1 py-2 text-xs text-muted transition-colors hover:text-ink"
        >
          <Icon name={advanced ? 'chevron-down' : 'chevron-right'} size={15} className="shrink-0" />
          Advanced
        </button>

        {advanced && (
          <div className="mt-1 rounded-xl bg-elevated px-3 py-2">
            <Row label="Owner">
              <Mono>
                {entry.owner || 'unknown'} ({entry.uid})
              </Mono>
            </Row>
            <Row label="Group">
              <Mono>
                {entry.group || 'unknown'} ({entry.gid})
              </Mono>
            </Row>
            <Row label="Permissions">
              <Mono>
                {entry.mode} {entry.modeOctal}
              </Mono>
            </Row>
            <Row label="Full path">
              <Mono>
                <span className="block break-all">{entry.path}</span>
              </Mono>
            </Row>
            <Row label="Media type">
              <Mono>{entry.mime || 'unknown'}</Mono>
            </Row>
            <Row label="Hidden">{entry.hidden ? 'Yes' : 'No'}</Row>
            {entry.symlink && (
              <Row label="Points at">
                <Mono>
                  <span className="block break-all">{entry.linkTarget || 'unknown'}</span>
                </Mono>
                {entry.broken && <span className="mt-1 block text-xs text-danger">The target is missing.</span>}
              </Row>
            )}
          </div>
        )}
      </div>
    </div>
  )
}

function ActivityTab({ entry, active }: { entry: Entry; active: boolean }) {
  const query = useQuery({
    queryKey: ['audit', entry.path],
    queryFn: () => api.audit({ q: entry.path, limit: 25 }),
    enabled: active,
  })

  if (query.isPending) return <LoadingRows rows={6} />
  if (query.isError) {
    return (
      <ErrorNote
        title="Activity could not be loaded"
        message="The audit log did not answer."
        onRetry={() => void query.refetch()}
      />
    )
  }

  const list = query.data.entries
  if (list.length === 0) {
    return <EmptyState icon="activity" title="No recorded activity" message="Nothing has touched this yet." />
  }

  return (
    <ul className="px-2 py-2">
      {list.map((item) => (
        <li key={item.id} className="flex items-start gap-3 rounded-xl px-2 py-2">
          <span
            className={clsx(
              'mt-0.5 flex h-7 w-7 shrink-0 items-center justify-center rounded-lg',
              item.ok ? 'bg-elevated text-muted' : 'bg-danger/12 text-danger',
            )}
          >
            <Icon name={item.ok ? 'check' : 'alert'} size={13} />
          </span>
          <div className="min-w-0 flex-1">
            <p className="truncate text-xs text-ink">
              <span className="font-medium">{item.username || 'System'}</span> {item.action}
            </p>
            <p className="truncate text-[11px] text-faint" title={dateTime(item.at)}>
              {smartDate(item.at)}
              {item.ip ? ` from ${item.ip}` : ''}
            </p>
            {item.detail && <p className="mt-0.5 break-words text-[11px] text-muted">{item.detail}</p>}
          </div>
        </li>
      ))}
    </ul>
  )
}

function SharesTab({
  entry,
  active,
  onCreate,
}: {
  entry: Entry
  active: boolean
  onCreate: () => void
}) {
  const query = useQuery({ queryKey: ['shares'], queryFn: () => api.shares(), enabled: active })

  if (query.isPending) return <LoadingRows rows={3} />
  if (query.isError) {
    return (
      <ErrorNote
        title="Links could not be loaded"
        message="The share list did not answer."
        onRetry={() => void query.refetch()}
      />
    )
  }

  const links = query.data.shares.filter((share) => share.path === entry.path)

  if (links.length === 0) {
    return (
      <EmptyState
        icon="link"
        title="No links yet"
        message="Create a link to let someone outside Storix reach this."
        action={
          <Button variant="primary" icon="share" onClick={onCreate}>
            Create link
          </Button>
        }
      />
    )
  }

  return (
    <div className="px-3 py-3">
      <ul className="space-y-2">
        {links.map((share) => (
          <li key={share.id} className="rounded-xl border border-line bg-elevated px-3 py-2.5">
            <div className="flex items-center gap-2">
              <Icon name={share.kind === 'upload' ? 'cloud-upload' : 'link'} size={14} className="shrink-0 text-primary" />
              <span className="min-w-0 flex-1 truncate text-xs text-ink" title={share.url || share.token}>
                {share.url || share.token}
              </span>
              {share.hasPassword && <Icon name="lock" size={13} className="shrink-0 text-warning" />}
            </div>
            <p className="mt-1.5 text-[11px] text-faint">
              {counted(share.downloads, 'download')}
              {share.maxDownloads > 0 ? ` of ${share.maxDownloads.toLocaleString()}` : ''}
              {share.expiresAt ? `, expires ${smartDate(share.expiresAt)}` : ', no expiry'}
            </p>
            {share.note && <p className="mt-1 break-words text-[11px] text-muted">{share.note}</p>}
          </li>
        ))}
      </ul>
      <Button className="mt-3" block icon="plus" onClick={onCreate}>
        Create another link
      </Button>
    </div>
  )
}

// ---- panel ------------------------------------------------------------------

export default function DetailsPanel({ entry, entries, onClose, onAction }: DetailsPanelProps) {
  const { isAdmin } = useSession()
  const storedWidth = useApp((state) => state.detailsWidth)
  const setDetailsWidth = useApp((state) => state.setDetailsWidth)

  const [dragWidth, setDragWidth] = useState<number | null>(null)
  const dragStart = useRef<{ x: number; width: number } | null>(null)
  const [tab, setTab] = useState<TabId>('details')
  const [menu, setMenu] = useState<{ x: number; y: number } | null>(null)

  const width = dragWidth ?? storedWidth
  const multiple = entries.length > 1

  const onPointerDown = useCallback(
    (event: ReactPointerEvent<HTMLDivElement>) => {
      event.preventDefault()
      dragStart.current = { x: event.clientX, width }
      event.currentTarget.setPointerCapture(event.pointerId)
      setDragWidth(width)
    },
    [width],
  )

  const onPointerMove = useCallback((event: ReactPointerEvent<HTMLDivElement>) => {
    const start = dragStart.current
    if (!start) return
    const next = Math.min(MAX_WIDTH, Math.max(MIN_WIDTH, start.width + (start.x - event.clientX)))
    setDragWidth(next)
  }, [])

  const endDrag = useCallback(
    (event: ReactPointerEvent<HTMLDivElement>) => {
      if (!dragStart.current) return
      dragStart.current = null
      if (event.currentTarget.hasPointerCapture(event.pointerId)) {
        event.currentTarget.releasePointerCapture(event.pointerId)
      }
      if (dragWidth !== null) setDetailsWidth(dragWidth)
      setDragWidth(null)
    },
    [dragWidth, setDetailsWidth],
  )

  const nudge = useCallback(
    (event: ReactKeyboardEvent<HTMLDivElement>) => {
      if (event.key === 'ArrowLeft') {
        event.preventDefault()
        setDetailsWidth(Math.min(MAX_WIDTH, width + 24))
      } else if (event.key === 'ArrowRight') {
        event.preventDefault()
        setDetailsWidth(Math.max(MIN_WIDTH, width - 24))
      }
    },
    [setDetailsWidth, width],
  )

  const tabs: Array<{ id: TabId; label: string }> = useMemo(() => {
    const list: Array<{ id: TabId; label: string }> = [
      { id: 'details', label: 'Details' },
      { id: 'preview', label: 'Preview' },
    ]
    if (isAdmin) list.push({ id: 'activity', label: 'Activity' })
    list.push({ id: 'shares', label: 'Shares' })
    return list
  }, [isAdmin])

  const menuItems: MenuItem[] = useMemo(() => {
    if (!entry) return []
    return [
      { id: 'rename', label: 'Rename', icon: 'edit', onSelect: () => onAction('rename', entry) },
      { id: 'copy', label: 'Copy', icon: 'copy', onSelect: () => onAction('copy', entry) },
      { id: 'move', label: 'Move', icon: 'move', onSelect: () => onAction('move', entry) },
      { id: 'compress', label: 'Compress', icon: 'archive', onSelect: () => onAction('compress', entry) },
      { id: 'divider', label: '', divider: true },
      { id: 'delete', label: 'Delete', icon: 'trash', danger: true, onSelect: () => onAction('delete', entry) },
    ]
  }, [entry, onAction])

  const activeTab: TabId = tab === 'activity' && !isAdmin ? 'details' : tab

  return (
    <aside
      aria-label="Details"
      className="relative flex h-full shrink-0 flex-col overflow-hidden border-l border-line bg-surface"
      style={{ width }}
    >
      <div
        role="separator"
        aria-label="Resize the details panel"
        aria-orientation="vertical"
        aria-valuenow={width}
        aria-valuemin={MIN_WIDTH}
        aria-valuemax={MAX_WIDTH}
        tabIndex={0}
        onPointerDown={onPointerDown}
        onPointerMove={onPointerMove}
        onPointerUp={endDrag}
        onPointerCancel={endDrag}
        onKeyDown={nudge}
        className="absolute inset-y-0 left-0 z-10 w-1.5 cursor-col-resize bg-transparent transition-colors hover:bg-primary/40"
      />

      {multiple ? (
        <>
          <header className="flex items-center gap-3 border-b border-line px-4 py-3">
            <div className="min-w-0 flex-1">
              <h2 className="truncate text-sm font-semibold text-ink">{counted(entries.length, 'item')}</h2>
              <p className="text-xs text-faint">Selected in this folder</p>
            </div>
            <IconButton icon="close" label="Close details" onClick={onClose} />
          </header>
          <SelectionSummary entries={entries} />
        </>
      ) : !entry ? (
        <>
          <header className="flex items-center gap-3 border-b border-line px-4 py-3">
            <h2 className="min-w-0 flex-1 truncate text-sm font-semibold text-ink">Details</h2>
            <IconButton icon="close" label="Close details" onClick={onClose} />
          </header>
          <div className="flex min-h-0 flex-1 items-center justify-center">
            <EmptyState
              icon="info"
              title="Select a file to see its details"
              message="Its preview, size and permissions appear here."
            />
          </div>
        </>
      ) : (
        <>
          <header className="flex items-start gap-3 border-b border-line px-4 py-3">
            <span
              className={clsx(
                'mt-0.5 flex h-9 w-9 shrink-0 items-center justify-center rounded-xl bg-elevated',
                colourForKind(entry.kind, entry.isDir),
              )}
            >
              <Icon name={iconForKind(entry.kind, entry.isDir)} size={18} />
            </span>
            <div className="min-w-0 flex-1">
              <h2 className="break-words text-sm font-semibold text-ink" title={entry.name}>
                {entry.name}
              </h2>
              <div className="mt-1.5 flex flex-wrap items-center gap-1.5">
                <span className="sx-chip">{kindLabel(entry)}</span>
                {entry.symlink && <span className="sx-chip">Shortcut</span>}
                {entry.readOnly && <span className="sx-chip">Read only</span>}
                {entry.hidden && <span className="sx-chip">Hidden</span>}
              </div>
            </div>
            <IconButton icon="close" label="Close details" onClick={onClose} />
          </header>

          <div className="shrink-0 border-b border-line p-3">
            <div className="h-40 overflow-hidden rounded-xl border border-line bg-elevated">
              <PreviewViewer key={entry.path} entry={entry} controls={false} />
            </div>

            <div className="mt-3 flex flex-wrap items-center gap-2">
              <Button
                variant="primary"
                icon={entry.isDir ? 'folder-open' : 'eye'}
                className="flex-1 basis-24"
                onClick={() => onAction('open', entry)}
              >
                Open
              </Button>
              <Button icon="download" className="flex-1 basis-24" onClick={() => onAction('download', entry)}>
                Download
              </Button>
              <Button icon="share" className="flex-1 basis-24" onClick={() => onAction('share', entry)}>
                Share
              </Button>
              <IconButton
                icon="more"
                label="More actions"
                className="shrink-0"
                onClick={(event) => {
                  const rect = event.currentTarget.getBoundingClientRect()
                  setMenu({ x: rect.right, y: rect.bottom + 6 })
                }}
              />
            </div>
          </div>

          <div role="tablist" aria-label="File details" className="flex shrink-0 gap-1 border-b border-line px-2">
            {tabs.map((item) => (
              <button
                key={item.id}
                type="button"
                role="tab"
                id={`sx-details-tab-${item.id}`}
                aria-selected={activeTab === item.id}
                aria-controls={`sx-details-panel-${item.id}`}
                onClick={() => setTab(item.id)}
                className={clsx(
                  'relative h-9 rounded-lg px-2.5 text-xs transition-colors',
                  activeTab === item.id ? 'text-ink' : 'text-muted hover:text-ink',
                )}
              >
                {item.label}
                {activeTab === item.id && (
                  <span className="absolute inset-x-2 -bottom-px h-0.5 rounded-full bg-primary" />
                )}
              </button>
            ))}
          </div>

          <div
            role="tabpanel"
            id={`sx-details-panel-${activeTab}`}
            aria-labelledby={`sx-details-tab-${activeTab}`}
            className={clsx('min-h-0 flex-1', activeTab === 'preview' ? 'flex flex-col' : 'sx-scroll')}
          >
            {activeTab === 'details' && <DetailsTab key={entry.path} entry={entry} active />}
            {activeTab === 'preview' && <PreviewViewer key={`full-${entry.path}`} entry={entry} />}
            {activeTab === 'activity' && isAdmin && <ActivityTab key={entry.path} entry={entry} active />}
            {activeTab === 'shares' && (
              <SharesTab key={entry.path} entry={entry} active onCreate={() => onAction('share', entry)} />
            )}
          </div>
        </>
      )}

      {menu && entry && (
        <Menu items={menuItems} x={menu.x} y={menu.y} anchorRight onClose={() => setMenu(null)} />
      )}
    </aside>
  )
}
