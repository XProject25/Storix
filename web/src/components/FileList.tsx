// The list view of the file browser. Sortable headers, inline rename, drag and
// drop, rubber band selection and windowing so a folder with fifty thousand
// files still scrolls smoothly.
// Developed by X Project.

import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type CSSProperties,
  type DragEvent,
  type MouseEvent as ReactMouseEvent,
  type ReactNode,
} from 'react'
import clsx from 'clsx'
import { api } from '../lib/api'
import { ago, bytes, parentPath, smartDate } from '../lib/format'
import type { Entry, SortField, SortOrder } from '../lib/types'
import { Icon, colourForKind, iconForKind } from './Icon'
import { Checkbox, Skeleton } from './ui'

/** The drag payload every internal move and copy travels in. */
export const STORIX_DRAG_TYPE = 'application/x-storix-paths'

export const ROW_HEIGHT = 44
/** Under 768px a row carries a second line of text and has to fit a finger. */
const ROW_HEIGHT_NARROW = 52
const NARROW_QUERY = '(max-width: 767px)'
const LIST_PADDING = 4
const VIRTUAL_THRESHOLD = 300
const OVERSCAN = 10

/**
 * useListRowHeight follows the row height the stylesheet is using, so the
 * windowed list puts rows where the browser actually draws them.
 */
function useListRowHeight(): number {
  const [height, setHeight] = useState(() =>
    window.matchMedia(NARROW_QUERY).matches ? ROW_HEIGHT_NARROW : ROW_HEIGHT,
  )
  useEffect(() => {
    const media = window.matchMedia(NARROW_QUERY)
    const update = () => setHeight(media.matches ? ROW_HEIGHT_NARROW : ROW_HEIGHT)
    update()
    media.addEventListener('change', update)
    return () => media.removeEventListener('change', update)
  }, [])
  return height
}

/** SelectModifiers tells the page which selection gesture was used. */
export interface SelectModifiers {
  ctrlKey: boolean
  metaKey: boolean
  shiftKey: boolean
}

/** FileViewProps is shared by the list, the grid and the gallery. */
export interface FileViewProps {
  entries: Entry[]
  selected: Set<string>
  focused: string | null
  onSelect: (entry: Entry, modifiers: SelectModifiers) => void
  onOpen: (entry: Entry) => void
  onContextMenu: (entry: Entry | null, event: ReactMouseEvent) => void
  onDragStartRow: (entry: Entry, event: DragEvent) => void
  onDropOnFolder: (entry: Entry, event: DragEvent) => void
  sort: SortField
  order: SortOrder
  onSort: (field: SortField) => void
  renamingPath: string | null
  onRenameCommit: (entry: Entry, name: string) => void
  onRenameCancel: () => void
  loading?: boolean
  /** Search results show the containing folder under each name. */
  showPath?: boolean
  /** Rubber band selection reports the paths it swept over. */
  onMarqueeSelect?: (paths: string[], additive: boolean) => void
}

export function carriesPaths(event: DragEvent): boolean {
  return Array.from(event.dataTransfer.types).includes(STORIX_DRAG_TYPE)
}

/** kindLabel is the plain word for the Type column. */
export function kindLabel(entry: Entry): string {
  if (entry.isDir) return 'Folder'
  if (entry.ext) return entry.ext.replace(/^\./, '').toUpperCase()
  switch (entry.kind) {
    case 'image':
      return 'Image'
    case 'video':
      return 'Video'
    case 'audio':
      return 'Audio'
    case 'archive':
      return 'Archive'
    case 'code':
      return 'Code'
    case 'text':
      return 'Text'
    case 'document':
      return 'Document'
    case 'disk':
      return 'Disk image'
    case 'pdf':
      return 'PDF'
    default:
      return 'File'
  }
}

