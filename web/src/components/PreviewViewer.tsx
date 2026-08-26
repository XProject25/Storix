// Inline previews. Whatever the file is, Storix tries to show it here rather
// than sending someone away to download it first.
// Developed by X Project.

import clsx from 'clsx'
import { Suspense, lazy, useMemo, useState, type CSSProperties, type ReactNode } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ApiError, api } from '../lib/api'
import { bytes, counted, parentPath, smartDate } from '../lib/format'
import type { Entry, Kind } from '../lib/types'
import { Icon, colourForKind, iconForKind, type IconName } from './Icon'
import { Button, Skeleton, Spinner, useToast } from './ui'

const CodeEditor = lazy(() => import('./CodeEditor'))

export interface PreviewViewerProps {
  entry: Entry
  className?: string
  controls?: boolean
}

/** KIND_LABELS keeps the plain word for each family in one place. */
export const KIND_LABELS: Record<Kind, string> = {
  folder: 'Folder',
  image: 'Image',
  video: 'Video',
  audio: 'Audio',
  pdf: 'PDF document',
  archive: 'Archive',
  code: 'Code',
  text: 'Text',
  document: 'Document',
  disk: 'Disk image',
  font: 'Font',
  binary: 'Program',
  other: 'File',
}

/** kindLabel names a file family in words anyone can read. */
export function kindLabel(entry: Entry): string {
  if (entry.isDir) return 'Folder'
  return KIND_LABELS[entry.kind] ?? 'File'
}

// A soft checkerboard so transparent images read correctly in both themes.
const CHECKER: CSSProperties = {
  backgroundImage:
    'linear-gradient(45deg, rgb(var(--sx-line) / 0.5) 25%, transparent 25%),' +
    'linear-gradient(-45deg, rgb(var(--sx-line) / 0.5) 25%, transparent 25%),' +
    'linear-gradient(45deg, transparent 75%, rgb(var(--sx-line) / 0.5) 75%),' +
    'linear-gradient(-45deg, transparent 75%, rgb(var(--sx-line) / 0.5) 75%)',
  backgroundSize: '16px 16px',
  backgroundPosition: '0 0, 0 8px, 8px -8px, -8px 0',
}

const MAX_PREVIEW_LINES = 4000

function messageOf(error: unknown): string {
  if (error instanceof ApiError) return error.detail || error.message
  if (error instanceof Error) return error.message
  return 'Something went wrong.'
}

// ---- shared shells ----------------------------------------------------------

function Notice({
  icon = 'info',
  tone = 'muted',
  title,
  message,
  action,
}: {
  icon?: IconName
  tone?: 'muted' | 'danger'
  title: string
  message?: string
  action?: ReactNode
}) {
  return (
    <div className="flex h-full min-h-0 w-full flex-col items-center justify-center gap-2.5 p-5 text-center">
      <span
        className={clsx(
          'flex h-11 w-11 items-center justify-center rounded-2xl',
          tone === 'danger' ? 'bg-danger/12 text-danger' : 'bg-elevated text-faint',
        )}
      >
        <Icon name={icon} size={21} />
      </span>
      <div className="min-w-0">
        <h3 className="truncate text-sm font-medium text-ink">{title}</h3>
        {message && <p className="mt-1 text-xs text-muted">{message}</p>}
      </div>
      {action}
    </div>
  )
}

function LoadingLines({ rows = 8 }: { rows?: number }) {
  const widths = ['w-3/4', 'w-1/2', 'w-5/6', 'w-2/3', 'w-1/3', 'w-4/5', 'w-1/2', 'w-3/5', 'w-2/3', 'w-1/2']
  return (
    <div className="flex h-full w-full flex-col gap-2 p-4">
      {Array.from({ length: rows }, (_, index) => (
        <Skeleton key={index} className={clsx('h-3.5', widths[index % widths.length])} />
      ))}
    </div>
  )
}

function DownloadLink({ entry, label = 'Download' }: { entry: Entry; label?: string }) {
  return (
    <a className="sx-btn-secondary" href={api.downloadURL(entry.path)}>
      <Icon name="download" size={16} />
      {label}
    </a>
  )
}

/** Banner is the calm one line note above a preview. */
function Banner({ children }: { children: ReactNode }) {
  return (
    <div className="flex shrink-0 items-center gap-2 border-b border-line bg-elevated px-3 py-1.5 text-[11px] text-muted">
      <Icon name="info" size={13} className="shrink-0 text-primary" />
      <span className="min-w-0 flex-1 truncate">{children}</span>
    </div>
  )
}

// ---- families ---------------------------------------------------------------

