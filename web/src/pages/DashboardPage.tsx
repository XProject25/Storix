// The home screen: where you are, what changed and what to do next.
// Developed by X Project.

import clsx from 'clsx'
import { useRef } from 'react'
import { useNavigate } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { api } from '../lib/api'
import type { Dashboard, Kind, Mount, Recent } from '../lib/types'
import { ago, bytes, counted, extensionOf, parentPath, percent as percentText, truncateMiddle } from '../lib/format'
import { useSession } from '../lib/session'
import { useApp } from '../state/app'
import { fromInput, useTransfers } from '../state/transfers'
import { Icon, colourForKind, iconForKind, type IconName } from '../components/Icon'
import { Button, EmptyState, Progress, SectionTitle, Skeleton, useToast } from '../components/ui'
import UploadZone from '../components/UploadZone'

/** folderRoute builds the browser route for a folder, keeping odd names intact. */
function folderRoute(path: string): string {
  const clean = path.replace(/\/+$/, '')
  if (!clean) return '/files'
  return '/files' + clean.split('/').map(encodeURIComponent).join('/')
}

/** selectRoute opens the folder that holds a file, with the file selected. */
function selectRoute(path: string, name: string): string {
  return `${folderRoute(parentPath(path) || '/')}?select=${encodeURIComponent(name)}`
}

/** today renders the long form date under the greeting. */
function today(): string {
  return new Date().toLocaleDateString(undefined, { weekday: 'long', day: 'numeric', month: 'long' })
}

const KINDS: Record<string, Kind> = {
  png: 'image', jpg: 'image', jpeg: 'image', gif: 'image', webp: 'image', svg: 'image', avif: 'image', heic: 'image',
  mp4: 'video', mkv: 'video', mov: 'video', avi: 'video', webm: 'video', m4v: 'video', mpg: 'video',
  mp3: 'audio', flac: 'audio', wav: 'audio', m4a: 'audio', ogg: 'audio', aac: 'audio',
  pdf: 'pdf',
  zip: 'archive', tar: 'archive', gz: 'archive', bz2: 'archive', xz: 'archive', rar: 'archive', '7z': 'archive',
  js: 'code', ts: 'code', tsx: 'code', jsx: 'code', go: 'code', py: 'code', rs: 'code', sh: 'code', json: 'code',
  yml: 'code', yaml: 'code', html: 'code', css: 'code', php: 'code', sql: 'code',
  txt: 'text', md: 'text', log: 'text', conf: 'text', ini: 'text',
  doc: 'document', docx: 'document', xls: 'document', xlsx: 'document', ppt: 'document', pptx: 'document',
  iso: 'disk', img: 'disk', qcow2: 'disk',
  ttf: 'font', otf: 'font', woff: 'font', woff2: 'font',
}

/** kindOf guesses a file family from the name, for records that carry no kind. */
function kindOf(name: string, isDir: boolean): Kind {
  if (isDir) return 'folder'
  return KINDS[extensionOf(name)] ?? 'other'
}

// ---- storage ----------------------------------------------------------------

function StorageRing({ value }: { value: number }) {
  const size = 152
  const stroke = 13
  const radius = (size - stroke) / 2
  const circumference = 2 * Math.PI * radius
  const clamped = Math.min(100, Math.max(0, value))
  const filled = (clamped / 100) * circumference
  const tight = clamped >= 90

  return (
    <div className="relative shrink-0" style={{ width: size, height: size }}>
      <svg width={size} height={size} viewBox={`0 0 ${size} ${size}`} aria-hidden="true" focusable="false">
        <defs>
          <linearGradient id="sx-storage-ring" x1="0" y1="0" x2="1" y2="1">
            <stop offset="0%" stopColor="rgb(var(--sx-secondary))" />
            <stop offset="100%" stopColor="rgb(var(--sx-primary))" />
          </linearGradient>
        </defs>
        <circle
          cx={size / 2}
          cy={size / 2}
          r={radius}
          fill="none"
          stroke="rgb(var(--sx-line))"
          strokeWidth={stroke}
        />
        <circle
          cx={size / 2}
          cy={size / 2}
          r={radius}
          fill="none"
          stroke={tight ? 'rgb(var(--sx-warning))' : 'url(#sx-storage-ring)'}
          strokeWidth={stroke}
          strokeLinecap="round"
          strokeDasharray={`${filled} ${Math.max(0, circumference - filled)}`}
          transform={`rotate(-90 ${size / 2} ${size / 2})`}
        />
      </svg>
      <div className="absolute inset-0 flex flex-col items-center justify-center">
        <span className="text-3xl font-semibold tracking-tight text-ink">{percentText(clamped)}</span>
        <span className="mt-0.5 text-xs text-faint">used</span>
      </div>
    </div>
  )
}

