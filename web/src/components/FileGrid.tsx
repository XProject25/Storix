// The card views of the file browser. FileGrid is the compact one, FileGallery
// puts media first with large tiles.
// Developed by X Project.

import {
  useEffect,
  useRef,
  useState,
  type DragEvent,
  type MouseEvent as ReactMouseEvent,
  type RefObject,
} from 'react'
import clsx from 'clsx'
import { bytes } from '../lib/format'
import type { Entry } from '../lib/types'
import { Icon, colourForKind, iconForKind } from './Icon'
import { Checkbox, Skeleton } from './ui'
import { RenameInput, Thumb, carriesPaths, type FileViewProps } from './FileList'

const PAGE = 240

/** useIncremental keeps very large folders responsive by growing as you scroll. */
function useIncremental(total: number, scrollRef: RefObject<HTMLDivElement>) {
  const [count, setCount] = useState(PAGE)

  useEffect(() => setCount(PAGE), [total])

  useEffect(() => {
    const node = scrollRef.current
    if (!node) return
    const onScroll = () => {
      if (node.scrollTop + node.clientHeight > node.scrollHeight - 600) {
        setCount((value) => (value >= total ? value : value + PAGE))
      }
    }
    node.addEventListener('scroll', onScroll)
    return () => node.removeEventListener('scroll', onScroll)
  }, [scrollRef, total])

  return Math.min(count, total)
}

interface TileProps {
  entry: Entry
  selected: boolean
  focused: boolean
  renaming: boolean
  large: boolean
  onSelect: FileViewProps['onSelect']
  onOpen: FileViewProps['onOpen']
  onContextMenu: FileViewProps['onContextMenu']
  onDragStartRow: FileViewProps['onDragStartRow']
  onDropOnFolder: FileViewProps['onDropOnFolder']
  onRenameCommit: FileViewProps['onRenameCommit']
  onRenameCancel: FileViewProps['onRenameCancel']
}

function Tile({
  entry,
  selected,
  focused,
  renaming,
  large,
  onSelect,
  onOpen,
  onContextMenu,
  onDragStartRow,
  onDropOnFolder,
  onRenameCommit,
  onRenameCancel,
}: TileProps) {
  const [over, setOver] = useState(false)
  const droppable = entry.isDir
  const media = entry.thumbnail || entry.kind === 'image' || entry.kind === 'video'

  return (
    <div
      role="option"
      aria-selected={selected}
      data-row={entry.path}
      draggable={!renaming}
      onClick={(event: ReactMouseEvent) =>
        onSelect(entry, { ctrlKey: event.ctrlKey, metaKey: event.metaKey, shiftKey: event.shiftKey })
      }
      onDoubleClick={() => onOpen(entry)}
      onContextMenu={(event) => onContextMenu(entry, event)}
      onDragStart={(event) => onDragStartRow(entry, event)}
      onDragOver={(event: DragEvent) => {
        if (!droppable || !carriesPaths(event)) return
        event.preventDefault()
        event.stopPropagation()
        event.dataTransfer.dropEffect = event.ctrlKey || event.metaKey ? 'copy' : 'move'
        setOver(true)
      }}
      onDragLeave={() => setOver(false)}
      onDrop={(event: DragEvent) => {
        if (!droppable || !carriesPaths(event)) return
        event.preventDefault()
        event.stopPropagation()
        setOver(false)
        onDropOnFolder(entry, event)
      }}
      className={clsx(
        'group relative flex cursor-default select-none flex-col rounded-2xl border p-2 transition-colors',
        selected ? 'border-primary/50 bg-primary/12' : 'border-line/70 bg-surface hover:bg-elevated',
        over && 'ring-2 ring-primary',
        focused && !selected && 'ring-1 ring-primary/45',
        entry.hidden && 'opacity-70',
      )}
    >
      <span
        className={clsx(
          'absolute left-3 top-3 z-10 transition-opacity',
          selected ? 'opacity-100' : 'opacity-0 group-hover:opacity-100',
        )}
        onClick={(event) => event.stopPropagation()}
      >
        <Checkbox
          checked={selected}
          onChange={() => onSelect(entry, { ctrlKey: true, metaKey: false, shiftKey: false })}
        />
      </span>

      <div
        className={clsx(
          'relative flex items-center justify-center overflow-hidden rounded-xl bg-elevated',
          large ? 'aspect-[4/3]' : 'aspect-square',
        )}
      >
        {media ? (
          <Thumb entry={entry} size={large ? 320 : 200} iconSize={large ? 46 : 38} />
        ) : (
          <span className={colourForKind(entry.kind, entry.isDir)}>
            <Icon name={iconForKind(entry.kind, entry.isDir)} size={large ? 46 : 38} strokeWidth={1.4} />
          </span>
        )}
        {entry.kind === 'video' && (
          <span className="absolute bottom-2 right-2 flex h-7 w-7 items-center justify-center rounded-full bg-black/55 text-white">
            <Icon name="play" size={13} />
          </span>
        )}
      </div>

      <div className="mt-2 min-w-0 px-1 pb-0.5">
        {renaming ? (
          <RenameInput entry={entry} onCommit={onRenameCommit} onCancel={onRenameCancel} />
        ) : (
          <>
            <div className="flex min-w-0 items-start gap-1">
              <span className="line-clamp-2 break-all text-[13px] leading-snug text-ink">{entry.name}</span>
              {entry.symlink && <Icon name="link" size={11} className="mt-0.5 shrink-0 text-faint" />}
            </div>
            <div className="mt-0.5 text-[11px] text-faint">{entry.isDir ? 'Folder' : bytes(entry.size)}</div>
          </>
        )}
      </div>
    </div>
  )
}

