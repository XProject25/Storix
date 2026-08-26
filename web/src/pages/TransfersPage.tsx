// The full transfers view: uploads leaving this browser, operations running on
// the server and any upload that can still be resumed.
// Developed by X Project.

import clsx from 'clsx'
import { useEffect, useRef, useState } from 'react'
import { Link } from 'react-router-dom'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api, subscribe } from '../lib/api'
import { ago, bytes, counted, dateTime, duration, truncateMiddle } from '../lib/format'
import type { Job, JobStatus } from '../lib/types'
import { Icon, type IconName } from '../components/Icon'
import { Button, EmptyState, Progress, SectionTitle, Skeleton, useToast } from '../components/ui'
import { TransferControls, filesLink, folderLabel, stateWord } from '../components/TransferDock'
import { useApp } from '../state/app'
import { fromInput, useTransfers } from '../state/transfers'
import { useSession } from '../lib/session'

const JOB_LABELS: Record<string, { label: string; icon: IconName }> = {
  copy: { label: 'Copy', icon: 'copy' },
  move: { label: 'Move', icon: 'move' },
  delete: { label: 'Delete', icon: 'trash' },
  compress: { label: 'Compress', icon: 'archive' },
  extract: { label: 'Extract', icon: 'folder-open' },
  update: { label: 'Update', icon: 'download' },
}

const JOB_CHIPS: Record<JobStatus, { label: string; className: string; icon: IconName }> = {
  queued: { label: 'Waiting', className: 'text-muted', icon: 'clock' },
  running: { label: 'Running', className: 'border-primary/40 bg-primary/10 text-primary', icon: 'activity' },
  done: { label: 'Finished', className: 'border-success/40 bg-success/10 text-success', icon: 'check-circle' },
  failed: { label: 'Failed', className: 'border-danger/40 bg-danger/10 text-danger', icon: 'alert' },
  canceled: { label: 'Canceled', className: 'text-faint', icon: 'close' },
}

/** useClock re-renders once a second, but only while something is running. */
function useClock(active: boolean): number {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    if (!active) return
    setNow(Date.now())
    const id = window.setInterval(() => setNow(Date.now()), 1000)
    return () => window.clearInterval(id)
  }, [active])
  return now
}

function jobElapsed(job: Job, now: number): string {
  const startedAt = Date.parse(job.startedAt ?? job.createdAt)
  if (Number.isNaN(startedAt)) return ''
  const finishedAt = job.finishedAt ? Date.parse(job.finishedAt) : Number.NaN
  const end = Number.isNaN(finishedAt) ? now : finishedAt
  return duration(Math.max(0, (end - startedAt) / 1000))
}

function jobPercent(job: Job): number {
  if (job.status === 'done') return 100
  if (job.total > 0) return (job.done / job.total) * 100
  if (job.totalItems > 0) return (job.doneItems / job.totalItems) * 100
  return 0
}

function StatusChip({ status }: { status: JobStatus }) {
  const chip = JOB_CHIPS[status]
  return (
    <span className={clsx('sx-chip', chip.className)}>
      <Icon name={chip.icon} size={12} />
      {chip.label}
    </span>
  )
}

function TableHead({ columns }: { columns: string[] }) {
  return (
    <thead>
      <tr>
        {columns.map((column, index) => (
          <th
            key={column || `column-${index}`}
            scope="col"
            className={clsx(
              'px-3 py-2.5 text-[11px] font-semibold uppercase tracking-[0.14em] text-faint',
              index === columns.length - 1 ? 'text-right' : 'text-left',
            )}
          >
            {column}
          </th>
        ))}
      </tr>
    </thead>
  )
}

function LoadingRows({ columns }: { columns: number }) {
  return (
    <tbody>
      {[0, 1, 2].map((row) => (
        <tr key={row} className="border-t border-line">
          {Array.from({ length: columns }).map((_, cell) => (
            <td key={cell} className="px-3 py-3.5">
              <Skeleton className="h-4 w-full" />
            </td>
          ))}
        </tr>
      ))}
    </tbody>
  )
}

