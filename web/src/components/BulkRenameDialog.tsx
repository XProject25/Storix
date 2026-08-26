// Rename many files at once, with a preview that shows exactly what will
// happen before anything on the disk is touched.
// Developed by X Project.

import clsx from 'clsx'
import { useEffect, useMemo, useState } from 'react'
import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ApiError, api } from '../lib/api'
import type { Entry, RenameChange, RenameMode, RenameRule } from '../lib/types'
import { counted } from '../lib/format'
import { Icon } from './Icon'
import { Button, Field, Modal, Skeleton, Toggle, useToast } from './ui'

// ---- helpers ----------------------------------------------------------------

/** bulkExplain turns any thrown value into one calm sentence for the reader. */
function bulkExplain(error: unknown, fallback: string): string {
  if (error instanceof ApiError) {
    if (error.status === 403) return error.message || 'You do not have permission to rename these items.'
    if (error.status === 404) return 'Some of these items are no longer there. Refresh and try again.'
    if (error.status === 409) return error.message || 'Something with that name already exists here.'
    return error.detail ? `${error.message} ${error.detail}` : error.message || fallback
  }
  if (error instanceof Error && error.message) return error.message
  return fallback
}

/** bulkPatternError validates a regular expression before it reaches the server. */
function bulkPatternError(find: string): string | null {
  if (!find) return null
  try {
    new RegExp(find)
    return null
  } catch (error) {
    const detail = error instanceof Error ? error.message : ''
    return detail ? `That pattern cannot be read. ${detail}` : 'That pattern cannot be read.'
  }
}

interface BulkCounts {
  valid: number
  unchanged: number
  conflicts: number
}

/** bulkCounts derives the summary straight from the rows on screen. */
function bulkCounts(rows: RenameChange[]): BulkCounts {
  let valid = 0
  let unchanged = 0
  let conflicts = 0
  for (const row of rows) {
    if (row.conflict) conflicts++
    else if (row.unchanged) unchanged++
    else valid++
  }
  return { valid, unchanged, conflicts }
}

/** bulkWholeNumber reads a number field, returning null when it is not usable. */
function bulkWholeNumber(value: string, max: number): number | null {
  const trimmed = value.trim()
  if (!trimmed) return null
  const parsed = Number(trimmed)
  if (!Number.isInteger(parsed) || parsed < 0 || parsed > max) return null
  return parsed
}

const BULK_MODES: Array<{ id: RenameMode; label: string }> = [
  { id: 'replace', label: 'Find and replace' },
  { id: 'prefix', label: 'Add prefix' },
  { id: 'suffix', label: 'Add suffix' },
  { id: 'number', label: 'Number them' },
  { id: 'case', label: 'Change case' },
]

const BULK_CASINGS: Array<{ id: 'lower' | 'upper' | 'title'; label: string }> = [
  { id: 'lower', label: 'lower case' },
  { id: 'upper', label: 'UPPER CASE' },
  { id: 'title', label: 'Title Case' },
]

const BULK_ROW_LIMIT = 200
const BULK_DEFAULT_PATTERN = '{name}-{n}'

/** BulkSegmented is the one row of choices this dialog uses for its modes. */
function BulkSegmented<T extends string>({
  value,
  options,
  label,
  onChange,
}: {
  value: T
  options: Array<{ id: T; label: string }>
  label: string
  onChange: (value: T) => void
}) {
  return (
    <div
      role="radiogroup"
      aria-label={label}
      className="flex flex-wrap gap-1 rounded-xl border border-line bg-elevated p-1"
    >
      {options.map((option) => (
        <button
          key={option.id}
          type="button"
          role="radio"
          aria-checked={value === option.id}
          onClick={() => onChange(option.id)}
          className={clsx(
            'h-8 flex-1 whitespace-nowrap rounded-lg px-3 text-xs font-medium transition-colors',
            value === option.id ? 'bg-primary/15 text-ink' : 'text-muted hover:bg-line/40 hover:text-ink',
          )}
        >
          {option.label}
        </button>
      ))}
    </div>
  )
}