function GenericPreview({ entry, compact }: { entry: Entry; compact: boolean }) {
  const icon = iconForKind(entry.kind, entry.isDir)
  const colour = colourForKind(entry.kind, entry.isDir)

  if (compact) {
    return (
      <div className="flex h-full w-full flex-col items-center justify-center gap-2">
        <span className={clsx('flex h-12 w-12 items-center justify-center rounded-2xl bg-elevated', colour)}>
          <Icon name={icon} size={24} />
        </span>
        <span className="text-[11px] text-faint">
          {kindLabel(entry)}
          {entry.isDir ? '' : `, ${bytes(entry.size)}`}
        </span>
      </div>
    )
  }

  return (
    <div className="flex h-full min-h-0 w-full flex-col items-center justify-center gap-3 p-6 text-center">
      <span className={clsx('flex h-16 w-16 items-center justify-center rounded-3xl bg-elevated', colour)}>
        <Icon name={icon} size={30} />
      </span>
      <div className="min-w-0 max-w-full">
        <p className="truncate text-sm font-medium text-ink" title={entry.name}>
          {entry.name}
        </p>
        <p className="mt-1 text-xs text-muted">
          {kindLabel(entry)}
          {entry.isDir ? '' : `, ${bytes(entry.size)}`}
        </p>
      </div>
      {!entry.isDir && <DownloadLink entry={entry} />}
    </div>
  )
}

function ImagePreview({ entry, compact }: { entry: Entry; compact: boolean }) {
  const [state, setState] = useState<'loading' | 'ready' | 'error'>('loading')
  const [actualSize, setActualSize] = useState(false)

  if (state === 'error') {
    return (
      <Notice
        icon="alert"
        tone="danger"
        title="This image could not be shown"
        message="The file may be damaged or in a format the browser does not read."
        action={compact ? undefined : <DownloadLink entry={entry} />}
      />
    )
  }

  return (
    <div className="relative h-full min-h-0 w-full overflow-auto sx-scroll" style={CHECKER}>
      {state === 'loading' && <Skeleton className="absolute inset-0 rounded-none" />}
      <button
        type="button"
        aria-label={actualSize ? 'Zoom to fit' : 'View at full size'}
        onClick={() => setActualSize((value) => !value)}
        className={clsx(
          'flex items-center justify-center p-2',
          actualSize ? 'min-h-full min-w-full' : 'h-full w-full',
        )}
      >
        <img
          src={api.rawURL(entry.path)}
          alt={entry.name}
          draggable={false}
          onLoad={() => setState('ready')}
          onError={() => setState('error')}
          className={clsx(
            'drag-none rounded-lg transition-opacity',
            actualSize ? 'max-w-none' : 'max-h-full max-w-full object-contain',
            state === 'loading' ? 'opacity-0' : 'opacity-100',
          )}
        />
      </button>
      {!compact && state === 'ready' && (
        <span className="pointer-events-none absolute bottom-2 right-2 sx-chip">
          {actualSize ? 'Full size' : 'Fit to view'}
        </span>
      )}
    </div>
  )
}

function VideoPreview({ entry, compact }: { entry: Entry; compact: boolean }) {
  const [failed, setFailed] = useState(false)

  if (failed) {
    return (
      <Notice
        icon="alert"
        tone="danger"
        title="This video cannot be played here"
        message="The browser does not support this format. Download it to play it locally."
        action={compact ? undefined : <DownloadLink entry={entry} />}
      />
    )
  }

  return (
    <div className="flex h-full min-h-0 w-full items-center justify-center bg-bg">
      <video
        key={entry.path}
        src={api.rawURL(entry.path)}
        poster={entry.thumbnail ? api.thumbURL(entry.path, 640) : undefined}
        controls
        preload="metadata"
        onError={() => setFailed(true)}
        className="max-h-full max-w-full rounded-lg"
      />
    </div>
  )
}

/** waveformBars builds a stable bar pattern from the file name. */
function useWaveform(seed: string, count: number): number[] {
  return useMemo(() => {
    let hash = 2166136261
    for (let index = 0; index < seed.length; index++) {
      hash ^= seed.charCodeAt(index)
      hash = Math.imul(hash, 16777619)
    }
    let value = hash >>> 0
    const out: number[] = []
    for (let index = 0; index < count; index++) {
      value = (value * 1664525 + 1013904223) >>> 0
      out.push(24 + (value % 74))
    }
    return out
  }, [seed, count])
}

