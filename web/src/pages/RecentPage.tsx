// Recent: the files this account touched, newest first, grouped by day.
// Developed by X Project.

import clsx from 'clsx'
import { useMemo, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { api } from '../lib/api'
import type { Kind, Recent } from '../lib/types'
import { ago, bytes, extensionOf, parentPath, truncateMiddle } from '../lib/format'
import { useSession } from '../lib/session'
import { Icon, colourForKind, iconForKind } from '../components/Icon'
import { Button, EmptyState, IconButton, Menu, Skeleton, useToast, type MenuItem } from '../components/ui'

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

/** folderRoute builds the browser route for a folder, keeping odd names intact. */
function folderRoute(path: string): string {
  const clean = path.replace(/\/+$/, '')
  if (!clean) return '/files'
  return '/files' + clean.split('/').map(encodeURIComponent).join('/')
}

/** verbOf turns the stored action into something a person would say. */
function verbOf(action: string): string {
  switch (action) {
    case 'open':
      return 'Opened'
    case 'edit':
      return 'Edited'
    case 'download':
      return 'Downloaded'
    case 'upload':
      return 'Uploaded'
    case 'share':
      return 'Shared'
    default:
      return action ? action.charAt(0).toUpperCase() + action.slice(1) : 'Used'
  }
}

/** dayKey buckets a timestamp by calendar day. */
function dayKey(input: string): string {
  const date = new Date(input)
  if (Number.isNaN(date.getTime())) return 'unknown'
  return `${date.getFullYear()}-${date.getMonth()}-${date.getDate()}`
}

/** dayLabel names a bucket the way a person reads a calendar. */
function dayLabel(input: string): string {
  const date = new Date(input)
  if (Number.isNaN(date.getTime())) return 'Earlier'
  const now = new Date()
  const key = dayKey(input)
  if (key === dayKey(now.toISOString())) return 'Today'
  const yesterday = new Date(now)
  yesterday.setDate(now.getDate() - 1)
  if (key === dayKey(yesterday.toISOString())) return 'Yesterday'
  const sameYear = date.getFullYear() === now.getFullYear()
  return date.toLocaleDateString(undefined, {
    weekday: 'long',
    day: 'numeric',
    month: 'long',
    year: sameYear ? undefined : 'numeric',
  })
}

interface Group {
  key: string
  label: string
  items: Recent[]
}

/** download hands the file to the browser without leaving the page. */
function download(item: Recent): void {
  const url = item.isDir ? api.zipURL([item.path], item.name) : api.downloadURL(item.path)
  const link = document.createElement('a')
  link.href = url
  link.rel = 'noopener'
  document.body.appendChild(link)
  link.click()
  link.remove()
}

function RowSkeleton() {
  return (
    <div className="flex items-center gap-3 px-3 py-2.5">
      <Skeleton className="h-8 w-8 rounded-xl" />
      <div className="flex-1 space-y-1.5">
        <Skeleton className="h-3.5 w-52" />
        <Skeleton className="h-3 w-72" />
      </div>
      <Skeleton className="h-3 w-16" />
    </div>
  )
}

export default function RecentPage() {
  const navigate = useNavigate()
  const toast = useToast()
  const { can } = useSession()
  const [hidden, setHidden] = useState<string[]>([])
  const [menu, setMenu] = useState<{ x: number; y: number; item: Recent } | null>(null)

  const { data, isPending, isError, error, refetch, isFetching } = useQuery({
    queryKey: ['recent', 100],
    queryFn: () => api.recent(100),
  })

  const groups = useMemo<Group[]>(() => {
    const rows = (data?.recent ?? []).filter((item) => !hidden.includes(item.path))
    const out: Group[] = []
    for (const item of rows) {
      const key = dayKey(item.at)
      const last = out[out.length - 1]
      if (last && last.key === key) last.items.push(item)
      else out.push({ key, label: dayLabel(item.at), items: [item] })
    }
    return out
  }, [data, hidden])

  const openItem = (item: Recent) => {
    if (item.isDir) {
      navigate(folderRoute(item.path))
      return
    }
    navigate(`${folderRoute(parentPath(item.path) || '/')}?select=${encodeURIComponent(item.name)}`)
  }

  const openFolder = (item: Recent) => {
    navigate(folderRoute(item.isDir ? item.path : parentPath(item.path) || '/'))
  }

  const remove = (item: Recent) => {
    setHidden((current) => [...current, item.path])
    toast.push({
      tone: 'info',
      title: 'Removed from the list',
      message: 'The file itself was not touched.',
      action: { label: 'Undo', run: () => setHidden((current) => current.filter((path) => path !== item.path)) },
    })
  }

  const menuItems = (item: Recent): MenuItem[] => {
    const items: MenuItem[] = [
      { id: 'open', label: item.isDir ? 'Open folder' : 'Open location', icon: 'folder-open', onSelect: () => openFolder(item) },
    ]
    if (can('download')) items.push({ id: 'download', label: 'Download', icon: 'download', onSelect: () => download(item) })
    items.push({ id: 'divider', label: '', divider: true })
    items.push({ id: 'remove', label: 'Remove from list', icon: 'close', onSelect: () => remove(item) })
    return items
  }

  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="sx-scroll flex-1">
        <div className="mx-auto w-full max-w-4xl px-6 py-8">
          <header className="mb-6 flex items-end justify-between gap-4">
            <div>
              <h1 className="text-2xl font-semibold tracking-tight text-ink">Recent</h1>
              <p className="mt-1 text-sm text-muted">Everything you opened, edited or downloaded, newest first.</p>
            </div>
            <Button
              variant="ghost"
              icon="refresh"
              onClick={() => void refetch()}
              loading={isFetching && !isPending}
              aria-label="Refresh the list"
            >
              Refresh
            </Button>
          </header>

          {isPending ? (
            <div className="sx-panel p-2">
              {[0, 1, 2, 3, 4, 5].map((index) => (
                <RowSkeleton key={index} />
              ))}
            </div>
          ) : isError ? (
            <div className="sx-panel p-8 text-center">
              <span className="mx-auto mb-4 flex h-12 w-12 items-center justify-center rounded-2xl bg-danger/15 text-danger">
                <Icon name="alert" size={22} />
              </span>
              <h2 className="text-[15px] font-medium text-ink">The list could not load</h2>
              <p className="mx-auto mt-1.5 max-w-sm text-sm text-muted">
                {error instanceof Error && error.message ? error.message : 'The server did not answer.'}
              </p>
              <div className="mt-5 flex justify-center">
                <Button variant="primary" icon="refresh" onClick={() => void refetch()} loading={isFetching}>
                  Try again
                </Button>
              </div>
            </div>
          ) : groups.length === 0 ? (
            <div className="sx-panel">
              <EmptyState
                icon="clock"
                title="No recent files"
                message="Files appear here once you open, edit or download them, so you can find your way back quickly."
                action={
                  <Button icon="folder-open" onClick={() => navigate('/files')}>
                    Browse files
                  </Button>
                }
              />
            </div>
          ) : (
            <div className="space-y-6">
              {groups.map((group) => (
                <section key={group.key}>
                  <h2 className="sticky top-0 z-10 -mx-2 mb-1 bg-bg/95 px-2 py-2 text-[11px] font-semibold uppercase tracking-[0.14em] text-faint backdrop-blur">
                    {group.label}
                  </h2>
                  <div className="sx-panel p-2">
                    {group.items.map((item) => {
                      const kind = kindOf(item.name, item.isDir)
                      const folder = parentPath(item.path) || '/'
                      return (
                        <div
                          key={`${item.path}-${item.at}`}
                          className="sx-row"
                          onContextMenu={(event) => {
                            event.preventDefault()
                            setMenu({ x: event.clientX, y: event.clientY, item })
                          }}
                        >
                          <button
                            type="button"
                            onClick={() => openItem(item)}
                            onDoubleClick={() => openItem(item)}
                            className="flex min-w-0 flex-1 items-center gap-3 text-left"
                            title={item.path}
                          >
                            <Icon
                              name={iconForKind(kind, item.isDir)}
                              size={18}
                              className={clsx('shrink-0', colourForKind(kind, item.isDir))}
                            />
                            <span className="min-w-0 flex-1">
                              <span className="block truncate text-sm text-ink">{item.name}</span>
                              <span className="block truncate text-xs text-faint">{truncateMiddle(folder, 58)}</span>
                            </span>
                          </button>
                          <span className="hidden shrink-0 text-xs text-muted sm:block">{verbOf(item.action)}</span>
                          {!item.isDir && item.size > 0 && (
                            <span className="hidden shrink-0 text-xs text-faint md:block">{bytes(item.size)}</span>
                          )}
                          <span className="shrink-0 text-xs text-muted">{ago(item.at)}</span>
                          <IconButton
                            icon="more"
                            size={16}
                            label={`Actions for ${item.name}`}
                            onClick={(event) => setMenu({ x: event.clientX, y: event.clientY, item })}
                          />
                        </div>
                      )
                    })}
                  </div>
                </section>
              ))}
            </div>
          )}
        </div>
      </div>

      {menu && (
        <Menu items={menuItems(menu.item)} x={menu.x} y={menu.y} anchorRight onClose={() => setMenu(null)} />
      )}
    </div>
  )
}
