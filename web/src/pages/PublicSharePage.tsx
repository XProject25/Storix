// The page a visitor sees when they open a link. Nobody here is signed in, so
// it carries its own frame and never reveals a server path.
// Developed by X Project.

import clsx from 'clsx'
import { useEffect, useRef, useState, type DragEvent, type FormEvent, type ReactNode } from 'react'
import { useParams } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import * as tus from 'tus-js-client'
import { API_BASE, ApiError, api } from '../lib/api'
import { bytes, smartDate } from '../lib/format'
import type { Entry, PublicShare } from '../lib/types'
import { collectFiles, fromInput } from '../state/transfers'
import { Logo } from '../components/Logo'
import { Icon, colourForKind, iconForKind } from '../components/Icon'
import { Button, EmptyState, IconButton, Progress, Skeleton, Spinner } from '../components/ui'

export default function PublicSharePage() {
  const { token = '' } = useParams<{ token: string }>()
  const [rel, setRel] = useState('')

  const meta = useQuery({
    queryKey: ['public', token, rel],
    queryFn: () => api.publicMeta(token, rel || undefined),
    retry: false,
    enabled: token !== '',
  })

  const error = meta.error
  const locked = error instanceof ApiError && (error.is('password_required') || error.status === 401)

  if (token !== '' && meta.isLoading) {
    return (
      <Shell>
        <div className="sx-panel space-y-4 p-6">
          <Skeleton className="h-5 w-44" />
          <Skeleton className="h-3.5 w-64" />
          <Skeleton className="h-40 w-full rounded-2xl" />
          <Skeleton className="h-9 w-40 rounded-xl" />
        </div>
      </Shell>
    )
  }

  if (locked) {
    return (
      <Shell>
        <Unlock token={token} onUnlocked={() => void meta.refetch()} />
      </Shell>
    )
  }

  if (meta.isError || !meta.data) {
    return (
      <Shell>
        <div className="sx-panel">
          <EmptyState
            icon="alert"
            title="This link is no longer available"
            message="Ask the person who sent it for a new one."
          />
        </div>
      </Shell>
    )
  }

  const share = meta.data

  return (
    <Shell>
      {share.kind === 'upload' ? (
        <UploadRequest token={token} share={share} />
      ) : share.isDir ? (
        <FolderView token={token} share={share} rel={rel} onNavigate={setRel} loading={meta.isFetching} />
      ) : (
        <FileView token={token} share={share} />
      )}
    </Shell>
  )
}

// ---- frame -------------------------------------------------------------------

function Shell({ children }: { children: ReactNode }) {
  return (
    <div className="sx-scroll h-full bg-bg">
      <div className="mx-auto flex min-h-full w-full max-w-3xl flex-col px-4 py-8 sm:py-12">
        <header className="mb-6 flex justify-center">
          <Logo size={34} />
        </header>
        <main className="flex-1">{children}</main>
        <footer className="mt-10 text-center text-xs text-faint">Developed by X Project</footer>
      </div>
    </div>
  )
}

function Header({ share, subtitle }: { share: PublicShare; subtitle: string }) {
  return (
    <div className="mb-4 flex items-start gap-3">
      <span
        className={clsx(
          'flex h-11 w-11 shrink-0 items-center justify-center rounded-2xl bg-elevated',
          colourForKind(share.entries[0]?.kind ?? 'other', share.isDir),
        )}
      >
        <Icon name={share.kind === 'upload' ? 'cloud-upload' : iconForKind(share.entries[0]?.kind ?? 'other', share.isDir)} size={21} />
      </span>
      <div className="min-w-0 flex-1">
        <h1 className="truncate text-lg font-semibold tracking-tight text-ink">{share.name}</h1>
        <p className="mt-0.5 text-sm text-muted">{subtitle}</p>
      </div>
    </div>
  )
}

function Note({ text }: { text: string }) {
  if (!text.trim()) return null
  return (
    <p className="mb-4 rounded-xl border border-line bg-elevated/60 px-3.5 py-2.5 text-sm text-muted">{text}</p>
  )
}