function CardView({ large, props }: { large: boolean; props: FileViewProps }) {
  const {
    entries,
    selected,
    focused,
    onSelect,
    onOpen,
    onContextMenu,
    onDragStartRow,
    onDropOnFolder,
    renamingPath,
    onRenameCommit,
    onRenameCancel,
    loading,
    onMarqueeSelect,
  } = props
  const scrollRef = useRef<HTMLDivElement>(null)
  const visible = useIncremental(entries.length, scrollRef)

  // Bring the focused tile into view when the keyboard moves it.
  useEffect(() => {
    if (!focused) return
    const node = scrollRef.current?.querySelector(`[data-row="${CSS.escape(focused)}"]`)
    node?.scrollIntoView({ block: 'nearest' })
  }, [focused])

  return (
    <div
      ref={scrollRef}
      role="listbox"
      aria-multiselectable
      aria-label="Files"
      className="sx-scroll min-h-0 flex-1 p-3"
      onClick={(event) => {
        if ((event.target as HTMLElement).closest('[data-row]')) return
        if (event.ctrlKey || event.metaKey || event.shiftKey) return
        onMarqueeSelect?.([], false)
      }}
      onContextMenu={(event) => {
        if ((event.target as HTMLElement).closest('[data-row]')) return
        onContextMenu(null, event)
      }}
    >
      {loading && entries.length === 0 ? (
        <div
          className={clsx(
            'grid gap-3',
            large
              ? 'grid-cols-[repeat(auto-fill,minmax(220px,1fr))]'
              : 'grid-cols-[repeat(auto-fill,minmax(150px,1fr))]',
          )}
        >
          {Array.from({ length: 12 }).map((_, index) => (
            <div key={index} className="rounded-2xl border border-line/70 p-2">
              <Skeleton className={clsx('w-full rounded-xl', large ? 'aspect-[4/3]' : 'aspect-square')} />
              <Skeleton className="mt-2 h-3 w-3/4" />
            </div>
          ))}
        </div>
      ) : (
        <div
          className={clsx(
            'grid gap-3',
            large
              ? 'grid-cols-[repeat(auto-fill,minmax(220px,1fr))]'
              : 'grid-cols-[repeat(auto-fill,minmax(150px,1fr))]',
          )}
        >
          {entries.slice(0, visible).map((entry) => (
            <Tile
              key={entry.path}
              entry={entry}
              large={large}
              selected={selected.has(entry.path)}
              focused={focused === entry.path}
              renaming={renamingPath === entry.path}
              onSelect={onSelect}
              onOpen={onOpen}
              onContextMenu={onContextMenu}
              onDragStartRow={onDragStartRow}
              onDropOnFolder={onDropOnFolder}
              onRenameCommit={onRenameCommit}
              onRenameCancel={onRenameCancel}
            />
          ))}
        </div>
      )}
      {visible < entries.length && (
        <p className="py-4 text-center text-xs text-faint">
          Showing {visible.toLocaleString()} of {entries.length.toLocaleString()}, scroll for more
        </p>
      )}
    </div>
  )
}

/** FileGrid is the compact card view. */
export function FileGrid(props: FileViewProps) {
  return <CardView large={false} props={props} />
}

/** FileGallery is the large media first view. */
export function FileGallery(props: FileViewProps) {
  return <CardView large props={props} />
}
