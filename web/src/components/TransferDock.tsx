// The floating upload dock. It stays out of the way but never lets a large
// transfer, a pause or a failure go unnoticed.
// Developed by X Project.

import clsx from 'clsx'
import { useCallback, useEffect, useMemo } from 'react'
import { Link } from 'react-router-dom'
import { Icon } from './Icon'
import { Button, IconButton, Progress } from './ui'
import { bytes, baseName, counted, duration, percent, rate, truncateMiddle } from '../lib/format'
import { useApp } from '../state/app'
import { useTransfers, type Transfer } from '../state/transfers'

/** filesLink points at the browser view for a folder. */
export function filesLink(dir: string): string {
  const clean = (dir.startsWith('/') ? dir : `/${dir}`).replace(/\/+$/, '')
  return `/files${clean}`
}

/** folderLabel names the destination folder in a way people recognise. */
export function folderLabel(dir: string): string {
  return baseName(dir) || dir || '/'
}

interface Totals {
  /** Transfers still waiting, running or paused. */
  remaining: number
  finished: number
  failed: number
  inFlight: number
  total: number
  done: number
  speed: number
  eta: number
  percent: number
}

function totalsOf(items: Transfer[]): Totals {
  const tracked = items.filter((item) => item.status !== 'canceled')
  const total = tracked.reduce((sum, item) => sum + item.size, 0)
  const done = tracked.reduce((sum, item) => sum + item.uploaded, 0)
  const speed = tracked.reduce((sum, item) => (item.status === 'uploading' ? sum + item.speed : sum), 0)
  const remaining = tracked.filter(
    (item) => item.status === 'queued' || item.status === 'uploading' || item.status === 'paused',
  ).length
  return {
    remaining,
    finished: tracked.filter((item) => item.status === 'done').length,
    failed: tracked.filter((item) => item.status === 'error').length,
    inFlight: tracked.filter((item) => item.status === 'queued' || item.status === 'uploading').length,
    total,
    done,
    speed,
    eta: speed > 0 ? Math.max(0, (total - done) / speed) : 0,
    percent: total > 0 ? (done / total) * 100 : tracked.length > 0 ? 100 : 0,
  }
}

/**
 * TransferControls renders the pause, resume, retry and cancel buttons that
 * match the state of one transfer. The dock and the transfers page share it.
 */
export function TransferControls({ transfer }: { transfer: Transfer }) {
  const pause = useTransfers((state) => state.pause)
  const resume = useTransfers((state) => state.resume)
  const retry = useTransfers((state) => state.retry)
  const cancel = useTransfers((state) => state.cancel)
  const name = truncateMiddle(transfer.name, 40)

  return (
    <div className="flex shrink-0 items-center gap-0.5">
      {transfer.status === 'uploading' && (
        <IconButton
          icon="pause"
          size={14}
          className="sx-touch h-7 w-7"
          label={`Pause ${name}`}
          onClick={() => pause(transfer.id)}
        />
      )}
      {transfer.status === 'paused' && (
        <IconButton
          icon="play"
          size={14}
          className="sx-touch h-7 w-7"
          label={`Resume ${name}`}
          onClick={() => resume(transfer.id)}
        />
      )}
      {transfer.status === 'error' && (
        <IconButton
          icon="refresh"
          size={14}
          className="sx-touch h-7 w-7"
          label={`Retry ${name}`}
          onClick={() => retry(transfer.id)}
        />
      )}
      {transfer.status !== 'done' && (
        <IconButton
          icon="close"
          size={14}
          tone="danger"
          className="sx-touch h-7 w-7"
          label={`Cancel ${name}`}
          onClick={() => cancel(transfer.id)}
        />
      )}
    </div>
  )
}

/** stateWord is the short status shown where a speed would otherwise sit. */
export function stateWord(transfer: Transfer): string {
  switch (transfer.status) {
    case 'queued':
      return 'Waiting'
    case 'paused':
      return 'Paused'
    case 'done':
      return 'Finished'
    case 'error':
      return 'Failed'
    default:
      return transfer.speed > 0 ? rate(transfer.speed) : 'Starting'
  }
}