function Expiry({ at }: { at?: string }) {
  if (!at) return null
  const past = new Date(at).getTime() <= Date.now()
  return (
    <p className="mt-4 flex items-center gap-1.5 text-xs text-faint">
      <Icon name="clock" size={13} />
      {past ? 'This link has expired' : `Available until ${smartDate(at)}`}
    </p>
  )
}

// ---- password ----------------------------------------------------------------

function Unlock({ token, onUnlocked }: { token: string; onUnlocked: () => void }) {
  const [password, setPassword] = useState('')
  const [busy, setBusy] = useState(false)
  const [problem, setProblem] = useState('')

  const submit = async (event: FormEvent) => {
    event.preventDefault()
    if (!password || busy) return
    setBusy(true)
    setProblem('')
    try {
      await api.publicAuth(token, password)
      onUnlocked()
    } catch (failure) {
      // The answer never says whether the link exists, only that this attempt
      // did not work.
      if (failure instanceof ApiError && failure.is('rate_limited')) {
        setProblem('Too many attempts. Wait a moment and try again.')
      } else {
        setProblem('That password did not work. Check it and try again.')
      }
      setBusy(false)
      return
    }
    setBusy(false)
  }

  return (
    <form className="sx-panel p-6" onSubmit={submit}>
      <div className="mb-4 flex items-start gap-3">
        <span className="flex h-11 w-11 shrink-0 items-center justify-center rounded-2xl bg-primary/12 text-primary">
          <Icon name="lock" size={20} />
        </span>
        <div className="min-w-0 flex-1">
          <h1 className="text-lg font-semibold tracking-tight text-ink">This link needs a password</h1>
          <p className="mt-0.5 text-sm text-muted">
            The person who sent you this link set a password. Enter it to continue.
          </p>
        </div>
      </div>

      <label className="sx-label" htmlFor="sx-public-password">
        Password
      </label>
      <input
        id="sx-public-password"
        className="sx-input"
        type="password"
        autoComplete="current-password"
        autoFocus
        value={password}
        onChange={(event) => setPassword(event.target.value)}
      />
      {problem && (
        <p className="mt-2 flex items-start gap-2 text-sm text-danger" role="alert">
          <Icon name="alert" size={15} className="mt-0.5 shrink-0" />
          <span>{problem}</span>
        </p>
      )}
      <div className="mt-4">
        <Button type="submit" variant="primary" block loading={busy} disabled={!password}>
          Continue
        </Button>
      </div>
    </form>
  )
}

// ---- one file ----------------------------------------------------------------

function FileView({ token, share }: { token: string; share: PublicShare }) {
  const entry = share.entries[0]
  const size = entry ? bytes(entry.size) : ''
  const modified = entry?.modified ? smartDate(entry.modified) : ''

  return (
    <div className="sx-panel p-6">
      <Header share={share} subtitle={[size, modified].filter(Boolean).join('  |  ')} />
      <Note text={share.note} />
      {entry && share.allowDownload && <FilePreview token={token} entry={entry} />}

      {share.allowDownload ? (
        <a
          className="sx-btn-primary mt-5 w-full"
          href={api.publicDownloadURL(token, entry?.path)}
          rel="noopener"
        >
          <Icon name="download" size={17} />
          Download {size ? `(${size})` : ''}
        </a>
      ) : (
        <p className="mt-5 rounded-xl border border-line bg-elevated/60 px-3.5 py-2.5 text-sm text-muted">
          This link does not allow the file to be opened. Ask the person who sent it for a new one.
        </p>
      )}
      <Expiry at={share.expiresAt} />
    </div>
  )
}

