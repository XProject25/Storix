// The two pieces that make storage legible: one stacked bar of the biggest
// items in a folder, and one card for the allowance an account has left.
// Neither of them knows anything about routing, so the storage screen and the
// dashboard can both render them.
// Developed by X Project.

import clsx from 'clsx'
import { useState } from 'react'
import { ago, bytes, counted, percent } from '../lib/format'
import type { Quota } from '../lib/types'
import { Icon } from './Icon'

// The slice palette, in the order a report uses it.
const PALETTE = [
  'rgb(var(--sx-primary))',
  'rgb(var(--sx-secondary))',
  'rgb(var(--sx-accent))',
  'rgb(var(--sx-violet))',
  'rgb(var(--sx-success))',
  'rgb(var(--sx-warning))',
]

/** colourForIndex cycles through the palette so neighbouring slices differ. */
export function colourForIndex(index: number): string {
  const safe = Number.isFinite(index) ? Math.abs(Math.trunc(index)) : 0
  return PALETTE[safe % PALETTE.length]
}

/** UsageSegment is one slice of a stacked bar. */
export interface UsageSegment {
  id: string
  label: string
  bytes: number
  percent: number
  colour: string
  isDir: boolean
}

interface Slice {
  segment: UsageSegment
  start: number
  width: number
}

/**
 * UsageBar draws the segments side by side, widest first if the caller sorted
 * them that way. Widths come from the share each segment reported, so a bar
 * that does not reach the end honestly shows the space nothing accounted for.
 */
export function UsageBar({
  segments,
  height = 16,
  onSelect,
}: {
  segments: UsageSegment[]
  height?: number
  onSelect?: (id: string) => void
}) {
  const [hovered, setHovered] = useState<string | null>(null)

  const visible = segments.filter((segment) => segment.bytes > 0 || segment.percent > 0)
  if (visible.length === 0) {
    return <div className="w-full rounded-full bg-line" style={{ height }} aria-hidden="true" />
  }

  let offset = 0
  const slices: Slice[] = visible.map((segment) => {
    const width = Math.max(0, Math.min(100 - offset, segment.percent))
    const start = offset
    offset += width
    return { segment, start, width }
  })

  const active = slices.find((slice) => slice.segment.id === hovered) ?? null

  return (
    <div className="relative w-full">
      {active && (
        <div
          className="pointer-events-none absolute bottom-full z-10 mb-2 -translate-x-1/2 whitespace-nowrap rounded-lg bg-elevated px-2 py-1 text-[11px] shadow-pop"
          style={{ left: `${active.start + active.width / 2}%` }}
        >
          <span className="font-medium text-ink">{active.segment.label}</span>
          <span className="ml-2 text-muted">{bytes(active.segment.bytes)}</span>
          <span className="ml-1.5 text-faint">{percent(active.segment.percent, 1)}</span>
        </div>
      )}

      <div className="flex w-full overflow-hidden rounded-full bg-line" style={{ height }}>
        {slices.map(({ segment, width }) => {
          const label = `${segment.label}, ${bytes(segment.bytes)}, ${percent(segment.percent, 1)}`
          const shared = {
            title: label,
            style: { width: `${width}%`, background: segment.colour },
            onMouseEnter: () => setHovered(segment.id),
            onMouseLeave: () => setHovered((current) => (current === segment.id ? null : current)),
            className: clsx(
              'h-full shrink-0 border-r border-bg/50 transition-opacity last:border-r-0',
              hovered && hovered !== segment.id ? 'opacity-50' : 'opacity-100',
            ),
          }
          if (!onSelect) return <span key={segment.id} aria-hidden="true" {...shared} />
          return (
            <button
              key={segment.id}
              type="button"
              aria-label={label}
              onFocus={() => setHovered(segment.id)}
              onBlur={() => setHovered((current) => (current === segment.id ? null : current))}
              onClick={() => onSelect(segment.id)}
              {...shared}
            />
          )
        })}
      </div>
    </div>
  )
}

/** QuotaCard shows an account allowance and how close it is to the ceiling. */
export function QuotaCard({ quota }: { quota: Quota }) {
  if (!quota || quota.limit <= 0) return null

  const share = quota.percent > 0 ? quota.percent : (quota.used / quota.limit) * 100
  const value = Math.min(100, Math.max(0, Number.isFinite(share) ? share : 0))
  const tight = value >= 95
  const close = !tight && value >= 80
  const left = Math.max(0, quota.remaining)

  const fill = tight
    ? 'rgb(var(--sx-danger))'
    : close
      ? 'rgb(var(--sx-warning))'
      : 'linear-gradient(90deg, rgb(var(--sx-secondary)), rgb(var(--sx-primary)))'

  return (
    <section className="sx-panel p-5">
      <div className="flex items-center gap-3">
        <span
          className={clsx(
            'flex h-9 w-9 items-center justify-center rounded-xl',
            tight ? 'bg-danger/15 text-danger' : close ? 'bg-warning/15 text-warning' : 'bg-primary/15 text-primary',
          )}
        >
          <Icon name="drive" size={17} />
        </span>
        <div className="min-w-0 flex-1">
          <h2 className="text-[11px] font-semibold uppercase tracking-[0.14em] text-faint">Your allowance</h2>
          <p className="mt-0.5 truncate text-[15px] font-medium text-ink">
            {bytes(quota.used)} of {bytes(quota.limit)}
          </p>
        </div>
        <span
          className={clsx(
            'shrink-0 text-sm font-medium',
            tight ? 'text-danger' : close ? 'text-warning' : 'text-muted',
          )}
        >
          {percent(value)}
        </span>
      </div>

      <div
        className="mt-4 h-2 w-full overflow-hidden rounded-full bg-line"
        role="progressbar"
        aria-valuenow={Math.round(value)}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-label="Allowance used"
      >
        <span
          className="block h-full rounded-full transition-[width] duration-200"
          style={{ width: `${value}%`, background: fill }}
        />
      </div>

      <p className="mt-3 text-sm text-muted">
        {left > 0 ? `${bytes(left)} still yours to use` : 'Nothing left of this allowance'}
        {quota.files > 0 ? `, across ${counted(quota.files, 'file')}` : null}
      </p>

      {tight && (
        <p className="mt-1.5 text-xs text-danger">
          You are almost out of room. Remove something, or ask for a larger allowance.
        </p>
      )}
      {close && <p className="mt-1.5 text-xs text-warning">You are getting close to the limit.</p>}

      {quota.stale ? (
        <p className="mt-3 flex items-center gap-1.5 text-xs text-faint">
          <Icon name="refresh" size={13} className="shrink-0" />
          This figure is being worked out again, so it may be a moment behind.
        </p>
      ) : quota.computedAt ? (
        <p className="mt-3 text-xs text-faint">Measured {ago(quota.computedAt)}</p>
      ) : null}
    </section>
  )
}
