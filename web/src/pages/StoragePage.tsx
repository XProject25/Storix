// Storage: the answer to "what is taking up space in this folder", one folder
// at a time, plus the allowance this account is working within.
// Developed by X Project.

import clsx from 'clsx'
import { useEffect, useMemo, useState } from 'react'
import { Link, useNavigate, useSearchParams } from 'react-router-dom'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ApiError, api } from '../lib/api'
import type { Crumb, Kind, Mount, UsageNode } from '../lib/types'
import { baseName, bytes, counted, parentPath, percent as percentText, truncateMiddle } from '../lib/format'
import { useSession } from '../lib/session'
import { Breadcrumbs } from '../components/Breadcrumbs'
import DuplicateFinder from '../components/DuplicateFinder'
import { Icon, colourForKind, iconForKind } from '../components/Icon'
import { UsageBar, QuotaCard, colourForIndex, type UsageSegment } from '../components/UsageBars'
import {
  Button,
  ConfirmDialog,
  EmptyState,
  IconButton,
  SectionTitle,
  Skeleton,
  useToast,
} from '../components/ui'

// How many children get their own slice before the rest are folded together.
const BAR_SLICES = 8

// The identifier of the folded slice. A real path always starts with a slash.
const OTHER_SLICE = 'storix:other'

const KIND_LABELS: Record<Kind, string> = {
  folder: 'Folders',
  image: 'Images',
  video: 'Video',
  audio: 'Audio',
  pdf: 'PDF files',
  archive: 'Archives',
  code: 'Code',
  text: 'Text',
  document: 'Documents',
  disk: 'Disk images',
  font: 'Fonts',
  binary: 'Programs',
  other: 'Everything else',
}

/** storageReason turns a failed request into one sentence a person can act on. */
function storageReason(error: unknown): string {
  if (error instanceof ApiError) return error.message
  if (error instanceof Error) return error.message
  return 'The server did not answer.'
}

/** storageFolderRoute builds the browser route for a folder, keeping odd names intact. */
function storageFolderRoute(path: string): string {
  const clean = path.replace(/\/+$/, '')
  if (!clean) return '/files'
  return '/files' + clean.split('/').map(encodeURIComponent).join('/')
}

/** storageSelectRoute opens the folder that holds a file, with the file selected. */
function storageSelectRoute(path: string, name: string): string {
  return `${storageFolderRoute(parentPath(path) || '/')}?select=${encodeURIComponent(name)}`
}

/** storageCrumbs builds the trail from the mount that holds a path down to it. */
function storageCrumbs(path: string, mounts: Mount[]): Crumb[] {
  const target = path.replace(/\/+$/, '') || '/'
  let root = ''
  let label = ''
  for (const mount of mounts) {
    const clean = mount.path.replace(/\/+$/, '')
    const inside = clean === '' || target === clean || target.startsWith(clean + '/')
    if (!inside) continue
    if (clean.length >= root.length || !label) {
      root = clean
      label = mount.label || baseName(mount.path) || mount.path
    }
  }

  const crumbs: Crumb[] = [{ name: label || root || '/', path: root || '/' }]
  const rest = target.slice(root.length).replace(/^\/+/, '')
  if (rest) {
    let walk = root
    for (const part of rest.split('/')) {
      walk = walk ? `${walk}/${part}` : `/${part}`
      crumbs.push({ name: part, path: walk })
    }
  }
  return crumbs
}

/** storageScanTime renders how long the server spent walking the tree. */
function storageScanTime(elapsedMs: number): string {
  if (!Number.isFinite(elapsedMs) || elapsedMs <= 0) return 'no time at all'
  if (elapsedMs < 1000) return `${Math.round(elapsedMs)} ms`
  return `${(elapsedMs / 1000).toFixed(1)} s`
}

// ---- pieces -----------------------------------------------------------------