function FilePreview({ token, entry }: { token: string; entry: Entry }) {
  const src = api.publicRawURL(token, entry.path)

  if (entry.kind === 'image') {
    return (
      <div className="flex items-center justify-center overflow-hidden rounded-2xl border border-line bg-elevated p-2">
        <img src={src} alt={entry.name} className="max-h-[60vh] w-auto max-w-full rounded-xl object-contain" />
      </div>
    )
  }
  if (entry.kind === 'video') {
    return (
      <video
        controls
        preload="metadata"
        className="w-full rounded-2xl border border-line bg-black"
        src={src}
        style={{ maxHeight: '60vh' }}
      />
    )
  }
  if (entry.kind === 'audio') {
    return (
      <div className="rounded-2xl border border-line bg-elevated p-4">
        <audio controls className="w-full" src={src} />
      </div>
    )
  }
  if (entry.kind === 'pdf') {
    return (
      <div>
        <iframe
          title={entry.name}
          src={src}
          className="h-[60vh] w-full rounded-2xl border border-line bg-elevated"
        />
        <p className="mt-2 text-xs text-faint">
          If the preview stays empty, download the file and open it on your device.
        </p>
      </div>
    )
  }
  if (entry.kind === 'text' || entry.kind === 'code') {
    return <TextPreview src={src} size={entry.size} />
  }
  return null
}

const TEXT_PREVIEW_LIMIT = 256 * 1024

function TextPreview({ src, size }: { src: string; size: number }) {
  const preview = useQuery({
    queryKey: ['public-text', src],
    queryFn: async () => {
      const response = await fetch(src, { credentials: 'same-origin' })
      if (!response.ok) throw new Error('The preview could not be loaded')
      const text = await response.text()
      return text.slice(0, TEXT_PREVIEW_LIMIT)
    },
    retry: false,
    enabled: size <= TEXT_PREVIEW_LIMIT,
  })

  if (size > TEXT_PREVIEW_LIMIT) {
    return (
      <p className="rounded-2xl border border-line bg-elevated/60 px-3.5 py-2.5 text-sm text-muted">
        This file is too large to preview here. Download it to read the whole thing.
      </p>
    )
  }
  if (preview.isLoading) {
    return (
      <div className="flex h-32 items-center justify-center rounded-2xl border border-line bg-elevated">
        <Spinner size={18} className="text-primary" />
      </div>
    )
  }
  if (preview.isError || preview.data === undefined) {
    return (
      <p className="rounded-2xl border border-line bg-elevated/60 px-3.5 py-2.5 text-sm text-muted">
        The preview could not be loaded. The file itself is still available.
      </p>
    )
  }
  return (
    <pre className="sx-scroll max-h-[50vh] overflow-x-auto rounded-2xl border border-line bg-elevated p-4 font-mono text-xs leading-relaxed text-muted">
      {preview.data}
    </pre>
  )
}

// ---- a folder ----------------------------------------------------------------