function StorageCard({ data, onOpen }: { data: Dashboard; onOpen: (mount: Mount) => void }) {
  const { total, used, free, percent } = data.storage
  const mounts = data.mounts.filter((mount) => mount.usage)

  return (
    <section className="sx-panel p-5">
      <SectionTitle>Storage</SectionTitle>
      <div className="flex flex-col items-center gap-6 sm:flex-row sm:items-center">
        <StorageRing value={percent} />
        <div className="min-w-0 flex-1">
          {total > 0 ? (
            <>
              <p className="text-lg font-medium text-ink">
                {bytes(used)} used of {bytes(total)}
              </p>
              <p className="mt-1 text-sm text-muted">{bytes(free)} still free on this volume</p>
            </>
          ) : (
            <>
              <p className="text-lg font-medium text-ink">Usage is not available</p>
              <p className="mt-1 text-sm text-muted">
                The server could not read this volume. Everything else keeps working.
              </p>
            </>
          )}
          {data.storage.path && (
            <p className="mt-3 inline-flex max-w-full items-center gap-2 text-xs text-faint">
              <Icon name="drive" size={14} />
              <span className="truncate font-mono">{data.storage.path}</span>
            </p>
          )}
        </div>
      </div>

      {mounts.length > 0 && (
        <div className="mt-6 space-y-1">
          {mounts.map((mount) => {
            const usage = mount.usage
            if (!usage) return null
            return (
              <button
                key={mount.path}
                type="button"
                onClick={() => onOpen(mount)}
                className="group flex w-full flex-col gap-1.5 rounded-xl px-3 py-2.5 text-left transition-colors hover:bg-elevated"
              >
                <span className="flex items-baseline justify-between gap-3">
                  <span className="flex min-w-0 items-center gap-2 text-sm text-ink">
                    <Icon name="folder" size={15} className="shrink-0 text-primary" />
                    <span className="truncate">{mount.label || mount.path}</span>
                    {mount.readOnly && <span className="sx-chip shrink-0">Read only</span>}
                  </span>
                  <span className="shrink-0 text-xs text-muted">
                    {bytes(usage.used)} of {bytes(usage.total)}
                  </span>
                </span>
                <Progress value={usage.percent} className="h-1" />
              </button>
            )
          })}
        </div>
      )}
    </section>
  )
}

// ---- small cards ------------------------------------------------------------

const TONES = {
  primary: 'bg-primary/15 text-primary',
  accent: 'bg-accent/15 text-accent',
  warning: 'bg-warning/15 text-warning',
  success: 'bg-success/15 text-success',
} as const

function StatCard({
  icon,
  tone,
  label,
  value,
  hint,
  chip,
  onClick,
}: {
  icon: IconName
  tone: keyof typeof TONES
  label: string
  value: string
  hint?: string
  chip?: string
  onClick: () => void
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className="sx-panel flex flex-col items-start gap-3 p-4 text-left transition-colors hover:bg-elevated"
    >
      <span className={clsx('flex h-9 w-9 items-center justify-center rounded-xl', TONES[tone])}>
        <Icon name={icon} size={17} />
      </span>
      <span className="min-w-0 w-full">
        <span className="block text-xs text-faint">{label}</span>
        <span className="mt-0.5 flex items-center gap-2">
          <span className="truncate text-lg font-medium text-ink">{value}</span>
          {chip && (
            <span className="sx-chip shrink-0 border-primary/40 text-primary">
              <Icon name="zap" size={12} />
              {chip}
            </span>
          )}
        </span>
        {hint && <span className="mt-0.5 block truncate text-xs text-muted">{hint}</span>}
      </span>
    </button>
  )
}