function ReportSkeleton({ seconds }: { seconds: number }) {
  return (
    <div className="space-y-6">
      <div className="sx-panel p-5">
        <Skeleton className="h-7 w-44" />
        <Skeleton className="mt-3 h-4 w-64" />
        {seconds >= 1 && (
          <p className="mt-4 flex items-center gap-2 text-xs text-faint">
            <Icon name="search" size={13} className="shrink-0" />
            Still counting, {counted(seconds, 'second')} so far. Large folders take a moment.
          </p>
        )}
      </div>
      <div className="sx-panel p-5">
        <Skeleton className="h-4 w-32" />
        <Skeleton className="mt-4 h-4 w-full rounded-full" />
        <div className="mt-5 space-y-2.5">
          {[0, 1, 2, 3, 4].map((index) => (
            <Skeleton key={index} className="h-4 w-full" />
          ))}
        </div>
      </div>
      <div className="grid gap-6 lg:grid-cols-2">
        <Skeleton className="h-72 rounded-2xl" />
        <Skeleton className="h-72 rounded-2xl" />
      </div>
    </div>
  )
}

function ReportError({ message, onRetry, busy }: { message: string; onRetry: () => void; busy: boolean }) {
  return (
    <div className="sx-panel p-8 text-center">
      <span className="mx-auto mb-4 flex h-12 w-12 items-center justify-center rounded-2xl bg-danger/15 text-danger">
        <Icon name="alert" size={22} />
      </span>
      <h2 className="text-[15px] font-medium text-ink">This folder could not be measured</h2>
      <p className="mx-auto mt-1.5 max-w-sm text-sm text-muted">{message}</p>
      <div className="mt-5 flex justify-center">
        <Button variant="primary" icon="refresh" onClick={onRetry} loading={busy}>
          Try again
        </Button>
      </div>
    </div>
  )
}

// ---- page -------------------------------------------------------------------

