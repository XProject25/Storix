// Duplicate files: the same bytes kept more than once, and a careful way to
// win that space back. Comparing files means reading them, so this panel never
// starts on its own. It waits to be asked, and it never removes the last copy
// of anything.
// Developed by X Project.

import clsx from 'clsx'
import { useEffect, useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ApiError, api } from '../lib/api'
import type { DuplicateFile, DuplicateGroup, DuplicateReport } from '../lib/types'
import { bytes, counted, parentPath, smartDate, truncateMiddle } from '../lib/format'
import { useSession } from '../lib/session'
import { Icon } from './Icon'
import { Button, Checkbox, ConfirmDialog, SectionTitle, Select, Spinner, useToast } from './ui'

// The thresholds worth offering. Duplicate small files are rarely worth
// anyone's time, so the default sits at one megabyte.
const DUP_MIN_OPTIONS = [
  { value: '1024', label: '1 KB' },
  { value: '1048576', label: '1 MB' },
  { value: '10485760', label: '10 MB' },
  { value: '104857600', label: '100 MB' },
]

const DUP_DEFAULT_MIN = '1048576'

// After this long a scan should say something, rather than spin in silence.
const DUP_SLOW_SECONDS = 3

/** DupScan is the request a person actually asked for, held apart from the controls. */
interface DupScan {
  path: string
  min: number
}

/** dupReason turns a failed request into one sentence a person can act on. */
function dupReason(error: unknown): string {
  if (error instanceof ApiError) return error.detail ? `${error.message} ${error.detail}` : error.message
  if (error instanceof Error && error.message) return error.message
  return 'The server did not answer.'
}

/** dupMinLabel names a threshold the way the select does. */
function dupMinLabel(min: number): string {
  const option = DUP_MIN_OPTIONS.find((entry) => entry.value === String(min))
  return option ? option.label : bytes(min)
}

/** dupScanTime renders how long the server spent comparing. */
function dupScanTime(elapsedMs: number): string {
  if (!Number.isFinite(elapsedMs) || elapsedMs <= 0) return 'no time at all'
  if (elapsedMs < 1000) return `${Math.round(elapsedMs)} ms`
  return `${(elapsedMs / 1000).toFixed(1)} s`
}

/** dupSentence finishes a server message so it reads as one. */
function dupSentence(text: string): string {
  const trimmed = text.trim()
  if (trimmed === '') return ''
  return /[.!?]$/.test(trimmed) ? trimmed : `${trimmed}.`
}

/**
 * dupTruncationNote prefers the server's own explanation of why a scan stopped
 * short, and falls back to the plain facts when the report carries none.
 */
function dupTruncationNote(report: DuplicateReport): string {
  const carried = (report as DuplicateReport & { message?: string }).message
  const note = typeof carried === 'string' ? dupSentence(carried) : ''
  if (note !== '') return note
  return `This folder holds more than one search covers, so the report stops short at ${counted(
    report.scanned,
    'file',
  )}.`
}

/** dupOrdered sorts one group so the oldest copy comes first. */
function dupOrdered(group: DuplicateGroup): DuplicateFile[] {
  return [...group.files].sort((left, right) => {
    const a = Date.parse(left.modified)
    const b = Date.parse(right.modified)
    if (Number.isFinite(a) && Number.isFinite(b) && a !== b) return a - b
    return left.path.localeCompare(right.path)
  })
}

/** dupInitialSelection picks every copy except the oldest one in each group. */
function dupInitialSelection(groups: DuplicateGroup[]): Set<string> {
  const selected = new Set<string>()
  for (const group of groups) {
    for (const file of dupOrdered(group).slice(1)) selected.add(file.path)
  }
  return selected
}

/** DupPlan is what would happen to one group if the reader confirmed now. */
interface DupPlan {
  group: DuplicateGroup
  ordered: DuplicateFile[]
  keeper: DuplicateFile | null
  forced: boolean
  remove: DuplicateFile[]
}

/**
 * dupPlan works out the removals for one group. A group always keeps a copy:
 * when every copy has been picked, the oldest one is forced back off the list,
 * which is what forced reports so the confirm can say why.
 */