function DockRow({ transfer }: { transfer: Transfer }) {
  const done = transfer.status === 'done'
  const failed = transfer.status === 'error'
  const value = transfer.size > 0 ? (transfer.uploaded / transfer.size) * 100 : 0

  return (
    <li className={clsx('px-3.5 py-2.5 transition-opacity duration-300', done && 'opacity-75')}>
      <div className="flex items-center gap-2">
        {done && (
          <span className="shrink-0 text-success">
            <Icon name="check-circle" size={15} />
          </span>
        )}
        {failed && (
          <span className="shrink-0 text-danger">
            <Icon name="alert" size={15} />
          </span>
        )}
        <span className="min-w-0 flex-1 truncate text-[13px] text-ink" title={transfer.name}>
          {truncateMiddle(transfer.name, 30)}
        </span>
        <TransferControls transfer={transfer} />
      </div>

      {done ? (
        <div className="mt-1 flex items-center gap-1.5 text-[11px] text-faint">
          <Icon name="folder" size={12} className="shrink-0" />
          <span>Saved to</span>
          <Link
            to={filesLink(transfer.dir)}
            className="min-w-0 truncate text-primary underline-offset-2 hover:underline"
            title={transfer.dir}
          >
            {folderLabel(transfer.dir)}
          </Link>
        </div>
      ) : failed ? (
        <p className="mt-1 text-[11px] leading-relaxed text-danger">
          {transfer.error || 'The upload did not finish'}
        </p>
      ) : (
        <>
          <Progress value={value} className="mt-2" />
          <div className="mt-1.5 flex items-center justify-between gap-3 text-[11px] text-faint">
            <span className="tabular-nums">
              {bytes(transfer.uploaded)} of {bytes(transfer.size)}
            </span>
            <span className="flex shrink-0 items-center gap-2 tabular-nums">
              <span>{stateWord(transfer)}</span>
              {transfer.status === 'uploading' && transfer.eta > 0 && <span>{duration(transfer.eta)} left</span>}
            </span>
          </div>
        </>
      )}
    </li>
  )
}

export default function TransferDock() {
  const items = useTransfers((state) => state.items)
  const cancel = useTransfers((state) => state.cancel)
  const clearFinished = useTransfers((state) => state.clearFinished)
  const open = useApp((state) => state.transfersOpen)
  const setOpen = useApp((state) => state.setTransfersOpen)

  const totals = useMemo(() => totalsOf(items), [items])
  const busy = totals.inFlight > 0

  // Leaving the page mid transfer loses the connection, so warn first.
  useEffect(() => {
    if (!busy) return
    const warn = (event: BeforeUnloadEvent) => {
      event.preventDefault()
      event.returnValue = ''
    }
    window.addEventListener('beforeunload', warn)
    return () => window.removeEventListener('beforeunload', warn)
  }, [busy])

  const dismiss = useCallback(() => {
    for (const item of items) cancel(item.id)
  }, [items, cancel])

  if (items.length === 0) return null

  const title =
    totals.remaining > 0
      ? `Uploading ${counted(totals.remaining, 'file')}`
      : totals.failed > 0
        ? `${counted(totals.failed, 'upload')} failed`
        : `${counted(totals.finished, 'upload')} finished`

  return (
    <section
      aria-label="Transfers"
      className="sx-panel sx-dock fixed inset-x-2 bottom-2 z-[65] overflow-hidden animate-slide-up sm:inset-x-auto sm:bottom-4 sm:right-4 sm:w-[380px] sm:max-w-[calc(100vw-2rem)]"
    >
      <div className={clsx('flex h-12 items-center gap-2.5 px-3.5', open && 'border-b border-line')}>
        <span
          className={clsx(
            'flex h-7 w-7 shrink-0 items-center justify-center rounded-lg',
            totals.remaining > 0
              ? 'bg-primary/15 text-primary'
              : totals.failed > 0
                ? 'bg-danger/15 text-danger'
                : 'bg-success/15 text-success',
          )}
        >
          <Icon
            name={totals.remaining > 0 ? 'cloud-upload' : totals.failed > 0 ? 'alert' : 'check-circle'}
            size={15}
          />
        </span>
        <span className="min-w-0 flex-1 truncate text-[13px] font-medium text-ink">{title}</span>
        <span className="shrink-0 text-xs tabular-nums text-muted">{percent(totals.percent)}</span>
        <IconButton
          icon={open ? 'chevron-down' : 'chevron-up'}
          size={16}
          className="sx-touch h-8 w-8"
          label={open ? 'Collapse transfers' : 'Expand transfers'}
          aria-expanded={open}
          onClick={() => setOpen(!open)}
        />
        {!busy && (
          <IconButton icon="close" size={15} className="sx-touch h-8 w-8" label="Close transfers" onClick={dismiss} />
        )}
      </div>

      {!open && totals.remaining > 0 && <Progress value={totals.percent} className="rounded-none" />}

      {open && (
        <>
          <div className="border-b border-line px-3.5 py-3">
            <div className="mb-1.5 flex items-center justify-between gap-3 text-[11px] text-muted">
              <span className="tabular-nums">
                {bytes(totals.done)} of {bytes(totals.total)}
              </span>
              <span className="flex shrink-0 items-center gap-2 tabular-nums">
                {totals.speed > 0 && <span>{rate(totals.speed)}</span>}
                {totals.eta > 0 && <span>{duration(totals.eta)} left</span>}
              </span>
            </div>
            <Progress value={totals.percent} />
          </div>

          <ul className="sx-scroll max-h-[40vh] divide-y divide-line py-0.5 sm:max-h-[320px]">
            {items.map((item) => (
              <DockRow key={item.id} transfer={item} />
            ))}
          </ul>

          {totals.finished > 0 && (
            <div className="flex items-center justify-between gap-2 border-t border-line px-3.5 py-2">
              <span className="text-[11px] text-faint">{counted(totals.finished, 'upload')} finished</span>
              <Button variant="ghost" className="h-7 px-2 text-xs" onClick={clearFinished}>
                Clear finished
              </Button>
            </div>
          )}
        </>
      )}
    </section>
  )
}