export default function StoragePage() {
  const navigate = useNavigate()
  const toast = useToast()
  const queryClient = useQueryClient()
  const { me, can } = useSession()
  const [params, setParams] = useSearchParams()
  const [selected, setSelected] = useState<UsageNode | null>(null)
  const [confirmDelete, setConfirmDelete] = useState<UsageNode | null>(null)
  const [seconds, setSeconds] = useState(0)

  const mounts = useMemo(() => me?.mounts ?? [], [me])
  const requested = params.get('path') ?? ''
  const path = requested || mounts[0]?.path || ''

  const usage = useQuery({
    queryKey: ['usage', path],
    queryFn: () => api.usage(path, 40),
    enabled: path !== '',
  })

  const quota = useQuery({
    queryKey: ['quota'],
    queryFn: () => api.quota(),
    staleTime: 60_000,
  })

  const removeMutation = useMutation({
    mutationFn: (target: string) => api.remove([target], false),
    onSuccess: (_result, target) => {
      toast.success('Moved to trash', `${baseName(target)} can be restored from the recycle bin.`)
      setSelected(null)
      void queryClient.invalidateQueries({ queryKey: ['usage'] })
      void queryClient.invalidateQueries({ queryKey: ['quota'] })
      void queryClient.invalidateQueries({ queryKey: ['trash'] })
      void queryClient.invalidateQueries({ queryKey: ['list'] })
    },
    onError: (error) => toast.error('Nothing was deleted', storageReason(error)),
  })

  // A slow scan should say so rather than sit behind a blank panel.
  const scanning = usage.isFetching
  useEffect(() => {
    if (!scanning) {
      setSeconds(0)
      return
    }
    const started = Date.now()
    const timer = window.setInterval(() => setSeconds(Math.floor((Date.now() - started) / 1000)), 500)
    return () => window.clearInterval(timer)
  }, [scanning, path])

  const goTo = (next: string) => {
    setSelected(null)
    setParams(next ? { path: next } : {}, { replace: false })
  }

  const report = usage.data
  const children = useMemo(() => report?.children ?? [], [report])

  const segments = useMemo<UsageSegment[]>(() => {
    if (!report || report.bytes <= 0) return []
    const shown = children.slice(0, BAR_SLICES)
    const list: UsageSegment[] = shown.map((child, index) => ({
      id: child.path,
      label: child.name,
      bytes: child.bytes,
      percent: child.percent,
      colour: colourForIndex(index),
      isDir: child.isDir,
    }))
    const known = shown.reduce((sum, child) => sum + child.bytes, 0)
    const rest = report.bytes - known
    if (rest > 0) {
      list.push({
        id: OTHER_SLICE,
        label: 'Everything else',
        bytes: rest,
        percent: (rest / report.bytes) * 100,
        colour: 'rgb(var(--sx-faint) / 0.45)',
        isDir: false,
      })
    }
    return list
  }, [report, children])

  const pick = (id: string) => {
    if (id === OTHER_SLICE) return
    const node = children.find((child) => child.path === id)
    if (!node) return
    if (node.isDir) {
      goTo(node.path)
      return
    }
    setSelected(node)
  }

  const crumbs = storageCrumbs(path, mounts)

  const header = (
    <header className="mb-6 flex flex-wrap items-end justify-between gap-4">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight text-ink">Storage</h1>
        <p className="mt-1 text-sm text-muted">See what is using space, one folder at a time.</p>
      </div>
      <div className="flex items-center gap-2">
        {path && (
          <Button icon="folder-open" onClick={() => navigate(storageFolderRoute(path))}>
            Open in files
          </Button>
        )}
        <Button
          variant="ghost"
          icon="refresh"
          onClick={() => {
            void usage.refetch()
            void quota.refetch()
          }}
          loading={usage.isFetching && !usage.isPending}
        >
          Rescan
        </Button>
      </div>
    </header>
  )

  if (mounts.length === 0) {
    return (
      <div className="sx-scroll h-full">
        <div className="mx-auto w-full max-w-6xl px-6 py-8">
          {header}
          <div className="sx-panel">
            <EmptyState
              icon="drive"
              title="No folders are connected yet"
              message={
                can('settings')
                  ? 'Add a folder in Settings and its usage appears here.'
                  : 'Once a folder is shared with you, its usage appears here.'
              }
              action={
                can('settings') ? (
                  <Button icon="settings" onClick={() => navigate('/settings')}>
                    Open settings
                  </Button>
                ) : undefined
              }
            />
          </div>
        </div>
      </div>
    )
  }

  const empty = report ? report.bytes <= 0 && children.length === 0 : false

  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="sx-scroll flex-1">
        <div className="mx-auto w-full max-w-6xl px-6 py-8">
          {header}

          <section className="sx-panel mb-6 p-4">
            <div className="flex flex-wrap items-center gap-2">
              {mounts.map((mount) => {
                const active = crumbs[0]?.path === mount.path.replace(/\/+$/, '') || crumbs[0]?.path === mount.path
                return (
                  <button
                    key={mount.path}
                    type="button"
                    onClick={() => goTo(mount.path)}
                    title={mount.path}
                    className={clsx(
                      'sx-chip transition-colors',
                      active ? 'border-primary/45 bg-primary/12 text-primary' : 'hover:text-ink',
                    )}
                  >
                    <Icon name="drive" size={13} className="shrink-0" />
                    <span className="max-w-[12rem] truncate">
                      {mount.label || baseName(mount.path) || mount.path}
                    </span>
                  </button>
                )
              })}
            </div>
            <div className="mt-3 flex items-center gap-1 border-t border-line pt-3">
              <IconButton
                icon="arrow-up"
                size={16}
                label="Measure the folder above"
                disabled={crumbs.length < 2}
                onClick={() => {
                  const up = crumbs[crumbs.length - 2]
                  if (up) goTo(up.path)
                }}
              />
              <Breadcrumbs crumbs={crumbs} onNavigate={goTo} className="min-w-0 flex-1" />
            </div>
          </section>

          {usage.isPending ? (
            <ReportSkeleton seconds={seconds} />
          ) : usage.isError || !report ? (
            <ReportError
              message={storageReason(usage.error)}
              onRetry={() => void usage.refetch()}
              busy={usage.isFetching}
            />
          ) : (
            <div className="space-y-6">
              <div className="grid gap-6 lg:grid-cols-3">
                <section className="sx-panel p-5 lg:col-span-2">
                  <SectionTitle>This folder</SectionTitle>
                  <p className="text-3xl font-semibold tracking-tight text-ink">{bytes(report.bytes)}</p>
                  <p className="mt-1.5 text-sm text-muted">
                    {counted(report.files, 'file')} in {counted(report.folders, 'folder')}
                  </p>
                  <p className="mt-3 flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-faint">
                    <Icon name="clock" size={13} className="shrink-0" />
                    <span>
                      Scanned {counted(report.scanned, 'entry', 'entries')} in {storageScanTime(report.elapsedMs)}
                    </span>
                    {usage.isFetching && <span className="text-primary">Rescanning now</span>}
                  </p>
                  {report.truncated && (
                    <p className="mt-3 flex items-start gap-2 rounded-xl border border-warning/35 bg-warning/10 px-3 py-2 text-xs text-warning">
                      <Icon name="info" size={14} className="mt-px shrink-0" />
                      This folder is larger than one scan covers, so the report stops short. The figures below are the
                      biggest items found before the count ended.
                    </p>
                  )}
                </section>

                {quota.data && quota.data.limit > 0 ? (
                  <QuotaCard quota={quota.data} />
                ) : (
                  <section className="sx-panel p-5">
                    <SectionTitle>Your allowance</SectionTitle>
                    <p className="text-sm text-muted">
                      This account has no storage limit, so the only ceiling is the space left on the volume.
                    </p>
                  </section>
                )}
              </div>

              {empty ? (
                <div className="sx-panel">
                  <EmptyState
                    icon="drive"
                    title="Nothing is using space here"
                    message="This folder is empty, so there is no breakdown to show yet."
                    action={
                      <Button icon="folder-open" onClick={() => navigate(storageFolderRoute(path))}>
                        Open in files
                      </Button>
                    }
                  />
                </div>
              ) : (
                <>
                  <section className="sx-panel p-5">
                    <SectionTitle>Biggest items</SectionTitle>
                    <div className="pt-7">
                      <UsageBar segments={segments} height={18} onSelect={pick} />
                    </div>

                    <div className="mt-5 space-y-0.5">
                      {segments.map((segment) => {
                        const isOther = segment.id === OTHER_SLICE
                        const row = (
                          <>
                            <span
                              className="h-2.5 w-2.5 shrink-0 rounded-sm"
                              style={{ background: segment.colour }}
                              aria-hidden="true"
                            />
                            <span className="min-w-0 flex-1 truncate text-sm text-ink">{segment.label}</span>
                            <span className="shrink-0 text-xs text-muted">{bytes(segment.bytes)}</span>
                            <span className="w-14 shrink-0 text-right text-xs text-faint">
                              {percentText(segment.percent, 1)}
                            </span>
                          </>
                        )
                        if (isOther) {
                          return (
                            <div
                              key={segment.id}
                              className="flex h-11 items-center gap-3 px-3"
                              title="Loose files in this folder and the items too small to show"
                            >
                              {row}
                            </div>
                          )
                        }
                        return (
                          <button
                            key={segment.id}
                            type="button"
                            onClick={() => pick(segment.id)}
                            className="sx-row w-full cursor-pointer text-left"
                            title={segment.isDir ? `Look inside ${segment.label}` : segment.label}
                          >
                            {row}
                            <Icon
                              name={segment.isDir ? 'chevron-right' : 'file'}
                              size={14}
                              className="shrink-0 text-faint"
                            />
                          </button>
                        )
                      })}
                    </div>

                    {selected && (
                      <div className="mt-4 flex flex-wrap items-center gap-3 rounded-xl border border-line bg-elevated/60 px-3 py-2.5">
                        <Icon
                          name={iconForKind(selected.kind, selected.isDir)}
                          size={18}
                          className={clsx('shrink-0', colourForKind(selected.kind, selected.isDir))}
                        />
                        <span className="min-w-0 flex-1">
                          <span className="block truncate text-sm text-ink">{selected.name}</span>
                          <span className="block truncate text-xs text-faint">
                            {bytes(selected.bytes)} in {truncateMiddle(parentPath(selected.path) || '/', 44)}
                          </span>
                        </span>
                        <Button
                          icon="folder-open"
                          onClick={() => navigate(storageSelectRoute(selected.path, selected.name))}
                        >
                          Open folder
                        </Button>
                        {can('delete') && (
                          <Button variant="danger" icon="trash" onClick={() => setConfirmDelete(selected)}>
                            Delete
                          </Button>
                        )}
                        <IconButton
                          icon="close"
                          size={15}
                          label="Clear the selection"
                          onClick={() => setSelected(null)}
                        />
                      </div>
                    )}
                  </section>

                  <div className="grid gap-6 lg:grid-cols-2">
                    <section className="sx-panel p-5">
                      <SectionTitle>Largest files</SectionTitle>
                      {report.largest.length === 0 ? (
                        <p className="px-3 py-6 text-center text-sm text-faint">
                          No files were found under this folder.
                        </p>
                      ) : (
                        <div className="-mx-1">
                          {report.largest.map((file) => (
                            <Link
                              key={file.path}
                              to={storageSelectRoute(file.path, file.name)}
                              title={file.path}
                              className="sx-row w-full cursor-pointer text-left"
                            >
                              <Icon
                                name={iconForKind(file.kind, false)}
                                size={18}
                                className={clsx('shrink-0', colourForKind(file.kind, false))}
                              />
                              <span className="min-w-0 flex-1">
                                <span className="block truncate text-sm text-ink">{file.name}</span>
                                <span className="block truncate text-xs text-faint">
                                  {truncateMiddle(parentPath(file.path) || '/', 46)}
                                </span>
                              </span>
                              <span className="shrink-0 text-xs text-muted">{bytes(file.bytes)}</span>
                            </Link>
                          ))}
                        </div>
                      )}
                    </section>

                    <section className="sx-panel p-5">
                      <SectionTitle>By type</SectionTitle>
                      {report.byKind.length === 0 ? (
                        <p className="px-3 py-6 text-center text-sm text-faint">
                          There is nothing to group by type yet.
                        </p>
                      ) : (
                        <div className="space-y-4">
                          {report.byKind.map((row, index) => (
                            <div key={row.kind}>
                              <div className="flex items-center gap-3">
                                <Icon
                                  name={iconForKind(row.kind, row.kind === 'folder')}
                                  size={16}
                                  className={clsx('shrink-0', colourForKind(row.kind, row.kind === 'folder'))}
                                />
                                <span className="min-w-0 flex-1 truncate text-sm text-ink">
                                  {KIND_LABELS[row.kind] ?? row.kind}
                                </span>
                                <span className="shrink-0 text-xs text-muted">{bytes(row.bytes)}</span>
                                <span className="w-14 shrink-0 text-right text-xs text-faint">
                                  {percentText(row.percent, 1)}
                                </span>
                              </div>
                              <div className="mt-2 h-1 w-full overflow-hidden rounded-full bg-line">
                                <span
                                  className="block h-full rounded-full"
                                  style={{
                                    width: `${Math.min(100, Math.max(0, row.percent))}%`,
                                    background: colourForIndex(index),
                                  }}
                                />
                              </div>
                            </div>
                          ))}
                        </div>
                      )}
                    </section>
                  </div>

                  <DuplicateFinder path={path} />
                </>
              )}
            </div>
          )}
        </div>
      </div>

      <ConfirmDialog
        open={confirmDelete !== null}
        danger
        title="Move to trash"
        confirmLabel="Move to trash"
        message={
          <>
            {confirmDelete?.name} goes to the recycle bin and stops using space here. You can put it back from Trash.
          </>
        }
        busy={removeMutation.isPending}
        onCancel={() => setConfirmDelete(null)}
        onConfirm={() => {
          const target = confirmDelete
          setConfirmDelete(null)
          if (target) removeMutation.mutate(target.path)
        }}
      />
    </div>
  )
}