function dupPlan(group: DuplicateGroup, selected: Set<string>): DupPlan {
  const ordered = dupOrdered(group)
  if (ordered.length === 0) return { group, ordered, keeper: null, forced: false, remove: [] }
  const keeper = ordered[0]
  const forced = ordered.every((file) => selected.has(file.path))
  const remove = ordered.filter((file) => selected.has(file.path) && !(forced && file.path === keeper.path))
  return { group, ordered, keeper, forced, remove }
}

/** DupTotals is the headline for everything currently picked. */
interface DupTotals {
  files: number
  bytes: number
  groups: number
  forced: number
}

/** dupTotals adds the plans up. */
function dupTotals(plans: DupPlan[]): DupTotals {
  let files = 0
  let size = 0
  let groups = 0
  let forced = 0
  for (const plan of plans) {
    if (plan.remove.length > 0) groups++
    if (plan.forced) forced++
    for (const file of plan.remove) {
      files++
      size += file.size
    }
  }
  return { files, bytes: size, groups, forced }
}

/** dupPaths flattens the plans into the list the delete call takes. */
function dupPaths(plans: DupPlan[]): string[] {
  const paths: string[] = []
  for (const plan of plans) {
    for (const file of plan.remove) paths.push(file.path)
  }
  return paths
}

// ---- pieces -----------------------------------------------------------------

function DupGroupRow({
  plan,
  expanded,
  onToggle,
  onPick,
}: {
  plan: DupPlan
  expanded: boolean
  onToggle: () => void
  onPick: (path: string, picked: boolean) => void
}) {
  const { group, ordered, keeper, forced, remove } = plan
  const title = ordered[0]?.name ?? 'Unnamed file'

  return (
    <div className="overflow-hidden rounded-xl border border-line">
      <button
        type="button"
        onClick={onToggle}
        aria-expanded={expanded}
        className="flex w-full items-center gap-3 px-3 py-2.5 text-left transition-colors hover:bg-elevated"
      >
        <Icon name={expanded ? 'chevron-down' : 'chevron-right'} size={15} className="shrink-0 text-faint" />
        <span className="min-w-0 flex-1">
          <span className="block truncate text-sm text-ink">{title}</span>
          <span className="block truncate text-xs text-faint">
            {counted(group.count, 'copy', 'copies')} of {bytes(group.size)}
          </span>
        </span>
        <span className="shrink-0 text-xs text-muted">{bytes(group.wasted)} wasted</span>
        <span className="w-24 shrink-0 text-right text-xs text-faint">
          {remove.length > 0 ? `${remove.length} picked` : 'none picked'}
        </span>
      </button>

      {expanded && (
        <div className="border-t border-line/70 px-3 py-1.5">
          {ordered.map((file) => {
            const isKeeper = keeper?.path === file.path
            const picked = remove.some((item) => item.path === file.path)
            return (
              <div key={file.path} className="flex items-center gap-3 py-1.5">
                <Checkbox checked={picked} onChange={(value) => onPick(file.path, value)} className="shrink-0" />
                <span className="min-w-0 flex-1">
                  <span className="block truncate text-sm text-ink" title={file.path}>
                    {file.name}
                  </span>
                  <span className="block truncate text-xs text-faint">
                    {truncateMiddle(parentPath(file.path) || '/', 52)}
                  </span>
                </span>
                <span className="hidden shrink-0 text-xs text-faint sm:block">{smartDate(file.modified)}</span>
                {isKeeper && (
                  <span
                    className={clsx(
                      'sx-chip shrink-0',
                      forced ? 'border-warning/40 bg-warning/10 text-warning' : 'border-success/40 bg-success/10 text-success',
                    )}
                  >
                    {forced ? 'Kept anyway' : 'Kept'}
                  </span>
                )}
              </div>
            )
          })}
          {forced && (
            <p className="mb-1 mt-1.5 flex items-start gap-2 rounded-lg border border-warning/35 bg-warning/10 px-2.5 py-1.5 text-xs text-warning">
              <Icon name="info" size={13} className="mt-px shrink-0" />
              Every copy here is picked. A group always keeps one, so the oldest copy stays where it is.
            </p>
          )}
        </div>
      )}
    </div>
  )
}

// ---- panel ------------------------------------------------------------------