function LoadFailed({ message, onRetry }: { message: string; onRetry: () => void }) {
  return (
    <div className="flex flex-col items-center gap-3 px-6 py-10 text-center">
      <span className="flex h-11 w-11 items-center justify-center rounded-2xl bg-danger/12 text-danger">
        <Icon name="alert" size={20} />
      </span>
      <div>
        <p className="text-sm font-medium text-ink">Could not load this list</p>
        <p className="mt-1 text-sm text-muted">{message}</p>
      </div>
      <Button icon="refresh" onClick={onRetry}>
        Try again
      </Button>
    </div>
  )
}

export default function TransfersPage() {
  const items = useTransfers((state) => state.items)
  const enqueue = useTransfers((state) => state.enqueue)
  const clearFinished = useTransfers((state) => state.clearFinished)
  const lastPath = useApp((state) => state.lastPath)
  const setTransfersOpen = useApp((state) => state.setTransfersOpen)
  const { me } = useSession()
  const toast = useToast()
  const queryClient = useQueryClient()
  const fileInput = useRef<HTMLInputElement>(null)

  const destination = lastPath || me?.mounts[0]?.path || '/'
  const finished = items.filter((item) => item.status === 'done').length

  const jobs = useQuery({
    queryKey: ['jobs'],
    queryFn: () => api.jobs(50),
    // The event stream drives updates; this is only a safety net while work runs.
    refetchInterval: (query) =>
      (query.state.data?.jobs ?? []).some((job) => job.status === 'running' || job.status === 'queued')
        ? 5000
        : false,
  })

  const uploads = useQuery({ queryKey: ['uploads'], queryFn: () => api.uploads() })

  useEffect(() => {
    const stop = subscribe((type) => {
      if (type.startsWith('job.')) void queryClient.invalidateQueries({ queryKey: ['jobs'] })
      if (type === 'upload.done') void queryClient.invalidateQueries({ queryKey: ['uploads'] })
    })
    return stop
  }, [queryClient])

  const cancelJob = useMutation({
    mutationFn: (id: string) => api.cancelJob(id),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['jobs'] })
      toast.success('Operation canceled')
    },
    onError: (error: unknown) =>
      toast.error('Could not cancel the operation', error instanceof Error ? error.message : undefined),
  })

  const jobList = jobs.data?.jobs ?? []
  const running = jobList.some((job) => job.status === 'running' || job.status === 'queued')
  const now = useClock(running)
  const uploadList = uploads.data?.uploads ?? []

  const openPicker = () => fileInput.current?.click()

  return (
    <div className="sx-scroll h-full">
      <div className="mx-auto w-full max-w-6xl px-5 py-6 md:px-8">
        <header className="mb-6 flex flex-wrap items-start justify-between gap-3">
          <div>
            <h1 className="text-xl font-semibold text-ink">Transfers</h1>
            <p className="mt-1 text-sm text-muted">
              Uploads from this browser and the work running on the server.
            </p>
          </div>
          <Button variant="primary" icon="upload" onClick={openPicker}>
            Upload files
          </Button>
        </header>

        <input
          ref={fileInput}
          type="file"
          multiple
          className="hidden"
          onChange={(event) => {
            const picked = fromInput(event.target.files)
            event.target.value = ''
            if (picked.length === 0) return
            enqueue(picked, destination)
            setTransfersOpen(true)
          }}
        />

        {/* ---- uploads from this browser ------------------------------- */}
        <section className="mb-8">
          <SectionTitle
            action={
              finished > 0 ? (
                <Button variant="ghost" className="h-7 px-2 text-xs" onClick={clearFinished}>
                  Clear finished
                </Button>
              ) : undefined
            }
          >
            Upload queue
          </SectionTitle>

          <div className="sx-panel overflow-hidden">
            {items.length === 0 ? (
              <EmptyState
                icon="cloud-upload"
                title="No transfers yet"
                message="Files you upload appear here with their progress, speed and time left."
                action={
                  <Button variant="primary" icon="upload" onClick={openPicker}>
                    Upload files
                  </Button>
                }
              />
            ) : (
              <div className="overflow-x-auto">
                <table className="w-full min-w-[720px] border-collapse text-sm">
                  <TableHead columns={['File', 'Destination', 'Progress', 'Speed', 'Time left', 'Actions']} />
                  <tbody>
                    {items.map((transfer) => {
                      const value = transfer.size > 0 ? (transfer.uploaded / transfer.size) * 100 : 0
                      return (
                        <tr key={transfer.id} className="border-t border-line align-top">
                          <td className="px-3 py-3.5">
                            <div className="flex items-center gap-2">
                              {transfer.status === 'done' && (
                                <span className="shrink-0 text-success">
                                  <Icon name="check-circle" size={15} />
                                </span>
                              )}
                              {transfer.status === 'error' && (
                                <span className="shrink-0 text-danger">
                                  <Icon name="alert" size={15} />
                                </span>
                              )}
                              <span className="truncate text-ink" title={transfer.name}>
                                {truncateMiddle(transfer.name, 44)}
                              </span>
                            </div>
                            {transfer.status === 'error' && (
                              <p className="mt-1 text-xs text-danger">
                                {transfer.error || 'The upload did not finish'}
                              </p>
                            )}
                            {transfer.relativePath && (
                              <p className="mt-1 text-xs text-faint" title={transfer.relativePath}>
                                {truncateMiddle(transfer.relativePath, 44)}
                              </p>
                            )}
                          </td>
                          <td className="px-3 py-3.5 text-xs">
                            <Link
                              to={filesLink(transfer.dir)}
                              className="text-primary underline-offset-2 hover:underline"
                              title={transfer.dir}
                            >
                              {folderLabel(transfer.dir)}
                            </Link>
                          </td>
                          <td className="w-[240px] px-3 py-3.5">
                            <Progress value={value} />
                            <div className="mt-1.5 text-[11px] tabular-nums text-faint">
                              {bytes(transfer.uploaded)} of {bytes(transfer.size)}
                            </div>
                          </td>
                          <td className="px-3 py-3.5 text-xs tabular-nums text-muted">{stateWord(transfer)}</td>
                          <td className="px-3 py-3.5 text-xs tabular-nums text-muted">
                            {transfer.status === 'uploading' && transfer.eta > 0 ? duration(transfer.eta) : '--:--'}
                          </td>
                          <td className="px-3 py-3.5">
                            <div className="flex justify-end">
                              <TransferControls transfer={transfer} />
                            </div>
                          </td>
                        </tr>
                      )
                    })}
                  </tbody>
                </table>
              </div>
            )}
          </div>
        </section>

        {/* ---- server side operations ----------------------------------- */}
        <section className="mb-8">
          <SectionTitle
            action={
              <Button
                variant="ghost"
                className="h-7 px-2 text-xs"
                icon="refresh"
                onClick={() => void jobs.refetch()}
                loading={jobs.isFetching && !jobs.isPending}
              >
                Refresh
              </Button>
            }
          >
            Server operations
          </SectionTitle>

          <div className="sx-panel overflow-hidden">
            {jobs.isPending ? (
              <div className="overflow-x-auto">
                <table className="w-full min-w-[720px] border-collapse text-sm">
                  <TableHead columns={['Operation', 'Progress', 'Elapsed', 'Status', 'Actions']} />
                  <LoadingRows columns={5} />
                </table>
              </div>
            ) : jobs.isError ? (
              <LoadFailed
                message={jobs.error instanceof Error ? jobs.error.message : 'The server did not answer'}
                onRetry={() => void jobs.refetch()}
              />
            ) : jobList.length === 0 ? (
              <EmptyState
                icon="activity"
                title="Nothing is running"
                message="Copying, moving, compressing and extracting happen on the server, so you can keep working while they finish. They show up here."
              />
            ) : (
              <div className="overflow-x-auto">
                <table className="w-full min-w-[720px] border-collapse text-sm">
                  <TableHead columns={['Operation', 'Progress', 'Elapsed', 'Status', 'Actions']} />
                  <tbody>
                    {jobList.map((job) => {
                      const meta = JOB_LABELS[job.type] ?? { label: job.type, icon: 'activity' as IconName }
                      const value = jobPercent(job)
                      const canCancel = job.cancellable && (job.status === 'queued' || job.status === 'running')
                      return (
                        <tr key={job.id} className="border-t border-line align-top">
                          <td className="px-3 py-3.5">
                            <div className="flex items-center gap-2.5">
                              <span className="flex h-7 w-7 shrink-0 items-center justify-center rounded-lg bg-elevated text-muted">
                                <Icon name={meta.icon} size={14} />
                              </span>
                              <div className="min-w-0">
                                <div className="truncate text-ink" title={job.title || meta.label}>
                                  {job.title || meta.label}
                                </div>
                                <div className="mt-0.5 text-xs text-faint">{meta.label}</div>
                              </div>
                            </div>
                            {job.status === 'failed' && job.error && (
                              <p className="mt-1.5 text-xs text-danger">{job.error}</p>
                            )}
                            {job.status !== 'failed' && job.message && (
                              <p className="mt-1.5 truncate text-xs text-muted" title={job.message}>
                                {truncateMiddle(job.message, 60)}
                              </p>
                            )}
                          </td>
                          <td className="w-[240px] px-3 py-3.5">
                            <Progress value={value} />
                            <div className="mt-1.5 text-[11px] tabular-nums text-faint">
                              {job.total > 0
                                ? `${bytes(job.done)} of ${bytes(job.total)}`
                                : job.totalItems > 0
                                  ? `${job.doneItems.toLocaleString()} of ${counted(job.totalItems, 'item')}`
                                  : `${Math.round(value)}%`}
                            </div>
                          </td>
                          <td className="px-3 py-3.5 text-xs tabular-nums text-muted">{jobElapsed(job, now)}</td>
                          <td className="px-3 py-3.5">
                            <StatusChip status={job.status} />
                          </td>
                          <td className="px-3 py-3.5">
                            <div className="flex justify-end">
                              {canCancel ? (
                                <Button
                                  variant="ghost"
                                  className="h-7 px-2 text-xs"
                                  onClick={() => cancelJob.mutate(job.id)}
                                  loading={cancelJob.isPending && cancelJob.variables === job.id}
                                >
                                  Cancel
                                </Button>
                              ) : (
                                <span className="text-xs text-faint">{ago(job.finishedAt ?? job.updatedAt)}</span>
                              )}
                            </div>
                          </td>
                        </tr>
                      )
                    })}
                  </tbody>
                </table>
              </div>
            )}
          </div>
        </section>

        {/* ---- resumable uploads ---------------------------------------- */}
        <section className="mb-4">
          <SectionTitle>Resumable uploads</SectionTitle>
          <div className="sx-panel overflow-hidden">
            <p className="border-b border-line px-4 py-3 text-sm text-muted">
              An interrupted upload is kept on the server for a while. Add the same file again and it continues from
              where it stopped instead of starting over.
            </p>

            {uploads.isPending ? (
              <div className="space-y-3 p-4">
                <Skeleton className="h-10 w-full" />
                <Skeleton className="h-10 w-full" />
              </div>
            ) : uploads.isError ? (
              <LoadFailed
                message={uploads.error instanceof Error ? uploads.error.message : 'The server did not answer'}
                onRetry={() => void uploads.refetch()}
              />
            ) : uploadList.length === 0 ? (
              <EmptyState
                icon="clock"
                title="No interrupted uploads"
                message="Everything you have sent so far arrived in one piece."
              />
            ) : (
              <ul className="divide-y divide-line">
                {uploadList.map((record) => {
                  const value = record.size > 0 ? (record.offset / record.size) * 100 : 0
                  return (
                    <li key={record.id} className="flex flex-wrap items-center gap-4 px-4 py-3.5">
                      <div className="min-w-0 flex-1">
                        <div className="truncate text-sm text-ink" title={record.name}>
                          {truncateMiddle(record.name, 48)}
                        </div>
                        <div className="mt-0.5 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-faint">
                          <Link
                            to={filesLink(record.dir)}
                            className="text-primary underline-offset-2 hover:underline"
                            title={record.dir}
                          >
                            {folderLabel(record.dir)}
                          </Link>
                          <span title={dateTime(record.createdAt)}>Started {ago(record.createdAt)}</span>
                          <span title={dateTime(record.expiresAt)}>Kept until {dateTime(record.expiresAt)}</span>
                        </div>
                      </div>
                      <div className="w-full max-w-[240px]">
                        <Progress value={value} />
                        <div className="mt-1.5 text-[11px] tabular-nums text-faint">
                          {bytes(record.offset)} of {bytes(record.size)} received
                        </div>
                      </div>
                    </li>
                  )
                })}
              </ul>
            )}

            {uploadList.length > 0 && (
              <div className="border-t border-line px-4 py-2.5 text-xs text-faint">
                {counted(uploadList.length, 'upload')} can be resumed, {bytes(uploads.data?.bytes ?? 0)} already on the
                server
              </div>
            )}
          </div>
        </section>
      </div>
    </div>
  )
}