/** Thumb shows a server rendered preview, or the icon for the family. */
export function Thumb({
  entry,
  size,
  className,
  iconSize,
}: {
  entry: Entry
  size: number
  className?: string
  iconSize?: number
}) {
  const [failed, setFailed] = useState(false)
  useEffect(() => setFailed(false), [entry.path])

  if (entry.thumbnail && !failed) {
    return (
      <img
        src={api.thumbURL(entry.path, size <= 64 ? 128 : 512)}
        alt=""
        loading="lazy"
        draggable={false}
        onError={() => setFailed(true)}
        className={clsx('drag-none h-full w-full object-cover', className)}
      />
    )
  }
  return (
    <span className={clsx('flex h-full w-full items-center justify-center', colourForKind(entry.kind, entry.isDir))}>
      <Icon name={iconForKind(entry.kind, entry.isDir)} size={iconSize ?? Math.round(size * 0.62)} />
    </span>
  )
}

/** RenameInput is the field that replaces a name while it is being renamed. */
export function RenameInput({
  entry,
  onCommit,
  onCancel,
  className,
}: {
  entry: Entry
  onCommit: (entry: Entry, name: string) => void
  onCancel: () => void
  className?: string
}) {
  const [value, setValue] = useState(entry.name)
  const settled = useRef(false)
  const ref = useRef<HTMLInputElement>(null)

  useEffect(() => {
    const input = ref.current
    if (!input) return
    input.focus()
    const dot = entry.name.lastIndexOf('.')
    if (!entry.isDir && dot > 0) input.setSelectionRange(0, dot)
    else input.select()
  }, [entry])

  const commit = () => {
    if (settled.current) return
    settled.current = true
    const next = value.trim()
    if (!next || next === entry.name) onCancel()
    else onCommit(entry, next)
  }

  const cancel = () => {
    if (settled.current) return
    settled.current = true
    onCancel()
  }

  return (
    <input
      ref={ref}
      value={value}
      aria-label="New name"
      spellCheck={false}
      onChange={(event) => setValue(event.target.value)}
      onClick={(event) => event.stopPropagation()}
      onDoubleClick={(event) => event.stopPropagation()}
      onBlur={commit}
      onKeyDown={(event) => {
        event.stopPropagation()
        if (event.key === 'Enter') {
          event.preventDefault()
          commit()
        }
        if (event.key === 'Escape') {
          event.preventDefault()
          cancel()
        }
      }}
      className={clsx('sx-input h-7 px-2 text-sm', className)}
    />
  )
}

// ---- header ------------------------------------------------------------------

function SortHeader({
  field,
  label,
  sort,
  order,
  onSort,
  className,
}: {
  field: SortField
  label: string
  sort: SortField
  order: SortOrder
  onSort: (field: SortField) => void
  className?: string
}) {
  const active = sort === field
  return (
    <div className={className} aria-sort={active ? (order === 'asc' ? 'ascending' : 'descending') : 'none'}>
      <button
        type="button"
        onClick={() => onSort(field)}
        className={clsx(
          'inline-flex items-center gap-1 rounded-md px-1 py-0.5 transition-colors hover:text-ink',
          active && 'text-ink',
        )}
      >
        <span className="truncate">{label}</span>
        {active && <Icon name={order === 'asc' ? 'chevron-up' : 'chevron-down'} size={13} />}
      </button>
    </div>
  )
}

// ---- row ---------------------------------------------------------------------

// Under 768px every column but the name gives way to the second line of the
// row, which says the same thing in fewer words.
const CELL_SIZE = 'hidden w-24 shrink-0 text-right tabular-nums md:block'
const CELL_SIZE_HEAD = 'hidden w-24 shrink-0 justify-end md:flex'
const CELL_TYPE = 'hidden w-28 shrink-0 lg:block'
const CELL_MODIFIED = 'hidden w-40 shrink-0 md:block'
const CELL_MODE = 'hidden w-28 shrink-0 xl:block'

/** rowSummary is the second line: what the hidden columns would have said. */
function rowSummary(entry: Entry): string {
  const when = ago(entry.modified)
  if (entry.isDir) return when
  const size = bytes(entry.size)
  return when ? `${size}, ${when}` : size
}