export default function DuplicateFinder({ path }: { path: string }) {
  const client = useQueryClient()
  const toast = useToast()
  const { can } = useSession()

  const [min, setMin] = useState(DUP_DEFAULT_MIN)
  const [scan, setScan] = useState<DupScan | null>(null)
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [expanded, setExpanded] = useState<Set<string>>(new Set())
  const [confirming, setConfirming] = useState(false)
  const [seconds, setSeconds] = useState(0)

  // A scan belongs to the folder it was asked for, so moving on drops it.
  useEffect(() => {
    setScan(null)
    setSelected(new Set())
    setExpanded(new Set())
  }, [path])

  const duplicates = useQuery({
    queryKey: ['duplicates', scan?.path ?? '', scan?.min ?? 0],
    queryFn: () => api.duplicates(scan?.path ?? '', scan?.min ?? 0),
    enabled: scan !== null && scan.path !== '',
    staleTime: 60_000,
  })

  const report = duplicates.data
  const scanning = duplicates.isFetching

  // Fresh results start from the safe answer: keep the oldest, pick the rest.
  useEffect(() => {
    if (!report) return
    setSelected(dupInitialSelection(report.groups))
    setExpanded(report.groups.length > 0 ? new Set([report.groups[0].hash]) : new Set())
  }, [report])

  // A slow scan should say so rather than spin behind nothing.
  useEffect(() => {
    if (!scanning) {
      setSeconds(0)
      return
    }
    const started = Date.now()
    const timer = window.setInterval(() => setSeconds(Math.floor((Date.now() - started) / 1000)), 500)
    return () => window.clearInterval(timer)
  }, [scanning])

  const plans = useMemo(
    () => (report ? report.groups.map((group) => dupPlan(group, selected)) : []),
    [report, selected],
  )
  const totals = useMemo(() => dupTotals(plans), [plans])

  const remove = useMutation({
    mutationFn: (paths: string[]) => api.remove(paths, false),
    onSuccess: (_result, paths) => {
      toast.success(
        `${counted(paths.length, 'copy', 'copies')} moved to the bin`,
        'Nothing was erased. They can be put back from Trash.',
      )
      void client.invalidateQueries({ queryKey: ['list'] })
      void client.invalidateQueries({ queryKey: ['usage'] })
      void client.invalidateQueries({ queryKey: ['trash'] })
      void client.invalidateQueries({ queryKey: ['quota'] })
      void duplicates.refetch()
    },
    onError: (error) => toast.error('Nothing was removed', dupReason(error)),
  })

  const pick = (target: string, picked: boolean) => {
    setSelected((current) => {
      const next = new Set(current)
      if (picked) next.add(target)
      else next.delete(target)
      return next
    })
  }

  const toggleGroup = (hash: string) => {
    setExpanded((current) => {
      const next = new Set(current)
      if (next.has(hash)) next.delete(hash)
      else next.add(hash)
      return next
    })
  }

  const start = () => {
    setSelected(new Set())
    setExpanded(new Set())
    setScan({ path, min: Number(min) })
  }

  const threshold = scan ? dupMinLabel(scan.min) : dupMinLabel(Number(min))

  return (
    <section className="sx-panel p-5">
      <SectionTitle>Duplicates</SectionTitle>

      <div className="flex flex-wrap items-end justify-between gap-4">
        <p className="max-w-xl text-sm text-muted">
          The same file kept in more than one place costs space every time. Finding copies means reading the files
          themselves, so the search runs only when you ask for it.
        </p>
        <div className="flex items-end gap-2">
          <Select
            label="Smallest file to compare"
            value={min}
            onChange={setMin}
            options={DUP_MIN_OPTIONS}
            className="w-44"
          />
          <Button variant="primary" icon="copy" onClick={start} loading={scanning} disabled={path === ''}>
            Find duplicates
          </Button>
        </div>
      </div>

      {scanning && (
        <div className="mt-5 flex items-start gap-3 rounded-xl border border-line bg-elevated/60 px-3 py-3">
          <Spinner size={16} className="mt-0.5 shrink-0 text-primary" />
          <div className="min-w-0">
            <p className="text-sm text-ink">Comparing files</p>
            {seconds >= DUP_SLOW_SECONDS && (
              <p className="mt-1 text-xs text-faint">
                Still going, {counted(seconds, 'second')} so far. Only files that share a size are ever opened, so most
                of this folder is never read.
              </p>
            )}
          </div>
        </div>
      )}

      {!scanning && duplicates.isError && (
        <div className="mt-5 flex flex-wrap items-center gap-3 rounded-xl border border-danger/35 bg-danger/10 px-3 py-2.5">
          <Icon name="alert" size={16} className="shrink-0 text-danger" />
          <span className="min-w-0 flex-1 text-sm text-danger">{dupReason(duplicates.error)}</span>
          <Button icon="refresh" onClick={() => void duplicates.refetch()}>
            Try again
          </Button>
        </div>
      )}

      {!scanning && report && report.groups.length === 0 && (
        <div className="mt-5 rounded-xl border border-line bg-elevated/60 px-3 py-6 text-center">
          <p className="text-sm text-ink">No duplicates found above {threshold}</p>
          <p className="mt-1 text-xs text-faint">
            Lower the threshold to take smaller files in as well, then search again.
          </p>
        </div>
      )}

      {!scanning && report && report.groups.length > 0 && (
        <div className="mt-5">
          <p className="text-2xl font-semibold tracking-tight text-ink">
            {bytes(report.wasted)} can be freed across {counted(report.groups.length, 'group')}
          </p>
          <p className="mt-1.5 flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-faint">
            <Icon name="clock" size={13} className="shrink-0" />
            <span>
              Compared {counted(report.scanned, 'file')} above {threshold}, opening {counted(report.hashed, 'file')}, in{' '}
              {dupScanTime(report.elapsedMs)}
            </span>
          </p>

          {report.truncated && (
            <p className="mt-3 flex items-start gap-2 rounded-xl border border-warning/35 bg-warning/10 px-3 py-2 text-xs text-warning">
              <Icon name="info" size={14} className="mt-px shrink-0" />
              <span>{dupTruncationNote(report)} The groups below are the ones it found before it ended.</span>
            </p>
          )}

          <div className="mt-4 space-y-2">
            {plans.map((plan) => (
              <DupGroupRow
                key={plan.group.hash}
                plan={plan}
                expanded={expanded.has(plan.group.hash)}
                onToggle={() => toggleGroup(plan.group.hash)}
                onPick={pick}
              />
            ))}
          </div>

          <div className="mt-4 flex flex-wrap items-center gap-3 rounded-xl border border-line bg-elevated/60 px-3 py-2.5">
            <span className="min-w-0 flex-1 text-sm text-muted">
              {totals.files > 0
                ? `${counted(totals.files, 'copy', 'copies')} picked in ${counted(totals.groups, 'group')}, ${bytes(
                    totals.bytes,
                  )} to reclaim`
                : 'Nothing is picked, so nothing would be removed.'}
            </span>
            <Button onClick={() => setSelected(new Set())} disabled={totals.files === 0}>
              Clear picks
            </Button>
            {can('delete') && (
              <Button
                variant="danger"
                icon="trash"
                onClick={() => setConfirming(true)}
                disabled={totals.files === 0}
                loading={remove.isPending}
              >
                Move selected to the bin
              </Button>
            )}
          </div>
        </div>
      )}

      <ConfirmDialog
        open={confirming}
        danger
        title="Move copies to the bin"
        confirmLabel="Move to the bin"
        message={
          <>
            <p>
              {counted(totals.files, 'copy', 'copies')} go to the recycle bin and free {bytes(totals.bytes)}. Nothing is
              erased, so anything picked by mistake can be put back from Trash.
            </p>
            {totals.forced > 0 && (
              <p className="mt-2 text-warning">
                Every copy is picked in {counted(totals.forced, 'group')}. A group always keeps one, so the oldest copy
                in {totals.forced === 1 ? 'that group' : 'those groups'} stays where it is.
              </p>
            )}
          </>
        }
        busy={remove.isPending}
        onCancel={() => setConfirming(false)}
        onConfirm={() => {
          const paths = dupPaths(plans)
          setConfirming(false)
          if (paths.length > 0) remove.mutate(paths)
        }}
      />
    </section>
  )
}
