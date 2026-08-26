// Links: every public address this account has handed out.
// Developed by X Project.

import clsx from 'clsx'
import { useEffect, useMemo, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '../lib/api'
import { ago, counted, dateTime, truncateMiddle } from '../lib/format'
import { useSession } from '../lib/session'
import { useApp } from '../state/app'
import type { Share } from '../lib/types'
import { Icon, colourForKind, iconForKind } from '../components/Icon'
import {
  Button,
  ConfirmDialog,
  EmptyState,
  IconButton,
  Skeleton,
  Toggle,
  useToast,
} from '../components/ui'
import { ShareDialog, copyText, shareLink } from '../components/ShareDialog'
import { PathPickerDialog } from '../components/dialogs'

const DAY = 86_400_000

interface Target {
  path: string
  isDir: boolean
}

export default function SharesPage() {
  const toast = useToast()
  const queryClient = useQueryClient()
  const { isAdmin } = useSession()
  const lastPath = useApp((state) => state.lastPath)
  const [params, setParams] = useSearchParams()

  const [all, setAll] = useState(false)
  const [picking, setPicking] = useState(false)
  const [target, setTarget] = useState<Target | null>(null)
  const [editing, setEditing] = useState<Share | null>(null)
  const [revoking, setRevoking] = useState<Share | null>(null)

  const shares = useQuery({
    queryKey: ['shares', all ? 'all' : 'mine'],
    queryFn: () => api.shares(all),
  })

  // A link can be requested straight from another screen.
  useEffect(() => {
    if (params.get('create') !== '1') return
    const path = params.get('path')
    if (path) setTarget({ path, isDir: params.get('dir') !== '0' })
    else setPicking(true)
    const next = new URLSearchParams(params)
    next.delete('create')
    next.delete('path')
    next.delete('dir')
    setParams(next, { replace: true })
  }, [params, setParams])

  const revoke = useMutation({
    mutationFn: (share: Share) => api.deleteShare(share.id),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['shares'] })
      void queryClient.invalidateQueries({ queryKey: ['dashboard'] })
      setRevoking(null)
      toast.success('Link revoked', 'The address no longer works')
    },
    onError: (error: unknown) => {
      setRevoking(null)
      toast.error('Could not revoke the link', error instanceof Error ? error.message : undefined)
    },
  })

  const rows = useMemo(() => shares.data?.shares ?? [], [shares.data])

  const copy = async (share: Share) => {
    const ok = await copyText(shareLink(share))
    if (ok) toast.success('Link copied', 'The address is on your clipboard')
    else toast.error('Could not copy', 'Select the address and copy it by hand')
  }

  const open = (share: Share) => window.open(shareLink(share), '_blank', 'noopener')

  const startEdit = (share: Share) => {
    setEditing(share)
    setTarget({ path: share.path, isDir: share.isDir })
  }

  const closeDialog = () => {
    setTarget(null)
    setEditing(null)
  }

  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="sx-scroll flex-1">
        <div className="mx-auto w-full max-w-6xl px-6 py-8">
        <header className="mb-6 flex flex-wrap items-end justify-between gap-3">
          <div>
            <h1 className="text-2xl font-semibold tracking-tight text-ink">Links</h1>
            <p className="mt-1 text-sm text-muted">
              Public addresses for files and folders. Anyone with a link can use it without an account.
            </p>
          </div>
          <div className="flex items-center gap-2">
            {isAdmin && (
              <div className="mr-1 hidden sm:block">
                <Toggle checked={all} onChange={setAll} label="All users" />
              </div>
            )}
            <IconButton
              icon="refresh"
              label="Refresh"
              onClick={() => void shares.refetch()}
              disabled={shares.isFetching}
            />
            <Button variant="primary" icon="plus" onClick={() => setPicking(true)}>
              Create link
            </Button>
          </div>
        </header>

        {isAdmin && (
          <div className="mb-4 sm:hidden">
            <Toggle checked={all} onChange={setAll} label="All users" />
          </div>
        )}

        {shares.isLoading ? (
          <LoadingRows />
        ) : shares.isError ? (
          <div className="sx-panel">
            <EmptyState
              icon="alert"
              title="The links could not be loaded"
              message={shares.error instanceof Error ? shares.error.message : 'The server did not answer.'}
              action={
                <Button icon="refresh" onClick={() => void shares.refetch()}>
                  Try again
                </Button>
              }
            />
          </div>
        ) : rows.length === 0 ? (
          <div className="sx-panel">
            <EmptyState
              icon="link"
              title="No links yet"
              message="A link lets someone outside Storix download a file, or send files to you, without an account. You decide when it expires and whether it needs a password."
              action={
                <Button variant="primary" icon="plus" onClick={() => setPicking(true)}>
                  Create link
                </Button>
              }
            />
          </div>
        ) : (
          <>
            <div className="sx-panel hidden overflow-hidden md:block">
              <div className="overflow-x-auto">
                <table className="w-full min-w-[900px] text-sm">
                  <thead>
                    <tr className="border-b border-line text-left text-[11px] uppercase tracking-[0.12em] text-faint">
                      <th className="px-4 py-2.5 font-semibold">Shared item</th>
                      <th className="px-4 py-2.5 font-semibold">Kind</th>
                      {all && <th className="px-4 py-2.5 font-semibold">Owner</th>}
                      <th className="px-4 py-2.5 font-semibold">Address</th>
                      <th className="px-4 py-2.5 font-semibold">Activity</th>
                      <th className="px-4 py-2.5 font-semibold">Expires</th>
                      <th className="px-4 py-2.5 text-right font-semibold">Actions</th>
                    </tr>
                  </thead>
                  <tbody>
                    {rows.map((share) => (
                      <tr key={share.id} className="border-b border-line/60 last:border-0 hover:bg-elevated/70">
                        <td className="max-w-[280px] px-4 py-3">
                          <TargetCell share={share} />
                        </td>
                        <td className="px-4 py-3">
                          <div className="flex flex-wrap items-center gap-1.5">
                            <KindChip share={share} />
                            {share.hasPassword && <LockChip />}
                          </div>
                        </td>
                        {all && (
                          <td className="px-4 py-3 text-muted">{share.ownerName || `User ${share.ownerId}`}</td>
                        )}
                        <td className="max-w-[240px] px-4 py-3">
                          <AddressCell share={share} onCopy={() => void copy(share)} />
                        </td>
                        <td className="px-4 py-3">
                          <ActivityCell share={share} />
                        </td>
                        <td className="px-4 py-3">
                          <ExpiryCell share={share} />
                        </td>
                        <td className="px-4 py-3">
                          <div className="flex items-center justify-end gap-0.5">
                            <Actions
                              share={share}
                              onCopy={() => void copy(share)}
                              onOpen={() => open(share)}
                              onEdit={() => startEdit(share)}
                              onRevoke={() => setRevoking(share)}
                            />
                          </div>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </div>

            <div className="space-y-3 md:hidden">
              {rows.map((share) => (
                <div key={share.id} className="sx-panel space-y-3 p-4">
                  <TargetCell share={share} />
                  <div className="flex flex-wrap items-center gap-1.5">
                    <KindChip share={share} />
                    {share.hasPassword && <LockChip />}
                    {all && <span className="sx-chip">{share.ownerName || `User ${share.ownerId}`}</span>}
                  </div>
                  <AddressCell share={share} onCopy={() => void copy(share)} />
                  <div className="flex flex-wrap items-center justify-between gap-2">
                    <ActivityCell share={share} />
                    <ExpiryCell share={share} />
                  </div>
                  <div className="flex items-center gap-0.5 border-t border-line pt-2">
                    <Actions
                      share={share}
                      onCopy={() => void copy(share)}
                      onOpen={() => open(share)}
                      onEdit={() => startEdit(share)}
                      onRevoke={() => setRevoking(share)}
                    />
                  </div>
                </div>
              ))}
            </div>

            <p className="mt-4 text-xs text-faint">{counted(rows.length, 'link')}</p>
          </>
        )}
        </div>
      </div>

      {picking && (
        <PathPickerDialog
          open={picking}
          title="Choose what to share"
          confirmLabel="Continue"
          initialPath={lastPath || '/'}
          onClose={() => setPicking(false)}
          onPick={(path: string) => {
            setPicking(false)
            setEditing(null)
            setTarget({ path, isDir: true })
          }}
        />
      )}

      <ShareDialog
        open={target !== null}
        path={target?.path ?? ''}
        isDir={target?.isDir ?? true}
        share={editing}
        onClose={closeDialog}
        onCreated={() => void queryClient.invalidateQueries({ queryKey: ['shares'] })}
        onUpdated={closeDialog}
      />

      <ConfirmDialog
        open={revoking !== null}
        danger
        busy={revoke.isPending}
        title="Revoke this link"
        confirmLabel="Revoke link"
        message={
          revoking ? (
            <>
              <p>
                The address for <span className="text-ink">{revoking.name}</span> stops working immediately. Anyone who
                already has it will see that it is no longer available.
              </p>
              <p className="mt-2">The file itself is not touched, and you can create a new link at any time.</p>
            </>
          ) : (
            ''
          )
        }
        onCancel={() => setRevoking(null)}
        onConfirm={() => revoking && revoke.mutate(revoking)}
      />
    </div>
  )
}

// ---- cells -------------------------------------------------------------------

function TargetCell({ share }: { share: Share }) {
  return (
    <div className="flex min-w-0 items-center gap-3">
      <span className={clsx('shrink-0', colourForKind('other', share.isDir))}>
        <Icon name={iconForKind('other', share.isDir)} size={19} />
      </span>
      <div className="min-w-0">
        <div className="truncate font-medium text-ink">{share.name}</div>
        <div className="truncate font-mono text-[11px] text-faint" title={share.path}>
          {truncateMiddle(share.path, 44)}
        </div>
      </div>
    </div>
  )
}

function KindChip({ share }: { share: Share }) {
  const upload = share.kind === 'upload'
  return (
    <span className={clsx('sx-chip', upload ? 'text-accent' : 'text-primary')}>
      <Icon name={upload ? 'cloud-upload' : 'download'} size={13} />
      {upload ? 'Upload request' : 'Download'}
    </span>
  )
}

function LockChip() {
  return (
    <span className="sx-chip">
      <Icon name="lock" size={13} />
      Password
    </span>
  )
}

function AddressCell({ share, onCopy }: { share: Share; onCopy: () => void }) {
  const url = shareLink(share)
  return (
    <div className="flex min-w-0 items-center gap-1">
      <span className="min-w-0 flex-1 truncate font-mono text-xs text-muted" title={url}>
        {url.replace(/^https?:\/\//, '')}
      </span>
      <IconButton icon="copy" label="Copy link" size={15} onClick={onCopy} className="h-7 w-7 shrink-0" />
    </div>
  )
}

function ActivityCell({ share }: { share: Share }) {
  const limit = share.maxDownloads > 0 ? ` of ${share.maxDownloads}` : ''
  return (
    <div className="min-w-0">
      <div className="text-ink">
        {share.downloads.toLocaleString()}
        {limit} <span className="text-muted">{share.downloads === 1 && !limit ? 'download' : 'downloads'}</span>
      </div>
      <div className="text-[11px] text-faint">
        {share.lastAccessAt ? `Opened ${ago(share.lastAccessAt)}` : 'Not opened yet'}
      </div>
    </div>
  )
}

function ExpiryCell({ share }: { share: Share }) {
  if (!share.expiresAt) {
    return <span className="text-muted">No end date</span>
  }
  const at = new Date(share.expiresAt).getTime()
  const left = at - Date.now()
  if (left <= 0) {
    return (
      <span className="sx-chip border-danger/40 bg-danger/10 text-danger">
        <Icon name="alert" size={13} />
        Expired
      </span>
    )
  }
  if (left < DAY) {
    return (
      <span className="sx-chip border-warning/40 bg-warning/10 text-warning" title={dateTime(share.expiresAt)}>
        <Icon name="clock" size={13} />
        Expires {ago(share.expiresAt)}
      </span>
    )
  }
  return (
    <span className="text-muted" title={dateTime(share.expiresAt)}>
      Expires {ago(share.expiresAt)}
    </span>
  )
}

function Actions({
  share,
  onCopy,
  onOpen,
  onEdit,
  onRevoke,
}: {
  share: Share
  onCopy: () => void
  onOpen: () => void
  onEdit: () => void
  onRevoke: () => void
}) {
  return (
    <>
      <IconButton icon="copy" label={`Copy the link for ${share.name}`} size={16} onClick={onCopy} />
      <IconButton icon="external" label={`Open the link for ${share.name}`} size={16} onClick={onOpen} />
      <IconButton icon="edit" label={`Edit the link for ${share.name}`} size={16} onClick={onEdit} />
      <IconButton icon="trash" label={`Revoke the link for ${share.name}`} size={16} tone="danger" onClick={onRevoke} />
    </>
  )
}

function LoadingRows() {
  return (
    <div className="sx-panel divide-y divide-line p-1.5">
      {Array.from({ length: 5 }).map((_, index) => (
        <div key={index} className="flex items-center gap-3 px-3 py-3.5">
          <Skeleton className="h-8 w-8 rounded-xl" />
          <div className="min-w-0 flex-1 space-y-2">
            <Skeleton className="h-3.5 w-40" />
            <Skeleton className="h-3 w-64" />
          </div>
          <Skeleton className="hidden h-3.5 w-24 sm:block" />
          <Skeleton className="hidden h-3.5 w-20 md:block" />
          <Skeleton className="h-8 w-8 rounded-xl" />
        </div>
      ))}
    </div>
  )
}