function FolderView({
  token,
  share,
  rel,
  onNavigate,
  loading,
}: {
  token: string
  share: PublicShare
  rel: string
  onNavigate: (path: string) => void
  loading: boolean
}) {
  const crumbs = share.breadcrumbs.length > 0 ? share.breadcrumbs : [{ name: share.name, path: '/' }]
  const canBrowse = share.allowList

  return (
    <div className="sx-panel overflow-hidden">
      <div className="p-6 pb-4">
        <Header
          share={share}
          subtitle={
            canBrowse
              ? `${share.entries.length} ${share.entries.length === 1 ? 'item' : 'items'} shared by ${share.owner}`
              : `Shared by ${share.owner}`
          }
        />
        <Note text={share.note} />

        {canBrowse && (
          <nav aria-label="Folder path" className="flex flex-wrap items-center gap-1 text-sm">
            {crumbs.map((crumb, index) => {
              const last = index === crumbs.length - 1
              const target = index === 0 ? '' : crumb.path
              return (
                <span key={crumb.path} className="flex items-center gap-1">
                  {index > 0 && <Icon name="chevron-right" size={14} className="text-faint" />}
                  {last ? (
                    <span className="truncate text-ink">{crumb.name}</span>
                  ) : (
                    <button
                      type="button"
                      className="truncate rounded-lg px-1 text-muted transition-colors hover:text-ink"
                      onClick={() => onNavigate(target)}
                    >
                      {crumb.name}
                    </button>
                  )}
                </span>
              )
            })}
          </nav>
        )}

        {share.allowDownload && canBrowse && (
          <a className="sx-btn-secondary mt-4" href={api.publicZipURL(token, rel || undefined)} rel="noopener">
            <Icon name="archive" size={16} />
            Download all as zip
          </a>
        )}
      </div>

      {!canBrowse ? (
        <EmptyState
          icon="lock"
          title="Browsing is turned off"
          message="The person who shared this folder did not allow its contents to be listed."
          action={
            share.allowDownload ? (
              <a className="sx-btn-primary" href={api.publicZipURL(token)} rel="noopener">
                <Icon name="download" size={16} />
                Download the folder
              </a>
            ) : undefined
          }
        />
      ) : share.entries.length === 0 ? (
        <EmptyState icon="folder-open" title="This folder is empty" message="There is nothing to download here yet." />
      ) : (
        <ul className={clsx('border-t border-line', loading && 'opacity-60')}>
          {share.entries.map((entry) => (
            <li key={entry.path} className="border-b border-line/60 last:border-0">
              <div className="flex items-center gap-3 px-4 py-2.5 transition-colors hover:bg-elevated/70 sm:px-6">
                {entry.isDir ? (
                  <button
                    type="button"
                    className="flex min-w-0 flex-1 items-center gap-3 text-left"
                    onClick={() => onNavigate(entry.path)}
                  >
                    <EntryIcon token={token} entry={entry} />
                    <span className="min-w-0 flex-1">
                      <span className="block truncate text-sm text-ink">{entry.name}</span>
                      <span className="block text-[11px] text-faint">Folder</span>
                    </span>
                    <Icon name="chevron-right" size={16} className="shrink-0 text-faint" />
                  </button>
                ) : (
                  <>
                    <EntryIcon token={token} entry={entry} />
                    <span className="min-w-0 flex-1">
                      <span className="block truncate text-sm text-ink">{entry.name}</span>
                      <span className="block text-[11px] text-faint">
                        {bytes(entry.size)}
                        {entry.modified ? `  |  ${smartDate(entry.modified)}` : ''}
                      </span>
                    </span>
                    {share.allowDownload && (
                      <a
                        className="inline-flex h-9 w-9 shrink-0 items-center justify-center rounded-xl text-muted transition-colors hover:bg-elevated hover:text-ink"
                        href={api.publicDownloadURL(token, entry.path)}
                        title={`Download ${entry.name}`}
                        aria-label={`Download ${entry.name}`}
                        rel="noopener"
                      >
                        <Icon name="download" size={17} />
                      </a>
                    )}
                  </>
                )}
              </div>
            </li>
          ))}
        </ul>
      )}

      <div className="px-6 pb-5">
        <Expiry at={share.expiresAt} />
      </div>
    </div>
  )
}

function EntryIcon({ token, entry }: { token: string; entry: Entry }) {
  if (entry.thumbnail) {
    return (
      <img
        src={api.publicThumbURL(token, entry.path, 64)}
        alt=""
        className="h-9 w-9 shrink-0 rounded-lg border border-line object-cover"
        loading="lazy"
      />
    )
  }
  return (
    <span className={clsx('flex h-9 w-9 shrink-0 items-center justify-center', colourForKind(entry.kind, entry.isDir))}>
      <Icon name={iconForKind(entry.kind, entry.isDir)} size={19} />
    </span>
  )
}

// ---- an upload request -------------------------------------------------------

type QueueStatus = 'queued' | 'uploading' | 'done' | 'error'

interface QueueItem {
  id: string
  name: string
  size: number
  uploaded: number
  status: QueueStatus
  error?: string
}

const MAX_PARALLEL = 2
const CHUNK_SIZE = 8 * 1024 * 1024

let queueCounter = 0

