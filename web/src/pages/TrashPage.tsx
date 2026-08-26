// The recycle bin: what was deleted, how long it stays and how to get it back.
// Developed by X Project.

import clsx from 'clsx'
import { useMemo, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '../lib/api'
import type { Kind, TrashItem } from '../lib/types'
import { bytes, counted, extensionOf, parentPath, smartDate, truncateMiddle } from '../lib/format'
import { useSession } from '../lib/session'
import { Icon, colourForKind, iconForKind } from '../components/Icon'
import { Button, Checkbox, ConfirmDialog, EmptyState, IconButton, Skeleton, Toggle, useToast } from '../components/ui'

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

/** timeLeft says how long an item has before the server clears it out. */
function timeLeft(expiresAt: string): { label: string; urgent: boolean } {
  const date = new Date(expiresAt)
  if (Number.isNaN(date.getTime()) || date.getTime() === 0) return { label: 'Kept until emptied', urgent: false }
  const remaining = date.getTime() - Date.now()
  if (remaining <= 0) return { label: 'Any moment now', urgent: true }
  const days = Math.floor(remaining / 86400000)
  if (days >= 1) return { label: `${counted(days, 'day')} left`, urgent: days <= 2 }
  const hours = Math.floor(remaining / 3600000)
  if (hours >= 1) return { label: `${counted(hours, 'hour')} left`, urgent: true }
  const minutes = Math.max(1, Math.floor(remaining / 60000))
  return { label: `${counted(minutes, 'minute')} left`, urgent: true }
}

type Pending = 'restore' | 'delete' | 'empty' | null

interface Failure {
  name: string
  reason: string
}

function TableSkeleton() {
  return (
    <div className="sx-panel divide-y divide-line">
      {[0, 1, 2, 3, 4].map((index) => (
        <div key={index} className="flex items-center gap-4 px-4 py-3">
          <Skeleton className="h-4 w-4 rounded" />
          <Skeleton className="h-4 w-48 flex-1" />
          <Skeleton className="hidden h-3 w-40 md:block" />
          <Skeleton className="h-3 w-16" />
          <Skeleton className="h-3 w-24" />
        </div>
      ))}
    </div>
  )
}

export default function TrashPage() {
  const navigate = useNavigate()
  const toast = useToast()
  const client = useQueryClient()
  const { user, isAdmin, can } = useSession()

  const [selected, setSelected] = useState<number[]>([])
  const [allUsers, setAllUsers] = useState(false)
  const [pending, setPending] = useState<Pending>(null)
  const [failures, setFailures] = useState<Failure[]>([])

  const { data, isPending, isError, error, refetch, isFetching } = useQuery({
    queryKey: ['trash'],
    queryFn: () => api.trash(),
  })

  const items = useMemo<TrashItem[]>(() => data?.items ?? [], [data])
  const mixedOwners = useMemo(() => new Set(items.map((item) => item.userId)).size > 1, [items])
  const showOwnerToggle = isAdmin && mixedOwners

  // Names for the owner column, only needed once an administrator looks at
  // everyone's items.
  const accounts = useQuery({
    queryKey: ['users'],
    queryFn: () => api.users(),
    enabled: showOwnerToggle && allUsers,
  })
  const ownerName = (id: number): string => {
    const match = accounts.data?.users.find((account) => account.id === id)
    return match ? match.displayName || match.username : `Account ${id}`
  }

  const visible = useMemo<TrashItem[]>(() => {
    if (!showOwnerToggle || allUsers) return items
    return items.filter((item) => item.userId === user?.id)
  }, [items, showOwnerToggle, allUsers, user])

  const filtered = visible.length !== items.length
  const count = filtered ? visible.length : (data?.count ?? items.length)
  const size = filtered ? visible.reduce((sum, item) => sum + item.size, 0) : (data?.bytes ?? 0)
  const retentionDays = data?.retentionDays ?? 0

  const selectedItems = useMemo(
    () => visible.filter((item) => selected.includes(item.id)),
    [visible, selected],
  )
  const allSelected = visible.length > 0 && selectedItems.length === visible.length
  const someSelected = selectedItems.length > 0 && !allSelected

  const refresh = () => {
    setSelected([])
    setPending(null)
    void client.invalidateQueries({ queryKey: ['trash'] })
    void client.invalidateQueries({ queryKey: ['dashboard'] })
    void client.invalidateQueries({ queryKey: ['list'] })
  }

  const done = (message: string) => {
    refresh()
    toast.success(message)
  }

  const failed = (title: string) => (mutationError: unknown) => {
    setPending(null)
    toast.error(title, mutationError instanceof Error ? mutationError.message : undefined)
  }

  const restore = useMutation({
    mutationFn: (ids: number[]) => api.trashRestore(ids),
    onSuccess: (result, ids) => {
      const names = new Map(items.map((item) => [item.id, item.name]))
      const list = (result.failed ?? []).map((entry) => ({
        name: names.get(entry.id) ?? `Item ${entry.id}`,
        reason: entry.reason || 'The server did not say why',
      }))
      setFailures(list)
      const restored = result.restored ?? 0
      if (list.length === 0) {
        done(`${counted(restored, 'item')} put back`)
        return
      }
      refresh()
      toast.error(
        restored > 0 ? `${counted(restored, 'item')} put back` : 'Nothing was put back',
        `${counted(list.length, 'item')} of ${ids.length} could not be restored`,
      )
    },
    onError: failed('The items could not be restored'),
  })

  const erase = useMutation({
    mutationFn: (ids: number[]) => api.trashDelete(ids),
    onSuccess: (_result, ids) => done(`${counted(ids.length, 'item')} deleted for good`),
    onError: failed('The items could not be deleted'),
  })

  const empty = useMutation({
    mutationFn: () => api.trashEmpty(showOwnerToggle && allUsers),
    onSuccess: () => done('The bin is empty'),
    onError: failed('The bin could not be emptied'),
  })

  const busy = restore.isPending || erase.isPending || empty.isPending
  const mayManage = can('delete')

  const toggleAll = (value: boolean) => setSelected(value ? visible.map((item) => item.id) : [])
  const toggleOne = (id: number, value: boolean) =>
    setSelected((current) => (value ? [...current, id] : current.filter((entry) => entry !== id)))

  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="sx-scroll flex-1">
        <div className="mx-auto w-full max-w-5xl px-6 py-8">
          <header className="mb-6 flex flex-wrap items-end justify-between gap-4">
            <div className="min-w-0">
              <h1 className="text-2xl font-semibold tracking-tight text-ink">Recycle bin</h1>
              <p className="mt-1 text-sm text-muted">
                {isPending
                  ? 'Counting what is in the bin'
                  : count > 0
                    ? `${counted(count, 'item')}, ${bytes(size)} in total`
                    : 'Nothing is waiting here'}
              </p>
              {retentionDays > 0 && (
                <p className="mt-1 text-xs text-faint">
                  Items are removed automatically after {counted(retentionDays, 'day')}.
                </p>
              )}
            </div>
            <div className="flex items-center gap-2">
              {showOwnerToggle && (
                <div className="mr-2">
                  <Toggle checked={allUsers} onChange={setAllUsers} label="All users" />
                </div>
              )}
              <Button
                variant="ghost"
                icon="refresh"
                onClick={() => void refetch()}
                loading={isFetching && !isPending}
                aria-label="Refresh the bin"
              >
                Refresh
              </Button>
              {visible.length > 0 && mayManage && (
                <Button variant="danger" icon="trash" onClick={() => setPending('empty')} disabled={busy}>
                  Empty bin
                </Button>
              )}
            </div>
          </header>

          {failures.length > 0 && (
            <div className="sx-panel mb-4 border-warning/40 p-4">
              <div className="flex items-start gap-3">
                <span className="mt-0.5 flex h-8 w-8 shrink-0 items-center justify-center rounded-xl bg-warning/15 text-warning">
                  <Icon name="alert" size={17} />
                </span>
                <div className="min-w-0 flex-1">
                  <h2 className="text-sm font-medium text-ink">
                    {counted(failures.length, 'item')} could not be restored
                  </h2>
                  <ul className="mt-2 space-y-2">
                    {failures.map((failure) => (
                      <li key={failure.name}>
                        <p className="truncate text-xs text-ink">{failure.name}</p>
                        <p className="text-xs text-faint">{failure.reason}</p>
                      </li>
                    ))}
                  </ul>
                  <p className="mt-3 text-xs text-faint">
                    The items that failed are still in the bin, so you can try again.
                  </p>
                </div>
                <IconButton icon="close" size={15} label="Dismiss this notice" onClick={() => setFailures([])} />
              </div>
            </div>
          )}

          {selectedItems.length > 0 && (
            <div className="sx-panel mb-4 flex flex-wrap items-center gap-3 px-4 py-3">
              <span className="text-sm text-ink">{counted(selectedItems.length, 'item')} selected</span>
              <span className="flex-1" />
              <Button icon="restore" onClick={() => setPending('restore')} disabled={!mayManage || busy}>
                Restore
              </Button>
              <Button variant="danger" icon="trash" onClick={() => setPending('delete')} disabled={!mayManage || busy}>
                Delete permanently
              </Button>
              <Button variant="ghost" onClick={() => setSelected([])} disabled={busy}>
                Clear
              </Button>
            </div>
          )}

          {isPending ? (
            <TableSkeleton />
          ) : isError ? (
            <div className="sx-panel p-8 text-center">
              <span className="mx-auto mb-4 flex h-12 w-12 items-center justify-center rounded-2xl bg-danger/15 text-danger">
                <Icon name="alert" size={22} />
              </span>
              <h2 className="text-[15px] font-medium text-ink">The bin could not load</h2>
              <p className="mx-auto mt-1.5 max-w-sm text-sm text-muted">
                {error instanceof Error && error.message ? error.message : 'The server did not answer.'}
              </p>
              <div className="mt-5 flex justify-center">
                <Button variant="primary" icon="refresh" onClick={() => void refetch()} loading={isFetching}>
                  Try again
                </Button>
              </div>
            </div>
          ) : visible.length === 0 ? (
            <div className="sx-panel">
              {items.length > 0 ? (
                <EmptyState
                  icon="trash"
                  title="Nothing of yours is in the bin"
                  message="Other accounts have deleted items waiting here. Turn on All users to see them."
                  action={<Button icon="users" onClick={() => setAllUsers(true)}>Show all users</Button>}
                />
              ) : (
                <EmptyState
                  icon="trash"
                  title="The bin is empty"
                  message="Files you delete land here first, so a mistake is never final. From here you can put them back or remove them for good."
                  action={
                    <Button icon="folder-open" onClick={() => navigate('/files')}>
                      Browse files
                    </Button>
                  }
                />
              )}
            </div>
          ) : (
            <div className="sx-panel overflow-hidden">
              <div className="overflow-x-auto">
                <table className="w-full min-w-[46rem] border-collapse text-sm">
                  <thead>
                    <tr className="border-b border-line text-left text-xs text-faint">
                      <th scope="col" className="w-10 py-2.5 pl-4">
                        <Checkbox
                          checked={allSelected}
                          indeterminate={someSelected}
                          onChange={toggleAll}
                          label={<span className="sr-only">Select every item in the bin</span>}
                        />
                      </th>
                      <th scope="col" className="py-2.5 pr-4 font-medium">
                        Name
                      </th>
                      <th scope="col" className="py-2.5 pr-4 font-medium">
                        Was in
                      </th>
                      {showOwnerToggle && allUsers && (
                        <th scope="col" className="py-2.5 pr-4 font-medium">
                          Owner
                        </th>
                      )}
                      <th scope="col" className="py-2.5 pr-4 font-medium">
                        Size
                      </th>
                      <th scope="col" className="py-2.5 pr-4 font-medium">
                        Deleted
                      </th>
                      <th scope="col" className="py-2.5 pr-4 font-medium">
                        Removed in
                      </th>
                    </tr>
                  </thead>
                  <tbody>
                    {visible.map((item) => {
                      const kind = kindOf(item.name, item.isDir)
                      const checked = selected.includes(item.id)
                      const remaining = timeLeft(item.expiresAt)
                      return (
                        <tr
                          key={item.id}
                          data-selected={checked ? 'true' : undefined}
                          className={clsx(
                            'border-b border-line/60 transition-colors last:border-0',
                            checked ? 'bg-primary/10 hover:bg-primary/15' : 'hover:bg-elevated',
                          )}
                        >
                          <td className="py-2.5 pl-4 align-middle">
                            <Checkbox
                              checked={checked}
                              onChange={(value) => toggleOne(item.id, value)}
                              label={<span className="sr-only">Select {item.name}</span>}
                            />
                          </td>
                          <td className="py-2.5 pr-4 align-middle">
                            <span className="flex min-w-0 items-center gap-2.5">
                              <Icon
                                name={iconForKind(kind, item.isDir)}
                                size={17}
                                className={clsx('shrink-0', colourForKind(kind, item.isDir))}
                              />
                              <span className="truncate text-ink">{item.name}</span>
                            </span>
                          </td>
                          <td className="py-2.5 pr-4 align-middle text-xs text-muted" title={item.originalPath}>
                            {truncateMiddle(parentPath(item.originalPath) || '/', 34)}
                          </td>
                          {showOwnerToggle && allUsers && (
                            <td className="py-2.5 pr-4 align-middle text-xs text-muted">{ownerName(item.userId)}</td>
                          )}
                          <td className="py-2.5 pr-4 align-middle text-xs text-muted">
                            {item.isDir ? 'Folder' : bytes(item.size)}
                          </td>
                          <td className="py-2.5 pr-4 align-middle text-xs text-muted">{smartDate(item.deletedAt)}</td>
                          <td className="py-2.5 pr-4 align-middle text-xs">
                            <span className={remaining.urgent ? 'text-warning' : 'text-muted'}>{remaining.label}</span>
                          </td>
                        </tr>
                      )
                    })}
                  </tbody>
                </table>
              </div>
            </div>
          )}

          {!mayManage && visible.length > 0 && (
            <p className="mt-3 text-xs text-faint">
              Your account can look inside the bin but cannot restore or remove items.
            </p>
          )}
        </div>
      </div>

      <ConfirmDialog
        open={pending === 'restore'}
        title="Put these items back"
        message={
          <>
            {counted(selectedItems.length, 'item')} will return to the folder it came from. If a file with the same
            name is there now, the restored copy gets a number added to its name.
          </>
        }
        confirmLabel="Restore"
        busy={restore.isPending}
        onCancel={() => setPending(null)}
        onConfirm={() => restore.mutate(selectedItems.map((item) => item.id))}
      />

      <ConfirmDialog
        open={pending === 'delete'}
        danger
        title="Delete permanently"
        message={
          <>
            {counted(selectedItems.length, 'item')} will be erased from the server. This cannot be undone.
          </>
        }
        confirmLabel="Delete permanently"
        busy={erase.isPending}
        onCancel={() => setPending(null)}
        onConfirm={() => erase.mutate(selectedItems.map((item) => item.id))}
      />

      <ConfirmDialog
        open={pending === 'empty'}
        danger
        title="Empty the bin"
        message={
          <>
            Everything in the bin{showOwnerToggle && allUsers ? ', for every account,' : ''} will be erased from the
            server. This cannot be undone.
          </>
        }
        confirmLabel="Empty bin"
        busy={empty.isPending}
        onCancel={() => setPending(null)}
        onConfirm={() => empty.mutate()}
      />
    </div>
  )
}