/** BulkPreviewTable lists every current name beside the name it would get. */
function BulkPreviewTable({ rows }: { rows: RenameChange[] }) {
  const shown = rows.slice(0, BULK_ROW_LIMIT)
  return (
    <table className="w-full table-fixed border-collapse text-xs">
      <thead>
        <tr className="text-left text-[11px] uppercase tracking-[0.08em] text-faint">
          <th className="px-3 py-2 font-medium">Now</th>
          <th className="w-8 py-2 font-medium">
            <span className="sr-only">becomes</span>
          </th>
          <th className="px-3 py-2 font-medium">After</th>
        </tr>
      </thead>
      <tbody>
        {shown.map((row) => (
          <tr key={row.path} className={clsx('border-t border-line/60', row.conflict && 'bg-danger/5')}>
            <td className="px-3 py-1.5 align-top">
              <span className="block truncate text-muted" title={row.from}>
                {row.from}
              </span>
            </td>
            <td className="py-1.5 text-center align-top text-faint">
              <Icon name="arrow-right" size={13} className="inline-block" />
            </td>
            <td className="px-3 py-1.5 align-top">
              <span
                className={clsx('block truncate', row.conflict ? 'text-danger' : row.unchanged ? 'text-faint' : 'text-ink')}
                title={row.to}
              >
                {row.to}
              </span>
              {row.conflict && (
                <span className="block truncate text-[11px] text-danger/85">
                  {row.reason || 'Something with that name is already there.'}
                </span>
              )}
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  )
}

// ---- dialog -----------------------------------------------------------------

export function BulkRenameDialog({
  open,
  entries,
  onClose,
  onDone,
}: {
  open: boolean
  entries: Entry[]
  onClose: () => void
  onDone?: (renamed: number) => void
}) {
  const client = useQueryClient()
  const toast = useToast()

  const [mode, setMode] = useState<RenameMode>('replace')
  const [find, setFind] = useState('')
  const [replace, setReplace] = useState('')
  const [regex, setRegex] = useState(false)
  const [caseSensitive, setCaseSensitive] = useState(false)
  const [text, setText] = useState('')
  const [keepExtension, setKeepExtension] = useState(true)
  const [pattern, setPattern] = useState(BULK_DEFAULT_PATTERN)
  const [start, setStart] = useState('1')
  const [padding, setPadding] = useState('0')
  const [casing, setCasing] = useState<'lower' | 'upper' | 'title'>('lower')
  const [failure, setFailure] = useState('')

  useEffect(() => {
    if (!open) return
    setMode('replace')
    setFind('')
    setReplace('')
    setRegex(false)
    setCaseSensitive(false)
    setText('')
    setKeepExtension(true)
    setPattern(BULK_DEFAULT_PATTERN)
    setStart('1')
    setPadding('0')
    setCasing('lower')
    setFailure('')
  }, [open])

  const paths = useMemo(() => entries.map((entry) => entry.path), [entries])
  const patternError = regex ? bulkPatternError(find) : null
  const startNumber = bulkWholeNumber(start, 1_000_000)
  const padNumber = bulkWholeNumber(padding, 10)

  const rule = useMemo<RenameRule>(() => {
    switch (mode) {
      case 'prefix':
        return { mode, text }
      case 'suffix':
        return { mode, text, keepExtension }
      case 'number':
        return { mode, pattern, start: startNumber ?? 1, padding: padNumber ?? 0 }
      case 'case':
        return { mode, casing }
      default:
        return { mode: 'replace', find, replace, regex, caseSensitive }
    }
  }, [mode, text, keepExtension, pattern, startNumber, padNumber, casing, find, replace, regex, caseSensitive])

  // The rule is only worth sending once its own fields make sense.
  const ready =
    mode === 'replace'
      ? find !== '' && patternError === null
      : mode === 'prefix' || mode === 'suffix'
        ? text !== ''
        : mode === 'number'
          ? pattern !== '' && startNumber !== null && padNumber !== null
          : true

  // The preview follows typing, so it waits for a short pause before asking.
  const serialized = JSON.stringify(rule)
  const [applied, setApplied] = useState(serialized)

  useEffect(() => {
    const timer = window.setTimeout(() => setApplied(serialized), 250)
    return () => window.clearTimeout(timer)
  }, [serialized])

  const preview = useQuery({
    queryKey: ['rename-preview', paths, applied],
    queryFn: () => api.renamePreview(paths, JSON.parse(applied) as RenameRule),
    enabled: open && ready && paths.length > 0,
    placeholderData: keepPreviousData,
  })

  const idleRows = useMemo<RenameChange[]>(
    () =>
      entries.map((entry) => ({
        path: entry.path,
        from: entry.name,
        to: entry.name,
        conflict: false,
        unchanged: true,
      })),
    [entries],
  )

  const rows = ready ? (preview.data?.changes ?? idleRows) : idleRows
  const counts =
    ready && preview.data
      ? { valid: preview.data.valid, unchanged: preview.data.unchanged, conflicts: preview.data.conflicts }
      : bulkCounts(rows)

  const rename = useMutation({
    // The applied rule is the one the table was built from, so what the reader
    // confirmed is exactly what runs, even mid keystroke.
    mutationFn: () => api.renameBulk(paths, JSON.parse(applied) as RenameRule),
    onSuccess: (result) => {
      void client.invalidateQueries({ queryKey: ['list'] })
      const failed = result.failed ?? []
      if (failed.length > 0) {
        const first = failed[0]
        setFailure(
          `${counted(failed.length, 'item')} could not be renamed.${first?.reason ? ` ${first.reason}` : ''}`,
        )
        return
      }
      toast.success(`${counted(result.renamed, 'file')} renamed`)
      onDone?.(result.renamed)
      onClose()
    },
    onError: (error) => setFailure(bulkExplain(error, 'Nothing was renamed.')),
  })

  // Confirming is only offered once the table on screen belongs to the rule
  // that would run, so nothing is renamed against a preview still catching up.
  const settled = ready && !preview.isFetching && !preview.isPlaceholderData && !preview.isError
  const blocked = !settled || counts.conflicts > 0 || counts.valid === 0

  return (
    <Modal
      open={open && entries.length > 0}
      onClose={onClose}
      icon="edit"
      title="Rename many"
      description={`${counted(entries.length, 'item')} selected. Nothing changes until you confirm.`}
      width={720}
      footer={
        <>
          <Button onClick={onClose} disabled={rename.isPending}>
            Cancel
          </Button>
          <Button
            variant="primary"
            onClick={() => {
              setFailure('')
              rename.mutate()
            }}
            loading={rename.isPending}
            disabled={blocked}
          >
            {counts.valid > 0 ? `Rename ${counted(counts.valid, 'file')}` : 'Rename'}
          </Button>
        </>
      }
    >
      <BulkSegmented
        label="What to change"
        value={mode}
        options={BULK_MODES}
        onChange={(next) => {
          setMode(next)
          setFailure('')
        }}
      />

      <div className="mt-4 space-y-3">
        {mode === 'replace' && (
          <>
            <div className="grid gap-3 sm:grid-cols-2">
              <Field
                label="Find"
                value={find}
                spellCheck={false}
                autoComplete="off"
                placeholder={regex ? '^IMG_(\\d+)' : 'Text to look for'}
                onChange={(event) => {
                  setFind(event.target.value)
                  setFailure('')
                }}
                error={patternError ?? undefined}
              />
              <Field
                label="Replace with"
                value={replace}
                spellCheck={false}
                autoComplete="off"
                placeholder="Leave empty to remove it"
                hint={regex ? 'Use $1 for the first group of the pattern.' : undefined}
                onChange={(event) => {
                  setReplace(event.target.value)
                  setFailure('')
                }}
              />
            </div>
            <div className="grid gap-3 sm:grid-cols-2">
              <Toggle
                checked={regex}
                onChange={(value) => {
                  setRegex(value)
                  setFailure('')
                }}
                label="Use a pattern"
                hint="Read what you typed as a regular expression."
              />
              <Toggle
                checked={caseSensitive}
                onChange={(value) => {
                  setCaseSensitive(value)
                  setFailure('')
                }}
                label="Match case"
                hint="Only replace text with the same capitals."
              />
            </div>
          </>
        )}

        {(mode === 'prefix' || mode === 'suffix') && (
          <>
            <Field
              label="Text"
              value={text}
              spellCheck={false}
              autoComplete="off"
              placeholder={mode === 'prefix' ? '2026-' : '-final'}
              hint={
                mode === 'prefix' ? 'Added to the front of every name.' : 'Added to the end of every name.'
              }
              onChange={(event) => {
                setText(event.target.value)
                setFailure('')
              }}
            />
            {mode === 'suffix' && (
              <Toggle
                checked={keepExtension}
                onChange={(value) => {
                  setKeepExtension(value)
                  setFailure('')
                }}
                label="Keep the extension at the end"
                hint="report.pdf becomes report-final.pdf instead of report.pdf-final."
              />
            )}
          </>
        )}

        {mode === 'number' && (
          <>
            <Field
              label="Pattern"
              value={pattern}
              spellCheck={false}
              autoComplete="off"
              hint="{n} is the counter and {name} is the current name."
              onChange={(event) => {
                setPattern(event.target.value)
                setFailure('')
              }}
            />
            <div className="grid gap-3 sm:grid-cols-2">
              <Field
                label="Start number"
                value={start}
                inputMode="numeric"
                spellCheck={false}
                autoComplete="off"
                error={startNumber === null ? 'Enter a whole number.' : undefined}
                hint="The first file gets this number."
                onChange={(event) => {
                  setStart(event.target.value)
                  setFailure('')
                }}
              />
              <Field
                label="Zero padding"
                value={padding}
                inputMode="numeric"
                spellCheck={false}
                autoComplete="off"
                error={padNumber === null ? 'Enter a whole number from 0 to 10.' : undefined}
                hint="2 counts 01, 02, 03 instead of 1, 2, 3."
                onChange={(event) => {
                  setPadding(event.target.value)
                  setFailure('')
                }}
              />
            </div>
          </>
        )}

        {mode === 'case' && (
          <div>
            <span className="sx-label">How the names should read</span>
            <BulkSegmented
              label="How the names should read"
              value={casing}
              options={BULK_CASINGS}
              onChange={(next) => {
                setCasing(next)
                setFailure('')
              }}
            />
          </div>
        )}
      </div>

      <div className="mt-4">
        <span className="sx-label">What will happen</span>
        <div className="sx-scroll max-h-[min(288px,34vh)] rounded-xl border border-line bg-elevated/40">
          {ready && preview.isLoading ? (
            <div className="space-y-2 p-3">
              <Skeleton className="h-4 w-3/4" />
              <Skeleton className="h-4 w-2/3" />
              <Skeleton className="h-4 w-1/2" />
            </div>
          ) : ready && preview.isError ? (
            <p className="px-3 py-6 text-center text-xs text-danger">
              {bulkExplain(preview.error, 'The preview could not be worked out.')}
            </p>
          ) : (
            <BulkPreviewTable rows={rows} />
          )}
        </div>

        <div className="mt-1.5 flex flex-wrap items-center gap-x-3 gap-y-1">
          <p className="text-xs text-muted">
            {counts.valid.toLocaleString()} will be renamed, {counts.unchanged.toLocaleString()} unchanged,{' '}
            <span className={counts.conflicts > 0 ? 'text-danger' : undefined}>
              {counted(counts.conflicts, 'conflict')}
            </span>
          </p>
          {rows.length > BULK_ROW_LIMIT && (
            <p className="text-xs text-faint">
              Showing the first {BULK_ROW_LIMIT}, {counted(rows.length - BULK_ROW_LIMIT, 'more row')} follow the same
              rule.
            </p>
          )}
        </div>

        {counts.conflicts > 0 && (
          <p className="mt-1.5 text-xs text-danger">
            Two names would end up the same, or a name is already taken. Adjust the rule to continue.
          </p>
        )}
      </div>

      {failure && (
        <div
          role="alert"
          className="mt-4 flex items-start gap-2.5 rounded-xl border border-danger/35 bg-danger/10 px-3 py-2.5 text-sm text-danger"
        >
          <Icon name="alert" size={16} className="mt-0.5 shrink-0" />
          <span className="min-w-0 break-words">{failure}</span>
        </div>
      )}
    </Modal>
  )
}

export default BulkRenameDialog