function RecentRow({ item, onOpen }: { item: Recent; onOpen: (item: Recent) => void }) {
  const folder = parentPath(item.path) || '/'
  const kind = kindOf(item.name, item.isDir)
  return (
    <button type="button" onClick={() => onOpen(item)} className="sx-row w-full text-left" title={item.path}>
      <Icon
        name={iconForKind(kind, item.isDir)}
        size={18}
        className={clsx('shrink-0', colourForKind(kind, item.isDir))}
      />
      <span className="min-w-0 flex-1">
        <span className="block truncate text-sm text-ink">{item.name}</span>
        <span className="block truncate text-xs text-faint">{truncateMiddle(folder, 52)}</span>
      </span>
      <span className="shrink-0 text-xs text-muted">{ago(item.at)}</span>
    </button>
  )
}

// ---- skeleton and error -----------------------------------------------------

function DashboardSkeleton() {
  return (
    <div className="mx-auto w-full max-w-6xl space-y-6 px-6 py-8">
      <div className="space-y-2">
        <Skeleton className="h-7 w-64" />
        <Skeleton className="h-4 w-40" />
      </div>
      <Skeleton className="h-56 w-full rounded-2xl" />
      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
        {[0, 1, 2, 3].map((index) => (
          <Skeleton key={index} className="h-28 rounded-2xl" />
        ))}
      </div>
      <Skeleton className="h-72 w-full rounded-2xl" />
    </div>
  )
}

function ErrorCard({ message, onRetry, busy }: { message: string; onRetry: () => void; busy: boolean }) {
  return (
    <div className="mx-auto w-full max-w-2xl px-6 py-16">
      <div className="sx-panel p-8 text-center">
        <span className="mx-auto mb-4 flex h-12 w-12 items-center justify-center rounded-2xl bg-danger/15 text-danger">
          <Icon name="alert" size={22} />
        </span>
        <h2 className="text-[15px] font-medium text-ink">The home screen could not load</h2>
        <p className="mx-auto mt-1.5 max-w-sm text-sm text-muted">{message}</p>
        <div className="mt-5 flex justify-center">
          <Button variant="primary" icon="refresh" onClick={onRetry} loading={busy}>
            Try again
          </Button>
        </div>
      </div>
    </div>
  )
}

// ---- page -------------------------------------------------------------------