function UploadRequest({ token, share }: { token: string; share: PublicShare }) {
  const [queue, setQueue] = useState<QueueItem[]>([])
  const [dragging, setDragging] = useState(false)
  const itemsRef = useRef<QueueItem[]>([])
  const sourcesRef = useRef(new Map<string, { file: File; relativePath: string }>())
  const uploadsRef = useRef(new Map<string, tus.Upload>())
  const inputRef = useRef<HTMLInputElement>(null)

  // A visitor leaving the page should not leave half open connections behind.
  useEffect(() => {
    const uploads = uploadsRef.current
    return () => {
      for (const upload of uploads.values()) upload.abort()
      uploads.clear()
    }
  }, [])

  const sync = () => setQueue(itemsRef.current.map((item) => ({ ...item })))

  const startItem = (item: QueueItem) => {
    const source = sourcesRef.current.get(item.id)
    if (!source) return
    item.status = 'uploading'
    const upload = new tus.Upload(source.file, {
      endpoint: `${API_BASE}/public/${encodeURIComponent(token)}/tus`,
      chunkSize: CHUNK_SIZE,
      retryDelays: [0, 1000, 3000, 6000, 12000],
      storeFingerprintForResuming: false,
      metadata: {
        filename: source.file.name,
        relativePath: source.relativePath,
      },
      onProgress: (uploaded, total) => {
        item.uploaded = uploaded
        item.size = total || item.size
        sync()
      },
      onSuccess: () => {
        item.status = 'done'
        item.uploaded = item.size
        uploadsRef.current.delete(item.id)
        sync()
        pump()
      },
      onError: (failure) => {
        item.status = 'error'
        item.error = uploadMessage(failure)
        uploadsRef.current.delete(item.id)
        sync()
        pump()
      },
    })
    uploadsRef.current.set(item.id, upload)
    upload.start()
    sync()
  }

  const pump = () => {
    const running = () => itemsRef.current.filter((item) => item.status === 'uploading').length
    if (running() >= MAX_PARALLEL) return
    for (const item of itemsRef.current) {
      if (item.status !== 'queued') continue
      startItem(item)
      if (running() >= MAX_PARALLEL) break
    }
  }

  const add = (files: Array<{ file: File; relativePath: string }>) => {
    if (files.length === 0) return
    for (const source of files) {
      queueCounter += 1
      const id = `q${Date.now().toString(36)}${queueCounter}`
      sourcesRef.current.set(id, source)
      itemsRef.current.push({
        id,
        name: source.file.name,
        size: source.file.size,
        uploaded: 0,
        status: 'queued',
      })
    }
    sync()
    pump()
  }

  const retry = (id: string) => {
    const item = itemsRef.current.find((entry) => entry.id === id)
    if (!item) return
    item.status = 'queued'
    item.uploaded = 0
    item.error = undefined
    sync()
    pump()
  }

  const remove = (id: string) => {
    uploadsRef.current.get(id)?.abort()
    uploadsRef.current.delete(id)
    sourcesRef.current.delete(id)
    itemsRef.current = itemsRef.current.filter((entry) => entry.id !== id)
    sync()
    pump()
  }

  const reset = () => {
    for (const upload of uploadsRef.current.values()) upload.abort()
    uploadsRef.current.clear()
    sourcesRef.current.clear()
    itemsRef.current = []
    sync()
  }

  const onDrop = (event: DragEvent<HTMLDivElement>) => {
    event.preventDefault()
    setDragging(false)
    void collectFiles(event.dataTransfer).then(add)
  }

  if (!share.allowUpload) {
    return (
      <div className="sx-panel">
        <EmptyState
          icon="lock"
          title="This link is not accepting files"
          message="Ask the person who sent it to open it again."
        />
      </div>
    )
  }

  const finished = queue.length > 0 && queue.every((item) => item.status === 'done')
  const totalBytes = queue.reduce((sum, item) => sum + item.size, 0)
  const doneBytes = queue.reduce((sum, item) => sum + item.uploaded, 0)
  const active = queue.some((item) => item.status === 'uploading' || item.status === 'queued')

  return (
    <div className="sx-panel p-6">
      <Header share={share} subtitle={`${share.owner} is asking you for files`} />
      <Note text={share.note} />

      {finished ? (
        <div className="rounded-2xl border border-success/40 bg-success/10 px-5 py-8 text-center">
          <span className="mx-auto mb-3 flex h-12 w-12 items-center justify-center rounded-2xl bg-success/15 text-success">
            <Icon name="check-circle" size={24} />
          </span>
          <h2 className="text-[15px] font-medium text-ink">Thank you, everything arrived</h2>
          <p className="mt-1.5 text-sm text-muted">
            {queue.length === 1 ? 'Your file has been' : `All ${queue.length} files have been`} delivered to {share.name}.
          </p>
          <div className="mt-5 flex justify-center">
            <Button icon="plus" onClick={reset}>
              Send more files
            </Button>
          </div>
        </div>
      ) : (
        <div
          onDragOver={(event) => {
            event.preventDefault()
            setDragging(true)
          }}
          onDragLeave={() => setDragging(false)}
          onDrop={onDrop}
        >
          <button
            type="button"
            onClick={() => inputRef.current?.click()}
            className={clsx(
              'flex w-full flex-col items-center justify-center gap-2 rounded-2xl border-2 border-dashed px-5 py-12 text-center transition-colors',
              dragging ? 'border-primary bg-primary/10' : 'border-line bg-elevated/50 hover:border-primary/50',
            )}
          >
            <span className="flex h-12 w-12 items-center justify-center rounded-2xl bg-primary/12 text-primary">
              <Icon name="cloud-upload" size={24} />
            </span>
            <span className="text-[15px] font-medium text-ink">Upload your files here</span>
            <span className="max-w-sm text-sm text-muted">
              Drag files or folders onto this box, or choose them from your device. Large files continue where they
              stopped if your connection drops.
            </span>
          </button>
          <input
            ref={inputRef}
            type="file"
            multiple
            className="hidden"
            onChange={(event) => {
              add(fromInput(event.target.files))
              event.target.value = ''
            }}
          />
        </div>
      )}

      {queue.length > 0 && !finished && (
        <div className="mt-5">
          <div className="mb-2 flex items-center justify-between text-xs text-muted">
            <span>
              {queue.filter((item) => item.status === 'done').length} of {queue.length} sent
            </span>
            <span>
              {bytes(doneBytes)} of {bytes(totalBytes)}
            </span>
          </div>
          <Progress value={totalBytes > 0 ? (doneBytes / totalBytes) * 100 : 0} />
          <ul className="mt-3 space-y-1">
            {queue.map((item) => (
              <li key={item.id} className="flex items-center gap-3 rounded-xl px-2 py-2 hover:bg-elevated/70">
                <span
                  className={clsx(
                    'flex h-8 w-8 shrink-0 items-center justify-center rounded-lg',
                    item.status === 'done'
                      ? 'text-success'
                      : item.status === 'error'
                        ? 'text-danger'
                        : 'text-muted',
                  )}
                >
                  <Icon
                    name={item.status === 'done' ? 'check-circle' : item.status === 'error' ? 'alert' : 'file'}
                    size={17}
                  />
                </span>
                <div className="min-w-0 flex-1">
                  <div className="truncate text-sm text-ink">{item.name}</div>
                  {item.status === 'error' ? (
                    <div className="truncate text-[11px] text-danger">{item.error}</div>
                  ) : item.status === 'done' ? (
                    <div className="text-[11px] text-faint">Sent  |  {bytes(item.size)}</div>
                  ) : (
                    <div className="mt-1">
                      <Progress value={item.size > 0 ? (item.uploaded / item.size) * 100 : 0} />
                    </div>
                  )}
                </div>
                {item.status === 'error' && (
                  <IconButton icon="refresh" label={`Try ${item.name} again`} size={15} onClick={() => retry(item.id)} />
                )}
                {item.status !== 'done' && (
                  <IconButton icon="close" label={`Remove ${item.name}`} size={15} onClick={() => remove(item.id)} />
                )}
              </li>
            ))}
          </ul>
          {active && <p className="mt-2 text-xs text-faint">Keep this page open until every file is sent.</p>}
        </div>
      )}

      <Expiry at={share.expiresAt} />
    </div>
  )
}

/** uploadMessage turns a tus failure into something a visitor can act on. */
function uploadMessage(failure: unknown): string {
  const text = failure instanceof Error ? failure.message : String(failure)
  if (text.includes('403')) return 'This link is not accepting files any more'
  if (text.includes('404') || text.includes('410')) return 'This link is no longer available'
  if (text.includes('413')) return 'This file is larger than the server allows'
  if (text.includes('401')) return 'The link was locked again, reload the page'
  if (text.includes('Failed to fetch') || text.toLowerCase().includes('network')) {
    return 'The connection dropped, try again'
  }
  return text.replace(/^tus:\s*/, '')
}