interface RowProps {
  entry: Entry
  index: number
  selected: boolean
  focused: boolean
  renaming: boolean
  showPath?: boolean
  style?: CSSProperties
  onSelect: FileViewProps['onSelect']
  onOpen: FileViewProps['onOpen']
  onContextMenu: FileViewProps['onContextMenu']
  onDragStartRow: FileViewProps['onDragStartRow']
  onDropOnFolder: FileViewProps['onDropOnFolder']
  onRenameCommit: FileViewProps['onRenameCommit']
  onRenameCancel: FileViewProps['onRenameCancel']
}

function FileRow({
  entry,
  index,
  selected,
  focused,
  renaming,
  showPath,
  style,
  onSelect,
  onOpen,
  onContextMenu,
  onDragStartRow,
  onDropOnFolder,
  onRenameCommit,
  onRenameCancel,
}: RowProps) {
  const [over, setOver] = useState(false)
  const droppable = entry.isDir

  return (
    <div
      role="row"
      aria-rowindex={index + 1}
      aria-selected={selected}
      data-row={entry.path}
      draggable={!renaming}
      style={style}
      onClick={(event) =>
        onSelect(entry, { ctrlKey: event.ctrlKey, metaKey: event.metaKey, shiftKey: event.shiftKey })
      }
      onDoubleClick={() => onOpen(entry)}
      onContextMenu={(event) => onContextMenu(entry, event)}
      onDragStart={(event) => onDragStartRow(entry, event)}
      onDragOver={(event) => {
        if (!droppable || !carriesPaths(event)) return
        event.preventDefault()
        event.stopPropagation()
        event.dataTransfer.dropEffect = event.ctrlKey || event.metaKey ? 'copy' : 'move'
        setOver(true)
      }}
      onDragLeave={() => setOver(false)}
      onDrop={(event) => {
        if (!droppable || !carriesPaths(event)) return
        event.preventDefault()
        event.stopPropagation()
        setOver(false)
        onDropOnFolder(entry, event)
      }}
      className={clsx(
        'sx-row sx-row-file group text-sm',
        over && 'ring-2 ring-primary bg-primary/12',
        focused && !selected && 'ring-1 ring-primary/45',
        entry.hidden && 'opacity-65',
      )}
      data-selected={selected ? 'true' : undefined}
    >
      {/* The box itself stays small. The area around it is what a finger hits. */}
      <span
        className="sx-touch flex shrink-0 items-center justify-center"
        onClick={(event) => {
          event.stopPropagation()
          onSelect(entry, { ctrlKey: true, metaKey: false, shiftKey: false })
        }}
      >
        <Checkbox
          checked={selected}
          onChange={() => onSelect(entry, { ctrlKey: true, metaKey: false, shiftKey: false })}
        />
      </span>

      <span
        className={clsx(
          'flex h-7 w-7 shrink-0 items-center justify-center overflow-hidden rounded-lg',
          entry.thumbnail ? 'bg-elevated' : '',
        )}
      >
        <Thumb entry={entry} size={28} iconSize={18} />
      </span>

      <div className="min-w-0 flex-1">
        {renaming ? (
          <RenameInput entry={entry} onCommit={onRenameCommit} onCancel={onRenameCancel} />
        ) : (
          <>
            <div className="flex min-w-0 items-center gap-1.5">
              <span className="truncate text-ink">{entry.name}</span>
              {entry.symlink && (
                <Icon name="link" size={12} className={clsx('shrink-0', entry.broken ? 'text-danger' : 'text-faint')} />
              )}
              {entry.readOnly && <Icon name="lock" size={12} className="shrink-0 text-faint" />}
            </div>
            {showPath ? (
              <div className="truncate text-xs text-faint">{parentPath(entry.path) || '/'}</div>
            ) : (
              <div className="truncate text-xs text-muted md:hidden">{rowSummary(entry)}</div>
            )}
          </>
        )}
      </div>

      <span className={clsx(CELL_SIZE, 'text-muted')}>{entry.isDir ? '' : bytes(entry.size)}</span>
      <span className={clsx(CELL_TYPE, 'truncate text-muted')}>{kindLabel(entry)}</span>
      <span className={clsx(CELL_MODIFIED, 'truncate text-muted')}>{smartDate(entry.modified)}</span>
      <span className={clsx(CELL_MODE, 'truncate font-mono text-xs text-faint')}>{entry.mode || entry.modeOctal}</span>
    </div>
  )
}