export default function DashboardPage() {
  const navigate = useNavigate()
  const { user, can } = useSession()
  const toast = useToast()
  const picker = useRef<HTMLInputElement>(null)
  const enqueue = useTransfers((state) => state.enqueue)
  const localActive = useTransfers((state) => state.active)
  const setTransfersOpen = useApp((state) => state.setTransfersOpen)

  const { data, isPending, isError, error, refetch, isFetching } = useQuery({
    queryKey: ['dashboard'],
    queryFn: () => api.dashboard(),
  })

  if (isPending) return <DashboardSkeleton />

  if (isError || !data) {
    const message =
      error instanceof Error && error.message ? error.message : 'The server did not answer. Check that it is running.'
    return <ErrorCard message={message} onRetry={() => void refetch()} busy={isFetching} />
  }

  const name = data.user?.displayName || data.user?.username || user?.displayName || user?.username || ''
  const uploadPath = data.mounts[0]?.path ?? data.storage.path ?? '/'
  const recent = data.recent.slice(0, 8)
  const activeTransfers = Math.max(localActive, data.transfers.active)

  const openPicker = () => picker.current?.click()

  const quickActions: Array<{ id: string; label: string; icon: IconName; run: () => void; primary?: boolean }> = []
  if (can('upload')) {
    quickActions.push({ id: 'upload', label: 'Upload files', icon: 'cloud-upload', run: openPicker, primary: true })
  }
  if (can('create')) {
    quickActions.push({
      id: 'folder',
      label: 'New folder',
      icon: 'folder-plus',
      run: () => navigate(`${folderRoute(uploadPath)}?new=folder`),
    })
  }
  if (can('share')) {
    quickActions.push({ id: 'share', label: 'Create share', icon: 'link', run: () => navigate('/shares?create=1') })
  }
  quickActions.push({ id: 'files', label: 'Open files', icon: 'folder-open', run: () => navigate(folderRoute(uploadPath)) })

  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="sx-scroll flex-1">
        <div className="mx-auto w-full max-w-6xl space-y-6 px-6 py-8">
          <header>
            <h1 className="text-2xl font-semibold tracking-tight text-ink">
              {data.greeting}
              {name ? <span>, {name}</span> : null}
            </h1>
            <p className="mt-1 text-sm text-muted">{today()}</p>
          </header>

          <div className="flex flex-wrap gap-2">
            {quickActions.map((action) => (
              <Button
                key={action.id}
                variant={action.primary ? 'primary' : 'secondary'}
                icon={action.icon}
                onClick={action.run}
              >
                {action.label}
              </Button>
            ))}
            <input
              ref={picker}
              type="file"
              multiple
              className="hidden"
              aria-hidden="true"
              tabIndex={-1}
              onChange={(event) => {
                const files = fromInput(event.target.files)
                event.target.value = ''
                if (files.length === 0) return
                enqueue(files, uploadPath)
                setTransfersOpen(true)
                toast.info('Upload started', `${counted(files.length, 'file')} added to the queue`)
              }}
            />
          </div>

          <StorageCard data={data} onOpen={(mount) => navigate(folderRoute(mount.path))} />

          <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
            <StatCard
              icon="activity"
              tone="primary"
              label="Transfers"
              value={activeTransfers > 0 ? counted(activeTransfers, 'upload') : 'None active'}
              hint={
                data.transfers.bytes > 0 ? `${bytes(data.transfers.bytes)} still to send` : 'Uploads you start show here'
              }
              onClick={() => navigate('/transfers')}
            />
            <StatCard
              icon="link"
              tone="accent"
              label="Shares"
              value={data.shares.active > 0 ? counted(data.shares.active, 'active link') : 'None active'}
              hint={data.shares.active > 0 ? 'Public links you own' : 'Share a folder to create one'}
              onClick={() => navigate('/shares')}
            />
            <StatCard
              icon="trash"
              tone="warning"
              label="Recycle bin"
              value={data.trash.count > 0 ? bytes(data.trash.bytes) : 'Empty'}
              hint={data.trash.count > 0 ? counted(data.trash.count, 'item') : 'Nothing waiting to be removed'}
              onClick={() => navigate('/trash')}
            />
            <StatCard
              icon="shield"
              tone="success"
              label="Storix"
              value={data.version || 'Unknown'}
              chip={data.updateAvailable ? 'Update available' : undefined}
              hint={data.updateAvailable ? 'Open settings to install it' : 'You are on the latest build'}
              onClick={() => navigate('/settings')}
            />
          </div>

          <section className="sx-panel p-5">
            <SectionTitle
              action={
                recent.length > 0 ? (
                  <Button variant="ghost" iconRight="chevron-right" onClick={() => navigate('/recent')}>
                    See all
                  </Button>
                ) : undefined
              }
            >
              Recent files
            </SectionTitle>
            {recent.length === 0 ? (
              <EmptyState
                icon="clock"
                title="Nothing here yet"
                message="Files you open, edit or download appear here so you can pick up where you left off."
                action={
                  <Button icon="folder-open" onClick={() => navigate(folderRoute(uploadPath))}>
                    Browse files
                  </Button>
                }
              />
            ) : (
              <div className="-mx-1">
                {recent.map((item) => (
                  <RecentRow
                    key={`${item.path}-${item.at}`}
                    item={item}
                    onOpen={(row) => navigate(row.isDir ? folderRoute(row.path) : selectRoute(row.path, row.name))}
                  />
                ))}
              </div>
            )}
          </section>

          {can('upload') && (
            <section>
              <SectionTitle
                action={
                  <span className="flex min-w-0 items-center gap-1.5 text-xs text-faint">
                    <Icon name="arrow-right" size={13} className="shrink-0" />
                    <span className="truncate font-mono">{truncateMiddle(uploadPath, 40)}</span>
                  </span>
                }
              >
                Quick upload
              </SectionTitle>
              <UploadZone path={uploadPath} />
            </section>
          )}
        </div>
      </div>
    </div>
  )
}