function AudioPreview({ entry, compact }: { entry: Entry; compact: boolean }) {
  const [failed, setFailed] = useState(false)
  const bars = useWaveform(entry.name, compact ? 32 : 56)

  if (failed) {
    return (
      <Notice
        icon="alert"
        tone="danger"
        title="This audio cannot be played here"
        message="The browser does not support this format."
        action={compact ? undefined : <DownloadLink entry={entry} />}
      />
    )
  }

  return (
    <div className="flex h-full min-h-0 w-full flex-col justify-center gap-3 p-4">
      <div className="flex items-center gap-3">
        <span className="flex h-10 w-10 shrink-0 items-center justify-center rounded-xl bg-elevated text-success">
          <Icon name="music" size={20} />
        </span>
        <div className="min-w-0 flex-1">
          <p className="truncate text-sm font-medium text-ink" title={entry.name}>
            {entry.name}
          </p>
          <p className="text-xs text-faint">{bytes(entry.size)}</p>
        </div>
      </div>

      <div className="flex h-14 items-center gap-[3px] rounded-xl bg-elevated px-3" aria-hidden="true">
        {bars.map((height, index) => (
          <span
            key={index}
            className="flex-1 rounded-full bg-success/60"
            style={{ height: `${height}%`, minWidth: '2px' }}
          />
        ))}
      </div>

      <audio
        key={entry.path}
        src={api.rawURL(entry.path)}
        controls
        preload="metadata"
        onError={() => setFailed(true)}
        className="w-full"
      />
    </div>
  )
}

function PdfPreview({ entry }: { entry: Entry }) {
  return (
    <object
      data={api.rawURL(entry.path)}
      type="application/pdf"
      aria-label={`Preview of ${entry.name}`}
      className="h-full min-h-0 w-full"
    >
      <Notice
        icon="pdf"
        title="The document cannot be shown inline"
        message="This browser has no built in document viewer."
        action={<DownloadLink entry={entry} />}
      />
    </object>
  )
}

function TextBody({ content, onEdit }: { content: string; onEdit?: () => void }) {
  const lines = useMemo(() => content.split('\n'), [content])
  const shown = lines.length > MAX_PREVIEW_LINES ? lines.slice(0, MAX_PREVIEW_LINES) : lines
  const clipped = shown.length < lines.length

  return (
    <div className="flex h-full min-h-0 w-full flex-col">
      {clipped && (
        <Banner>{`Showing the first ${MAX_PREVIEW_LINES.toLocaleString()} lines of ${counted(lines.length, 'line')}`}</Banner>
      )}
      <div className="min-h-0 flex-1 overflow-auto sx-scroll">
        <div className="flex min-w-full font-mono text-xs leading-5">
          <div
            className="sticky left-0 shrink-0 select-none border-r border-line bg-surface px-3 py-3 text-right text-faint"
            aria-hidden="true"
          >
            {shown.map((_line, index) => (
              <div key={index}>{index + 1}</div>
            ))}
          </div>
          <pre className="min-w-0 flex-1 whitespace-pre px-3 py-3 text-ink">
            {shown.map((line, index) => (
              <div key={index}>{line === '' ? ' ' : line}</div>
            ))}
          </pre>
        </div>
      </div>
      {onEdit && (
        <div className="flex shrink-0 items-center justify-end gap-2 border-t border-line px-3 py-2">
          <Button icon="edit" onClick={onEdit}>
            Edit
          </Button>
        </div>
      )}
    </div>
  )
}

function TextPreview({ entry }: { entry: Entry }) {
  const client = useQueryClient()
  const [editing, setEditing] = useState(false)
  const query = useQuery({ queryKey: ['text', entry.path], queryFn: () => api.readText(entry.path) })

  if (editing) {
    return (
      <Suspense
        fallback={
          <div className="flex h-full w-full items-center justify-center gap-2 text-xs text-faint">
            <Spinner size={16} className="text-primary" />
            Opening the editor
          </div>
        }
      >
        <CodeEditor
          path={entry.path}
          onClose={() => setEditing(false)}
          onSaved={() => {
            void client.invalidateQueries({ queryKey: ['text', entry.path] })
          }}
        />
      </Suspense>
    )
  }

  if (query.isPending) return <LoadingLines rows={10} />

  if (query.isError) {
    return (
      <Notice
        icon="alert"
        tone="danger"
        title="This file could not be read"
        message={messageOf(query.error)}
        action={
          <Button icon="refresh" onClick={() => void query.refetch()}>
            Try again
          </Button>
        }
      />
    )
  }

  const file = query.data
  if (file.binary) {
    return (
      <Notice
        icon="file"
        title="This file is not readable text"
        message="It holds binary data, so there is nothing sensible to show."
        action={<DownloadLink entry={entry} />}
      />
    )
  }

  if (file.content.length === 0) {
    return <Notice icon="file" title="This file is empty" message="There is nothing in it yet." />
  }

  return (
    <div className="flex h-full min-h-0 w-full flex-col">
      {file.truncated && <Banner>Showing the first 8 MB</Banner>}
      <div className="min-h-0 flex-1">
        <TextBody
          content={file.content}
          onEdit={entry.editable && !file.readOnly && !file.truncated ? () => setEditing(true) : undefined}
        />
      </div>
    </div>
  )
}