// ---- list --------------------------------------------------------------------

/** FileList renders the detailed view of a folder. */
export function FileList({
  entries,
  selected,
  focused,
  onSelect,
  onOpen,
  onContextMenu,
  onDragStartRow,
  onDropOnFolder,
  sort,
  order,
  onSort,
  renamingPath,
  onRenameCommit,
  onRenameCancel,
  loading,
  showPath,
  onMarqueeSelect,
}: FileViewProps) {
  const scrollRef = useRef<HTMLDivElement>(null)
  const contentRef = useRef<HTMLDivElement>(null)
  const [scrollTop, setScrollTop] = useState(0)
  const [viewport, setViewport] = useState(600)
  const [band, setBand] = useState<{ top: number; bottom: number } | null>(null)
  const rowHeight = useListRowHeight()

  const virtual = entries.length > VIRTUAL_THRESHOLD

  useEffect(() => {
    const node = scrollRef.current
    if (!node) return
    setViewport(node.clientHeight)
    if (typeof ResizeObserver === 'undefined') return
    const observer = new ResizeObserver(() => setViewport(node.clientHeight))
    observer.observe(node)
    return () => observer.disconnect()
  }, [])

  // Keep the focused row inside the viewport, including when it is not mounted.
  useEffect(() => {
    if (!focused) return
    const node = scrollRef.current
    if (!node) return
    const index = entries.findIndex((entry) => entry.path === focused)
    if (index < 0) return
    const top = index * rowHeight
    const bottom = top + rowHeight
    if (top < node.scrollTop) node.scrollTop = Math.max(0, top - LIST_PADDING)
    else if (bottom > node.scrollTop + node.clientHeight) node.scrollTop = bottom - node.clientHeight + LIST_PADDING
  }, [focused, entries, rowHeight])

  const range = useMemo(() => {
    if (!virtual) return { start: 0, end: entries.length }
    const start = Math.max(0, Math.floor(scrollTop / rowHeight) - OVERSCAN)
    const end = Math.min(entries.length, Math.ceil((scrollTop + viewport) / rowHeight) + OVERSCAN)
    return { start, end }
  }, [virtual, scrollTop, viewport, entries.length, rowHeight])

  const indexAt = useCallback(
    (clientY: number): number => {
      const node = contentRef.current
      if (!node) return -1
      const rect = node.getBoundingClientRect()
      return Math.floor((clientY - rect.top - LIST_PADDING) / rowHeight)
    },
    [rowHeight],
  )

  const startMarquee = useCallback(
    (event: ReactMouseEvent<HTMLDivElement>) => {
      if (!onMarqueeSelect || event.button !== 0) return
      const target = event.target as HTMLElement
      if (target.closest('[data-row]')) return
      const additive = event.ctrlKey || event.metaKey || event.shiftKey
      const originY = event.clientY
      const originIndex = indexAt(originY)
      let moved = false

      const onMove = (move: MouseEvent) => {
        if (!moved && Math.abs(move.clientY - originY) < 4) return
        moved = true
        const node = contentRef.current
        if (!node) return
        const rect = node.getBoundingClientRect()
        const a = originY - rect.top
        const b = move.clientY - rect.top
        setBand({ top: Math.min(a, b), bottom: Math.max(a, b) })
        const from = Math.max(0, Math.min(originIndex, indexAt(move.clientY)))
        const to = Math.min(entries.length - 1, Math.max(originIndex, indexAt(move.clientY)))
        if (to < 0 || from > to) {
          onMarqueeSelect([], additive)
          return
        }
        onMarqueeSelect(
          entries.slice(from, to + 1).map((entry) => entry.path),
          additive,
        )
      }

      const onUp = () => {
        window.removeEventListener('mousemove', onMove)
        window.removeEventListener('mouseup', onUp)
        setBand(null)
        // A plain click on empty space clears the selection, as a desktop does.
        if (!moved && !additive) onMarqueeSelect([], false)
      }

      window.addEventListener('mousemove', onMove)
      window.addEventListener('mouseup', onUp)
    },
    [entries, indexAt, onMarqueeSelect],
  )

  const rows: ReactNode[] = []
  for (let index = range.start; index < range.end; index++) {
    const entry = entries[index]
    if (!entry) continue
    rows.push(
      <FileRow
        key={entry.path}
        entry={entry}
        index={index}
        selected={selected.has(entry.path)}
        focused={focused === entry.path}
        renaming={renamingPath === entry.path}
        showPath={showPath}
        onSelect={onSelect}
        onOpen={onOpen}
        onContextMenu={onContextMenu}
        onDragStartRow={onDragStartRow}
        onDropOnFolder={onDropOnFolder}
        onRenameCommit={onRenameCommit}
        onRenameCancel={onRenameCancel}
      />,
    )
  }

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="flex h-9 shrink-0 items-center gap-3 border-b border-line px-4 text-[11px] font-semibold uppercase tracking-[0.12em] text-faint">
        <span className="w-10 shrink-0 lg:w-[18px]" />
        <span className="w-7 shrink-0" />
        <SortHeader field="name" label="Name" sort={sort} order={order} onSort={onSort} className="min-w-0 flex-1" />
        <SortHeader field="size" label="Size" sort={sort} order={order} onSort={onSort} className={CELL_SIZE_HEAD} />
        <SortHeader field="kind" label="Type" sort={sort} order={order} onSort={onSort} className={CELL_TYPE} />
        <SortHeader
          field="modified"
          label="Modified"
          sort={sort}
          order={order}
          onSort={onSort}
          className={CELL_MODIFIED}
        />
        <span className={CELL_MODE}>Permissions</span>
      </div>

      <div
        ref={scrollRef}
        role="grid"
        aria-label="Files"
        aria-rowcount={entries.length}
        className="sx-scroll relative min-h-0 flex-1"
        onScroll={(event) => {
          if (virtual) setScrollTop(event.currentTarget.scrollTop)
        }}
        onMouseDown={startMarquee}
        onContextMenu={(event) => {
          if ((event.target as HTMLElement).closest('[data-row]')) return
          onContextMenu(null, event)
        }}
      >
        <div
          ref={contentRef}
          className="relative px-1 py-1"
          style={virtual ? { height: entries.length * rowHeight } : undefined}
        >
          {loading && entries.length === 0 ? (
            <div className="space-y-1.5 px-2 py-1.5">
              {Array.from({ length: 10 }).map((_, index) => (
                <div key={index} className="flex items-center gap-3">
                  <Skeleton className="h-7 w-7 rounded-lg" />
                  <Skeleton className="h-3.5 flex-1" />
                  <Skeleton className="hidden h-3.5 w-20 md:block" />
                  <Skeleton className="hidden h-3.5 w-28 lg:block" />
                </div>
              ))}
            </div>
          ) : virtual ? (
            <div style={{ transform: `translateY(${range.start * rowHeight}px)` }}>{rows}</div>
          ) : (
            rows
          )}

          {band && (
            <div
              aria-hidden="true"
              className="pointer-events-none absolute left-1 right-1 rounded-lg border border-primary/60 bg-primary/10"
              style={{ top: band.top, height: Math.max(1, band.bottom - band.top) }}
            />
          )}
        </div>
      </div>
    </div>
  )
}
