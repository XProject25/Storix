// Favorites: the folders and files pinned for quick reach.
// Developed by X Project.

import clsx from 'clsx'
import { useMemo } from 'react'
import { useNavigate } from 'react-router-dom'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '../lib/api'
import type { Favorite, Kind } from '../lib/types'
import { extensionOf, parentPath, truncateMiddle } from '../lib/format'
import { Icon, colourForKind, iconForKind } from '../components/Icon'
import { Button, EmptyState, IconButton, Skeleton, useToast } from '../components/ui'

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

function CardSkeleton() {
  return (
    <div className="sx-panel p-4">
      <Skeleton className="h-10 w-10 rounded-xl" />
      <Skeleton className="mt-3 h-3.5 w-28" />
      <Skeleton className="mt-2 h-3 w-40" />
    </div>
  )
}

export default function FavoritesPage() {
  const navigate = useNavigate()
  const toast = useToast()
  const client = useQueryClient()

  const { data, isPending, isError, error, refetch, isFetching } = useQuery({
    queryKey: ['favorites'],
    queryFn: () => api.favorites(),
  })

  const items = useMemo<Favorite[]>(() => {
    const list = [...(data?.favorites ?? [])]
    list.sort((a, b) => {
      if (a.isDir !== b.isDir) return a.isDir ? -1 : 1
      return a.name.localeCompare(b.name, undefined, { sensitivity: 'base' })
    })
    return list
  }, [data])

  const unpin = useMutation({
    mutationFn: (path: string) => api.removeFavorite(path),
    onSuccess: (_result, path) => {
      void client.invalidateQueries({ queryKey: ['favorites'] })
      void client.invalidateQueries({ queryKey: ['dashboard'] })
      toast.push({
        tone: 'info',
        title: 'Unpinned',
        message: truncateMiddle(path, 48),
        action: {
          label: 'Undo',
          run: () => {
            api
              .addFavorite(path)
              .then(() => {
                void client.invalidateQueries({ queryKey: ['favorites'] })
                void client.invalidateQueries({ queryKey: ['dashboard'] })
              })
              .catch(() => toast.error('That location could not be pinned again'))
          },
        },
      })
    },
    onError: (mutationError) =>
      toast.error(
        'The pin could not be removed',
        mutationError instanceof Error ? mutationError.message : undefined,
      ),
  })

  const open = (favorite: Favorite) => {
    if (favorite.isDir) {
      navigate(folderRoute(favorite.path))
      return
    }
    navigate(`${folderRoute(parentPath(favorite.path) || '/')}?select=${encodeURIComponent(favorite.name)}`)
  }

  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="sx-scroll flex-1">
        <div className="mx-auto w-full max-w-5xl px-6 py-8">
          <header className="mb-6 flex items-end justify-between gap-4">
            <div>
              <h1 className="text-2xl font-semibold tracking-tight text-ink">Favorites</h1>
              <p className="mt-1 text-sm text-muted">Pin any folder from the file browser to see it here.</p>
            </div>
            <Button
              variant="ghost"
              icon="refresh"
              onClick={() => void refetch()}
              loading={isFetching && !isPending}
              aria-label="Refresh favorites"
            >
              Refresh
            </Button>
          </header>

          {isPending ? (
            <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
              {[0, 1, 2, 3, 4, 5].map((index) => (
                <CardSkeleton key={index} />
              ))}
            </div>
          ) : isError ? (
            <div className="sx-panel p-8 text-center">
              <span className="mx-auto mb-4 flex h-12 w-12 items-center justify-center rounded-2xl bg-danger/15 text-danger">
                <Icon name="alert" size={22} />
              </span>
              <h2 className="text-[15px] font-medium text-ink">Favorites could not load</h2>
              <p className="mx-auto mt-1.5 max-w-sm text-sm text-muted">
                {error instanceof Error && error.message ? error.message : 'The server did not answer.'}
              </p>
              <div className="mt-5 flex justify-center">
                <Button variant="primary" icon="refresh" onClick={() => void refetch()} loading={isFetching}>
                  Try again
                </Button>
              </div>
            </div>
          ) : items.length === 0 ? (
            <div className="sx-panel">
              <EmptyState
                icon="star"
                title="Nothing pinned yet"
                message="Open a folder in the file browser and pin it. Pinned locations stay one click away from every screen."
                action={
                  <Button icon="folder-open" onClick={() => navigate('/files')}>
                    Browse files
                  </Button>
                }
              />
            </div>
          ) : (
            <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
              {items.map((favorite) => {
                const kind = kindOf(favorite.name, favorite.isDir)
                const folder = parentPath(favorite.path) || '/'
                const busy = unpin.isPending && unpin.variables === favorite.path
                return (
                  <div
                    key={favorite.id}
                    title={favorite.path}
                    className={clsx(
                      'sx-panel relative p-4 transition-colors hover:bg-elevated',
                      busy && 'opacity-60',
                    )}
                  >
                    <button
                      type="button"
                      onClick={() => open(favorite)}
                      className="absolute inset-0 rounded-2xl"
                      aria-label={`Open ${favorite.name}`}
                    />
                    <div className="pointer-events-none relative">
                      <span
                        className={clsx(
                          'flex h-10 w-10 items-center justify-center rounded-xl bg-elevated',
                          colourForKind(kind, favorite.isDir),
                        )}
                      >
                        <Icon name={iconForKind(kind, favorite.isDir)} size={20} />
                      </span>
                      <p className="mt-3 truncate pr-8 text-sm font-medium text-ink">{favorite.name}</p>
                      <p className="mt-1 truncate text-xs text-faint">{truncateMiddle(folder, 40)}</p>
                    </div>
                    <IconButton
                      icon="star-filled"
                      size={16}
                      label={`Unpin ${favorite.name}`}
                      disabled={busy}
                      onClick={() => unpin.mutate(favorite.path)}
                      className="absolute right-2 top-2 z-10 text-warning hover:text-warning"
                    />
                  </div>
                )
              })}
            </div>
          )}
        </div>
      </div>
    </div>
  )
}