function ArchivePreview({ entry }: { entry: Entry }) {
  const client = useQueryClient()
  const toast = useToast()
  const dir = parentPath(entry.path) || '/'
  const query = useQuery({ queryKey: ['archive', entry.path], queryFn: () => api.archivePreview(entry.path) })

  const extract = useMutation({
    mutationFn: () => api.extract(entry.path, dir),
    onSuccess: () => {
      toast.success('Extracting', `The contents are being unpacked into ${dir}.`)
      void client.invalidateQueries({ queryKey: ['jobs'] })
      void client.invalidateQueries({ queryKey: ['list', dir] })
    },
    onError: (error) => toast.error('Could not extract', messageOf(error)),
  })

  if (query.isPending) return <LoadingLines rows={7} />

  if (query.isError) {
    return (
      <Notice
        icon="alert"
        tone="danger"
        title="This archive could not be read"
        message={messageOf(query.error)}
        action={
          <Button icon="refresh" onClick={() => void query.refetch()}>
            Try again
          </Button>
        }
      />
    )
  }

  const listing = query.data
  if (listing.items.length === 0) {
    return <Notice icon="archive" title="This archive is empty" message="There are no files inside it." />
  }

  return (
    <div className="flex h-full min-h-0 w-full flex-col">
      <div className="flex shrink-0 items-center gap-2 border-b border-line px-3 py-2">
        <span className="sx-chip uppercase">{listing.format || 'archive'}</span>
        <span className="text-xs text-muted">
          {counted(listing.items.length, 'item')}
          {listing.truncated ? ' shown' : ''}
        </span>
        <span className="flex-1" />
        <Button icon="folder-open" loading={extract.isPending} onClick={() => extract.mutate()}>
          Extract here
        </Button>
      </div>

      <div className="min-h-0 flex-1 overflow-auto sx-scroll">
        <table className="w-full text-left text-xs">
          <thead className="sticky top-0 bg-surface text-faint">
            <tr>
              <th scope="col" className="px-3 py-2 font-medium">
                Name
              </th>
              <th scope="col" className="w-24 px-3 py-2 text-right font-medium">
                Size
              </th>
            </tr>
          </thead>
          <tbody>
            {listing.items.map((item) => (
              <tr key={item.name} className="border-t border-line/60">
                <td className="max-w-0 px-3 py-1.5">
                  <span className="flex items-center gap-2">
                    <Icon
                      name={item.isDir ? 'folder' : 'file'}
                      size={14}
                      className={clsx('shrink-0', item.isDir ? 'text-primary' : 'text-faint')}
                    />
                    <span className="truncate text-ink" title={item.name}>
                      {item.name}
                    </span>
                  </span>
                </td>
                <td className="px-3 py-1.5 text-right text-muted">{item.isDir ? '' : bytes(item.size)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      {listing.truncated && <Banner>Only the first part of the archive is listed</Banner>}
    </div>
  )
}

function FolderPreview({ entry, compact }: { entry: Entry; compact: boolean }) {
  if (compact) return <GenericPreview entry={entry} compact />
  return (
    <Notice
      icon="folder-open"
      title={entry.name}
      message={`Folder, last changed ${smartDate(entry.modified).toLowerCase()}`}
    />
  )
}

// ---- entry point ------------------------------------------------------------

/** PreviewViewer shows one entry inline. Set controls to false for a compact
 *  version that fits in a narrow panel header. */
export default function PreviewViewer({ entry, className, controls = true }: PreviewViewerProps) {
  const compact = controls === false

  let body: ReactNode
  if (entry.broken) {
    body = (
      <Notice
        icon="alert"
        tone="danger"
        title="This shortcut is broken"
        message={entry.linkTarget ? `It points at ${entry.linkTarget}, which is no longer there.` : undefined}
      />
    )
  } else if (entry.isDir) {
    body = <FolderPreview entry={entry} compact={compact} />
  } else {
    switch (entry.kind) {
      case 'image':
        body = <ImagePreview entry={entry} compact={compact} />
        break
      case 'video':
        body = <VideoPreview entry={entry} compact={compact} />
        break
      case 'audio':
        body = <AudioPreview entry={entry} compact={compact} />
        break
      case 'pdf':
        body = compact ? <GenericPreview entry={entry} compact /> : <PdfPreview entry={entry} />
        break
      case 'text':
      case 'code':
        body = compact ? <GenericPreview entry={entry} compact /> : <TextPreview entry={entry} />
        break
      case 'archive':
        body = compact ? <GenericPreview entry={entry} compact /> : <ArchivePreview entry={entry} />
        break
      default:
        body = <GenericPreview entry={entry} compact={compact} />
        break
    }
  }

  return <div className={clsx('flex h-full min-h-0 w-full flex-col overflow-hidden', className)}>{body}</div>
}
