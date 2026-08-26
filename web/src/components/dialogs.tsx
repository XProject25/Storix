// Every file operation dialog the browser needs, in one module. They all build
// on Modal so the product only ever shows one kind of dialog.
// Developed by X Project.

import clsx from 'clsx'
import { Fragment, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ApiError, api } from '../lib/api'
import type { Entry, Job } from '../lib/types'
import { baseName, bytes, counted, joinPath, modeToText, parentPath, smartDate, truncateMiddle } from '../lib/format'
import { useSession } from '../lib/session'
import { Icon, type IconName } from './Icon'
import { Button, Checkbox, EmptyState, Field, Modal, Skeleton, Spinner, Toggle, useToast } from './ui'

// ---- shared helpers ---------------------------------------------------------

/** nameError validates a single path element the way the server will. */
function nameError(value: string): string | null {
  const name = value.trim()
  if (!name) return 'Enter a name.'
  if (name === '.' || name === '..') return 'That name is reserved. Choose another one.'
  if (name.includes('/')) return 'A name cannot contain a slash.'
  if (name.length > 255) return 'That name is too long. Keep it under 255 characters.'
  for (let index = 0; index < name.length; index++) {
    if (name.charCodeAt(index) < 32) return 'That name contains characters that are not allowed.'
  }
  return null
}

/** explain turns any thrown value into one calm sentence for the reader. */
function explain(error: unknown, fallback: string): string {
  if (error instanceof ApiError) {
    if (error.status === 403) return error.message || 'You do not have permission to do this.'
    if (error.status === 404) return 'That item is no longer there. Refresh and try again.'
    if (error.status === 409) return error.message || 'Something with that name already exists here.'
    if (error.status === 507) return 'There is not enough free space on the disk.'
    return error.detail ? `${error.message} ${error.detail}` : error.message || fallback
  }
  if (error instanceof Error && error.message) return error.message
  return fallback
}

/** ErrorNote is the inline failure block every dialog uses instead of a toast. */
function ErrorNote({ message }: { message: string }) {
  if (!message) return null
  return (
    <div
      role="alert"
      className="mt-4 flex items-start gap-2.5 rounded-xl border border-danger/35 bg-danger/10 px-3 py-2.5 text-sm text-danger"
    >
      <Icon name="alert" size={16} className="mt-0.5 shrink-0" />
      <span className="min-w-0 break-words">{message}</span>
    </div>
  )
}

/** InfoNote is the calm counterpart used for context the reader should notice. */
function InfoNote({ children }: { children: ReactNode }) {
  return (
    <div className="mt-3 flex items-start gap-2.5 rounded-xl border border-line bg-elevated px-3 py-2.5 text-xs text-muted">
      <Icon name="info" size={15} className="mt-0.5 shrink-0 text-faint" />
      <span className="min-w-0">{children}</span>
    </div>
  )
}

/** RadioCard is the one choice control the dialogs share. */
function RadioCard({
  group,
  value,
  current,
  onChange,
  title,
  description,
  disabled,
}: {
  group: string
  value: string
  current: string
  onChange: (value: string) => void
  title: string
  description?: string
  disabled?: boolean
}) {
  const checked = current === value
  return (
    <label
      className={clsx(
        'flex items-start gap-3 rounded-xl border px-3 py-2.5 transition-colors',
        disabled ? 'cursor-not-allowed opacity-50' : 'cursor-pointer',
        checked ? 'border-primary/60 bg-primary/10' : 'border-line bg-elevated hover:bg-line/40',
      )}
    >
      <input
        type="radio"
        name={group}
        value={value}
        checked={checked}
        disabled={disabled}
        onChange={() => onChange(value)}
        className="peer sr-only"
      />
      <span
        className={clsx(
          'mt-0.5 flex h-[18px] w-[18px] shrink-0 items-center justify-center rounded-full border transition-colors',
          'peer-focus-visible:ring-2 peer-focus-visible:ring-primary/70 peer-focus-visible:ring-offset-0',
          checked ? 'border-primary' : 'border-line',
        )}
      >
        {checked && <span className="h-2 w-2 rounded-full bg-primary" />}
      </span>
      <span className="min-w-0">
        <span className="block text-sm text-ink">{title}</span>
        {description && <span className="mt-0.5 block text-xs text-muted">{description}</span>}
      </span>
    </label>
  )
}

/**
 * useNameField focuses the single text input of a dialog when it opens and
 * selects the part of the name the reader is most likely to replace.
 */
function useNameField(open: boolean, selection?: [number, number]) {
  const wrap = useRef<HTMLDivElement>(null)
  const range = useRef<[number, number] | undefined>(selection)
  range.current = selection

  useEffect(() => {
    if (!open) return
    const timer = window.setTimeout(() => {
      const input = wrap.current?.querySelector('input')
      if (!input) return
      input.focus()
      const value = range.current
      if (value) input.setSelectionRange(value[0], value[1])
      else input.select()
    }, 40)
    return () => window.clearTimeout(timer)
  }, [open])

  return wrap
}

/** baseSelection returns the range covering a file name without its extension. */
function baseSelection(name: string): [number, number] {
  const dot = name.lastIndexOf('.')
  if (dot <= 0) return [0, name.length]
  return [0, dot]
}

function extensionOfName(name: string): string {
  const dot = name.lastIndexOf('.')
  if (dot <= 0) return ''
  return name.slice(dot + 1).toLowerCase()
}

// ---- new folder -------------------------------------------------------------

export interface CreateDialogProps {
  open: boolean
  path: string
  onClose: () => void
  onCreated?: (entry: Entry) => void
}

export function NewFolderDialog({ open, path, onClose, onCreated }: CreateDialogProps) {
  const client = useQueryClient()
  const [name, setName] = useState('New folder')
  const [failure, setFailure] = useState('')
  const wrap = useNameField(open)

  useEffect(() => {
    if (!open) return
    setName('New folder')
    setFailure('')
  }, [open])

  const invalid = nameError(name)

  const create = useMutation({
    mutationFn: () => api.mkdir(path, name.trim()),
    onSuccess: (entry) => {
      void client.invalidateQueries({ queryKey: ['list', path] })
      void client.invalidateQueries({ queryKey: ['tree', path] })
      onCreated?.(entry)
      onClose()
    },
    onError: (error) => setFailure(explain(error, 'The folder could not be created.')),
  })

  const submit = () => {
    if (invalid || create.isPending) return
    setFailure('')
    create.mutate()
  }

  return (
    <Modal
      open={open}
      onClose={onClose}
      icon="folder-plus"
      title="New folder"
      width={440}
      footer={
        <>
          <Button onClick={onClose} disabled={create.isPending}>
            Cancel
          </Button>
          <Button variant="primary" onClick={submit} loading={create.isPending} disabled={invalid !== null}>
            Create folder
          </Button>
        </>
      }
    >
      <div ref={wrap}>
        <Field
          label="Name"
          value={name}
          onChange={(event) => {
            setName(event.target.value)
            setFailure('')
          }}
          onKeyDown={(event) => {
            if (event.key === 'Enter') {
              event.preventDefault()
              submit()
            }
          }}
          error={name.trim() && invalid ? invalid : undefined}
          hint={`It will be created in ${truncateMiddle(path || '/', 42)}`}
          spellCheck={false}
          autoComplete="off"
        />
      </div>
      <ErrorNote message={failure} />
    </Modal>
  )
}

// ---- new file ---------------------------------------------------------------

const FILE_SUGGESTIONS = ['.txt', '.md', '.json', '.sh', '.conf']

export function NewFileDialog({ open, path, onClose, onCreated }: CreateDialogProps) {
  const client = useQueryClient()
  const [name, setName] = useState('New file.txt')
  const [failure, setFailure] = useState('')
  const wrap = useNameField(open, baseSelection('New file.txt'))

  useEffect(() => {
    if (!open) return
    setName('New file.txt')
    setFailure('')
  }, [open])

  const invalid = nameError(name)

  const create = useMutation({
    mutationFn: () => api.touch(path, name.trim()),
    onSuccess: (entry) => {
      void client.invalidateQueries({ queryKey: ['list', path] })
      onCreated?.(entry)
      onClose()
    },
    onError: (error) => setFailure(explain(error, 'The file could not be created.')),
  })

  const submit = () => {
    if (invalid || create.isPending) return
    setFailure('')
    create.mutate()
  }

  const applySuggestion = (suffix: string) => {
    setFailure('')
    setName((current) => {
      const trimmed = current.trim()
      const stripped = FILE_SUGGESTIONS.some((item) => trimmed.toLowerCase().endsWith(item))
        ? trimmed.slice(0, trimmed.lastIndexOf('.'))
        : trimmed
      return (stripped || 'New file') + suffix
    })
  }

  return (
    <Modal
      open={open}
      onClose={onClose}
      icon="file-plus"
      title="New file"
      width={440}
      footer={
        <>
          <Button onClick={onClose} disabled={create.isPending}>
            Cancel
          </Button>
          <Button variant="primary" onClick={submit} loading={create.isPending} disabled={invalid !== null}>
            Create file
          </Button>
        </>
      }
    >
      <div ref={wrap}>
        <Field
          label="Name"
          value={name}
          onChange={(event) => {
            setName(event.target.value)
            setFailure('')
          }}
          onKeyDown={(event) => {
            if (event.key === 'Enter') {
              event.preventDefault()
              submit()
            }
          }}
          error={name.trim() && invalid ? invalid : undefined}
          hint={`It will be created in ${truncateMiddle(path || '/', 42)}`}
          spellCheck={false}
          autoComplete="off"
        />
      </div>

      <div className="mt-3 flex flex-wrap items-center gap-1.5">
        <span className="mr-1 text-xs text-faint">Common types</span>
        {FILE_SUGGESTIONS.map((suffix) => (
          <button
            key={suffix}
            type="button"
            className="sx-chip hover:text-ink"
            onClick={() => applySuggestion(suffix)}
          >
            {suffix}
          </button>
        ))}
      </div>

      <ErrorNote message={failure} />
    </Modal>
  )
}

// ---- rename -----------------------------------------------------------------

export function RenameDialog({
  open,
  entry,
  onClose,
  onDone,
}: {
  open: boolean
  entry: Entry | null
  onClose: () => void
  onDone?: (entry: Entry) => void
}) {
  const client = useQueryClient()
  const [name, setName] = useState('')
  const [failure, setFailure] = useState('')
  const original = entry?.name ?? ''
  const selection = useMemo<[number, number]>(() => baseSelection(original), [original])
  const wrap = useNameField(open && entry !== null, selection)

  useEffect(() => {
    if (!open || !original) return
    setName(original)
    setFailure('')
  }, [open, original])

  const invalid = nameError(name)
  const unchanged = name.trim() === original
  const parent = entry ? parentPath(entry.path) : ''
  const extensionChanged =
    !entry?.isDir && !unchanged && !invalid && extensionOfName(original) !== extensionOfName(name.trim())

  const rename = useMutation({
    mutationFn: () => {
      if (!entry) throw new Error('Nothing selected.')
      return api.rename(entry.path, name.trim())
    },
    onSuccess: (renamed) => {
      void client.invalidateQueries({ queryKey: ['list', parent] })
      void client.invalidateQueries({ queryKey: ['favorites'] })
      void client.invalidateQueries({ queryKey: ['recent'] })
      onDone?.(renamed)
      onClose()
    },
    onError: (error) => setFailure(explain(error, 'The item could not be renamed.')),
  })

  const submit = () => {
    if (invalid || unchanged || rename.isPending) return
    setFailure('')
    rename.mutate()
  }

  return (
    <Modal
      open={open && entry !== null}
      onClose={onClose}
      icon="edit"
      title={entry?.isDir ? 'Rename folder' : 'Rename file'}
      width={440}
      footer={
        <>
          <Button onClick={onClose} disabled={rename.isPending}>
            Cancel
          </Button>
          <Button
            variant="primary"
            onClick={submit}
            loading={rename.isPending}
            disabled={invalid !== null || unchanged}
          >
            Rename
          </Button>
        </>
      }
    >
      <div ref={wrap}>
        <Field
          label="Name"
          value={name}
          onChange={(event) => {
            setName(event.target.value)
            setFailure('')
          }}
          onKeyDown={(event) => {
            if (event.key === 'Enter') {
              event.preventDefault()
              submit()
            }
          }}
          error={name.trim() && invalid ? invalid : undefined}
          hint={parent ? `In ${truncateMiddle(parent, 42)}` : undefined}
          spellCheck={false}
          autoComplete="off"
        />
      </div>

      {extensionChanged && (
        <InfoNote>
          You changed the file type from
          {extensionOfName(original) ? ` .${extensionOfName(original)}` : ' none'} to
          {extensionOfName(name.trim()) ? ` .${extensionOfName(name.trim())}` : ' none'}. The file may stop opening in
          the app you normally use.
        </InfoNote>
      )}

      <ErrorNote message={failure} />
    </Modal>
  )
}

// ---- folder picker ----------------------------------------------------------

const MOUNT_ICONS: Record<string, IconName> = {
  archive: 'archive',
  code: 'code',
  database: 'database',
  drive: 'drive',
  folder: 'folder',
  globe: 'globe',
  home: 'home',
  image: 'image',
  lock: 'lock',
  music: 'music',
  server: 'server',
  star: 'star',
  user: 'user',
  users: 'users',
  video: 'video',
}

function mountIcon(name: string): IconName {
  return MOUNT_ICONS[name] ?? 'drive'
}

/** ancestorChain lists every folder from the root down to a path. */
function ancestorChain(path: string): string[] {
  if (!path || path === '/') return ['/']
  const chain: string[] = ['/']
  let accumulated = ''
  for (const part of path.split('/').filter(Boolean)) {
    accumulated += '/' + part
    chain.push(accumulated)
  }
  return chain
}

interface TreeRowProps {
  path: string
  label: string
  icon: IconName
  depth: number
  expanded: Set<string>
  selected: string
  hasChildren: boolean
  onSelect: (path: string) => void
  onToggle: (path: string) => void
}

function TreeRow({ path, label, icon, depth, expanded, selected, hasChildren, onSelect, onToggle }: TreeRowProps) {
  const isOpen = expanded.has(path)
  const branch = useQuery({
    queryKey: ['tree', path],
    queryFn: () => api.tree(path, 1),
    enabled: isOpen,
    staleTime: 15_000,
  })

  const children = branch.data?.children ?? []
  const isSelected = selected === path
  const indent = 6 + depth * 15

  return (
    <div>
      <div
        data-selected={isSelected ? 'true' : undefined}
        className="group flex h-9 items-center gap-1 rounded-xl pr-2 text-sm text-muted transition-colors hover:bg-elevated hover:text-ink data-[selected=true]:bg-primary/15 data-[selected=true]:text-ink"
        style={{ paddingLeft: indent }}
      >
        {hasChildren ? (
          <button
            type="button"
            aria-label={isOpen ? `Collapse ${label}` : `Expand ${label}`}
            className="flex h-6 w-6 shrink-0 items-center justify-center rounded-lg text-faint transition-colors hover:text-ink"
            onClick={() => onToggle(path)}
          >
            {isOpen && branch.isFetching ? (
              <Spinner size={13} className="text-primary" />
            ) : (
              <Icon name={isOpen ? 'chevron-down' : 'chevron-right'} size={15} />
            )}
          </button>
        ) : (
          <span className="h-6 w-6 shrink-0" />
        )}

        <button
          type="button"
          className="flex min-w-0 flex-1 items-center gap-2 text-left"
          onClick={() => {
            onSelect(path)
            if (hasChildren && !isOpen) onToggle(path)
          }}
        >
          <Icon name={icon} size={16} className={clsx('shrink-0', isSelected ? 'text-primary' : 'text-faint')} />
          <span className="truncate">{label}</span>
        </button>
      </div>

      {isOpen && (
        <div>
          {branch.isError ? (
            <p className="py-1.5 text-xs text-danger" style={{ paddingLeft: indent + 30 }}>
              This folder could not be opened.
            </p>
          ) : branch.isLoading ? (
            <div className="space-y-1.5 py-1.5" style={{ paddingLeft: indent + 30 }}>
              <Skeleton className="h-3.5 w-40" />
              <Skeleton className="h-3.5 w-28" />
            </div>
          ) : children.length === 0 ? (
            <p className="py-1.5 text-xs text-faint" style={{ paddingLeft: indent + 30 }}>
              No folders inside
            </p>
          ) : (
            children.map((child) => (
              <TreeRow
                key={child.path}
                path={child.path}
                label={child.name}
                icon="folder"
                depth={depth + 1}
                expanded={expanded}
                selected={selected}
                hasChildren={child.hasChildren}
                onSelect={onSelect}
                onToggle={onToggle}
              />
            ))
          )}
        </div>
      )}
    </div>
  )
}

export function PathPickerDialog({
  open,
  title,
  confirmLabel = 'Choose folder',
  initialPath,
  onClose,
  onPick,
}: {
  open: boolean
  title: string
  confirmLabel?: string
  initialPath?: string
  onClose: () => void
  onPick: (path: string) => void
}) {
  const { me } = useSession()
  const client = useQueryClient()
  const [selected, setSelected] = useState('')
  const [expanded, setExpanded] = useState<Set<string>>(new Set())
  const [creating, setCreating] = useState(false)
  const [newName, setNewName] = useState('New folder')
  const [failure, setFailure] = useState('')

  const roots = useMemo(() => me?.mounts ?? [], [me])
  const start = useRef({ initialPath, roots })
  start.current = { initialPath, roots }

  useEffect(() => {
    if (!open) return
    const first = start.current.initialPath || start.current.roots[0]?.path || ''
    setSelected(first)
    setExpanded(new Set(first ? ancestorChain(first) : []))
    setCreating(false)
    setNewName('New folder')
    setFailure('')
  }, [open])

  const toggle = (path: string) => {
    setExpanded((current) => {
      const next = new Set(current)
      if (next.has(path)) next.delete(path)
      else next.add(path)
      return next
    })
  }

  const invalidName = nameError(newName)

  const create = useMutation({
    mutationFn: () => api.mkdir(selected, newName.trim()),
    onSuccess: (entry) => {
      void client.invalidateQueries({ queryKey: ['tree', selected] })
      void client.invalidateQueries({ queryKey: ['list', selected] })
      setExpanded((current) => new Set(current).add(selected))
      setSelected(entry.path)
      setCreating(false)
      setNewName('New folder')
    },
    onError: (error) => setFailure(explain(error, 'The folder could not be created.')),
  })

  const crumbs = selected ? ancestorChain(selected) : []

  return (
    <Modal
      open={open}
      onClose={onClose}
      icon="folder-open"
      title={title}
      description="Pick where this should go."
      width={560}
      footer={
        <>
          <Button onClick={onClose}>Cancel</Button>
          <Button
            variant="primary"
            disabled={!selected}
            onClick={() => {
              onPick(selected)
              onClose()
            }}
          >
            {confirmLabel}
          </Button>
        </>
      }
    >
      {roots.length === 0 ? (
        <EmptyState
          icon="drive"
          title="No folders available"
          message="Your account does not have access to any folder yet. Ask an administrator to add one."
        />
      ) : (
        <>
          <div className="sx-scroll max-h-[320px] rounded-xl border border-line bg-elevated/40 p-1.5">
            {roots.map((mount) => (
              <TreeRow
                key={mount.path}
                path={mount.path}
                label={mount.label || baseName(mount.path) || mount.path}
                icon={mountIcon(mount.icon)}
                depth={0}
                expanded={expanded}
                selected={selected}
                hasChildren
                onSelect={setSelected}
                onToggle={toggle}
              />
            ))}
          </div>

          <div className="mt-3 flex items-center gap-2">
            <div className="sx-chip min-w-0 flex-1 justify-start overflow-hidden">
              <Icon name="folder" size={14} className="shrink-0 text-primary" />
              {selected ? (
                <span className="flex min-w-0 items-center gap-1 truncate">
                  {crumbs.map((crumb, index) => (
                    <span key={crumb} className="flex shrink-0 items-center gap-1">
                      {index > 0 && <Icon name="chevron-right" size={11} className="text-faint" />}
                      <span className={index === crumbs.length - 1 ? 'text-ink' : undefined}>
                        {index === 0 ? 'Server' : baseName(crumb)}
                      </span>
                    </span>
                  ))}
                </span>
              ) : (
                <span>No folder chosen yet</span>
              )}
            </div>
            <Button
              icon="folder-plus"
              onClick={() => {
                setFailure('')
                setCreating(true)
              }}
              disabled={!selected || creating}
            >
              New folder
            </Button>
          </div>

          {creating && (
            <div className="mt-3 rounded-xl border border-line bg-elevated p-3">
              <label className="sx-label">New folder in {baseName(selected) || 'the chosen folder'}</label>
              <div className="flex items-start gap-2">
                <Field
                  className="flex-1"
                  value={newName}
                  autoFocus
                  spellCheck={false}
                  autoComplete="off"
                  onChange={(event) => {
                    setNewName(event.target.value)
                    setFailure('')
                  }}
                  onKeyDown={(event) => {
                    if (event.key === 'Enter') {
                      event.preventDefault()
                      if (!invalidName && !create.isPending) create.mutate()
                    }
                  }}
                  error={newName.trim() && invalidName ? invalidName : undefined}
                />
                <Button
                  variant="primary"
                  onClick={() => create.mutate()}
                  loading={create.isPending}
                  disabled={invalidName !== null}
                >
                  Create
                </Button>
                <Button variant="ghost" onClick={() => setCreating(false)} disabled={create.isPending}>
                  Cancel
                </Button>
              </div>
            </div>
          )}
        </>
      )}

      <ErrorNote message={failure} />
    </Modal>
  )
}

// ---- compress ---------------------------------------------------------------

const ARCHIVE_SUFFIX = /\.(tar\.gz|tgz|zip|tar|gz)$/i

interface FormatOption {
  id: string
  title: string
  description: string
  suffix: string
}

const FORMATS: FormatOption[] = [
  { id: 'zip', title: 'Zip', description: 'Opens on any computer without extra software.', suffix: '.zip' },
  {
    id: 'tar.gz',
    title: 'Compressed tar',
    description: 'Smaller result and the usual choice on Linux servers.',
    suffix: '.tar.gz',
  },
  { id: 'tar', title: 'Tar', description: 'No compression, quickest to create.', suffix: '.tar' },
]

export function CompressDialog({
  open,
  sources,
  dest,
  onClose,
  onStarted,
}: {
  open: boolean
  sources: string[]
  dest: string
  onClose: () => void
  onStarted?: (job: Job) => void
}) {
  const client = useQueryClient()
  const toast = useToast()
  const [name, setName] = useState('archive')
  const [format, setFormat] = useState('zip')
  const [destination, setDestination] = useState(dest)
  const [picking, setPicking] = useState(false)
  const [failure, setFailure] = useState('')
  const wrap = useNameField(open && !picking)
  const incoming = useRef({ sources, dest })
  incoming.current = { sources, dest }

  useEffect(() => {
    if (!open) return
    const { sources: items, dest: target } = incoming.current
    const suggested =
      items.length === 1 ? baseName(items[0] ?? '').replace(ARCHIVE_SUFFIX, '') : baseName(target) || 'archive'
    setName(suggested || 'archive')
    setFormat('zip')
    setDestination(target)
    setPicking(false)
    setFailure('')
  }, [open])

  const invalid = nameError(name)
  const option = FORMATS.find((item) => item.id === format) ?? FORMATS[0]
  const finalName = name.trim().replace(ARCHIVE_SUFFIX, '') + option.suffix

  const compress = useMutation({
    mutationFn: () =>
      api.compress({
        sources,
        dest: destination,
        name: name.trim().replace(ARCHIVE_SUFFIX, ''),
        format,
      }),
    onSuccess: (job) => {
      void client.invalidateQueries({ queryKey: ['list', destination] })
      void client.invalidateQueries({ queryKey: ['jobs'] })
      toast.info('Creating the archive', 'Progress is shown in the transfer panel.')
      onStarted?.(job)
      onClose()
    },
    onError: (error) => setFailure(explain(error, 'The archive could not be started.')),
  })

  const submit = () => {
    if (invalid || compress.isPending || sources.length === 0) return
    setFailure('')
    compress.mutate()
  }

  return (
    <>
      <Modal
        open={open && !picking}
        onClose={onClose}
        icon="archive"
        title="Create archive"
        description={
          sources.length === 1
            ? `Packing ${truncateMiddle(baseName(sources[0] ?? ''), 40)}`
            : `Packing ${counted(sources.length, 'item')}`
        }
        width={520}
        footer={
          <>
            <Button onClick={onClose} disabled={compress.isPending}>
              Cancel
            </Button>
            <Button
              variant="primary"
              onClick={submit}
              loading={compress.isPending}
              disabled={invalid !== null || sources.length === 0}
            >
              Create archive
            </Button>
          </>
        }
      >
        <div ref={wrap}>
          <Field
            label="Archive name"
            value={name}
            onChange={(event) => {
              setName(event.target.value)
              setFailure('')
            }}
            onKeyDown={(event) => {
              if (event.key === 'Enter') {
                event.preventDefault()
                submit()
              }
            }}
            error={name.trim() && invalid ? invalid : undefined}
            hint={invalid ? undefined : `Saved as ${truncateMiddle(finalName, 44)}`}
            spellCheck={false}
            autoComplete="off"
          />
        </div>

        <div className="mt-4">
          <span className="sx-label">Format</span>
          <div className="space-y-2">
            {FORMATS.map((item) => (
              <RadioCard
                key={item.id}
                group="storix-compress-format"
                value={item.id}
                current={format}
                onChange={setFormat}
                title={`${item.title} (${item.suffix})`}
                description={item.description}
              />
            ))}
          </div>
        </div>

        <div className="mt-4">
          <span className="sx-label">Save in</span>
          <div className="flex items-center gap-2">
            <div className="sx-input flex min-w-0 items-center gap-2 text-muted">
              <Icon name="folder" size={15} className="shrink-0 text-faint" />
              <span className="truncate">{truncateMiddle(destination || '/', 44)}</span>
            </div>
            <Button onClick={() => setPicking(true)} disabled={compress.isPending}>
              Change
            </Button>
          </div>
        </div>

        <ErrorNote message={failure} />
      </Modal>

      <PathPickerDialog
        open={open && picking}
        title="Save the archive in"
        confirmLabel="Save here"
        initialPath={destination}
        onClose={() => setPicking(false)}
        onPick={(path) => setDestination(path)}
      />
    </>
  )
}

// ---- extract ----------------------------------------------------------------

export function ExtractDialog({
  open,
  path,
  onClose,
  onStarted,
}: {
  open: boolean
  path: string
  onClose: () => void
  onStarted?: (job: Job) => void
}) {
  const client = useQueryClient()
  const toast = useToast()
  const [choice, setChoice] = useState('new')
  const [chosen, setChosen] = useState('')
  const [picking, setPicking] = useState(false)
  const [failure, setFailure] = useState('')

  const parent = parentPath(path)
  const folderName = baseName(path).replace(ARCHIVE_SUFFIX, '') || 'extracted'

  useEffect(() => {
    if (!open) return
    setChoice('new')
    setChosen(parentPath(path))
    setPicking(false)
    setFailure('')
  }, [open, path])

  const preview = useQuery({
    queryKey: ['archive', path],
    queryFn: () => api.archivePreview(path, 50),
    enabled: open && path !== '',
    staleTime: 60_000,
  })

  const destination = choice === 'here' ? parent : choice === 'new' ? joinPath(parent, folderName) : chosen

  const extract = useMutation({
    mutationFn: async () => {
      if (choice === 'new') {
        try {
          await api.mkdir(parent, folderName)
        } catch {
          // The folder may already be there, which is fine. If it is not usable
          // the extract call below reports the real reason.
        }
      }
      return api.extract(path, destination)
    },
    onSuccess: (job) => {
      void client.invalidateQueries({ queryKey: ['list', parent] })
      void client.invalidateQueries({ queryKey: ['list', destination] })
      void client.invalidateQueries({ queryKey: ['jobs'] })
      toast.info('Extracting the archive', 'Progress is shown in the transfer panel.')
      onStarted?.(job)
      onClose()
    },
    onError: (error) => setFailure(explain(error, 'The archive could not be extracted.')),
  })

  const items = preview.data?.items ?? []

  return (
    <>
      <Modal
        open={open && !picking}
        onClose={onClose}
        icon="archive"
        title="Extract archive"
        description={truncateMiddle(baseName(path), 44)}
        width={520}
        footer={
          <>
            <Button onClick={onClose} disabled={extract.isPending}>
              Cancel
            </Button>
            <Button
              variant="primary"
              onClick={() => {
                setFailure('')
                extract.mutate()
              }}
              loading={extract.isPending}
              disabled={!destination}
            >
              Extract
            </Button>
          </>
        }
      >
        <div>
          <span className="sx-label">Inside this archive</span>
          <div className="sx-scroll max-h-[172px] rounded-xl border border-line bg-elevated/40 p-2">
            {preview.isLoading ? (
              <div className="space-y-2 p-1">
                <Skeleton className="h-4 w-3/4" />
                <Skeleton className="h-4 w-1/2" />
                <Skeleton className="h-4 w-2/3" />
              </div>
            ) : preview.isError ? (
              <p className="px-1 py-3 text-center text-xs text-danger">
                {explain(preview.error, 'The contents could not be read.')}
              </p>
            ) : items.length === 0 ? (
              <p className="px-1 py-5 text-center text-xs text-faint">This archive is empty.</p>
            ) : (
              items.map((item) => (
                <div key={item.name} className="flex h-7 items-center gap-2 px-1 text-xs text-muted">
                  <Icon
                    name={item.isDir ? 'folder' : 'file'}
                    size={14}
                    className={clsx('shrink-0', item.isDir ? 'text-primary' : 'text-faint')}
                  />
                  <span className="min-w-0 flex-1 truncate">{item.name}</span>
                  {!item.isDir && <span className="shrink-0 tabular-nums text-faint">{bytes(item.size)}</span>}
                </div>
              ))
            )}
          </div>
          {!preview.isLoading && !preview.isError && items.length > 0 && (
            <p className="mt-1.5 text-xs text-faint">
              {preview.data?.truncated
                ? `Showing the first ${counted(items.length, 'item')}, there are more inside.`
                : counted(items.length, 'item')}
              {preview.data?.format ? ` in ${preview.data.format} format` : ''}
            </p>
          )}
        </div>

        <div className="mt-4">
          <span className="sx-label">Where to put the contents</span>
          <div className="space-y-2">
            <RadioCard
              group="storix-extract-target"
              value="new"
              current={choice}
              onChange={setChoice}
              title={`Into a new folder named ${folderName}`}
              description="Keeps this folder tidy if the archive holds many files."
            />
            <RadioCard
              group="storix-extract-target"
              value="here"
              current={choice}
              onChange={setChoice}
              title="Here"
              description={`Straight into ${truncateMiddle(parent || '/', 38)}`}
            />
            <RadioCard
              group="storix-extract-target"
              value="choose"
              current={choice}
              onChange={setChoice}
              title="Another folder"
              description={choice === 'choose' && chosen ? truncateMiddle(chosen, 38) : 'Pick any folder you can write to.'}
            />
          </div>
          {choice === 'choose' && (
            <div className="mt-2 flex items-center gap-2">
              <div className="sx-input flex min-w-0 items-center gap-2 text-muted">
                <Icon name="folder" size={15} className="shrink-0 text-faint" />
                <span className="truncate">{truncateMiddle(chosen || '/', 44)}</span>
              </div>
              <Button onClick={() => setPicking(true)} disabled={extract.isPending}>
                Change
              </Button>
            </div>
          )}
        </div>

        <ErrorNote message={failure} />
      </Modal>

      <PathPickerDialog
        open={open && picking}
        title="Extract into"
        confirmLabel="Extract here"
        initialPath={chosen || parent}
        onClose={() => setPicking(false)}
        onPick={(target) => setChosen(target)}
      />
    </>
  )
}

// ---- properties -------------------------------------------------------------

interface ModeParts {
  special: string
  perms: string
}

/** splitMode separates the rarely used leading digit from the nine permission bits. */
function splitMode(octal: string): ModeParts {
  const digits = (octal || '').replace(/[^0-7]/g, '')
  if (!digits) return { special: '', perms: '000' }
  const perms = digits.length >= 3 ? digits.slice(-3) : digits.padStart(3, '0')
  const leading = digits.length > 3 ? digits.slice(0, digits.length - 3).replace(/^0+/, '') : ''
  return { special: leading, perms }
}

function joinMode(parts: ModeParts): string {
  return parts.special ? parts.special + parts.perms : parts.perms
}

function setBit(perms: string, index: number, bit: number, on: boolean): string {
  const digits = perms.split('').map((digit) => Number(digit) || 0)
  digits[index] = on ? digits[index] | bit : digits[index] & ~bit
  return digits.join('')
}

const ACCESS_PRESETS: Record<string, { file: string; folder: string }> = {
  me: { file: '600', folder: '700' },
  team: { file: '640', folder: '750' },
  everyone: { file: '644', folder: '755' },
}

/** PermBox is the checkbox of the advanced grid, with a name a reader can hear. */
function PermBox({ checked, onChange, label }: { checked: boolean; onChange: (value: boolean) => void; label: string }) {
  return (
    <label className="flex cursor-pointer items-center justify-center">
      <input
        type="checkbox"
        className="peer sr-only"
        checked={checked}
        aria-label={label}
        onChange={(event) => onChange(event.target.checked)}
      />
      <span
        className={clsx(
          'flex h-[18px] w-[18px] items-center justify-center rounded-[6px] border transition-colors',
          'peer-focus-visible:ring-2 peer-focus-visible:ring-primary/70',
          checked ? 'border-primary bg-primary text-white' : 'border-line bg-elevated',
        )}
      >
        {checked && <Icon name="check" size={12} strokeWidth={3} />}
      </span>
    </label>
  )
}

export function PropertiesDialog({
  open,
  entry,
  onClose,
  onChanged,
}: {
  open: boolean
  entry: Entry | null
  onClose: () => void
  onChanged?: () => void
}) {
  const client = useQueryClient()
  const { me, isAdmin } = useSession()
  const advancedAllowed = me?.features.advanced === true

  const [octal, setOctal] = useState('644')
  const [owner, setOwner] = useState('')
  const [group, setGroup] = useState('')
  const [recursive, setRecursive] = useState(false)
  const [showAdvanced, setShowAdvanced] = useState(false)
  const [failure, setFailure] = useState('')

  const originalMode = entry ? joinMode(splitMode(entry.modeOctal)) : ''
  const subject = useRef(entry)
  subject.current = entry
  const subjectPath = entry?.path ?? ''

  useEffect(() => {
    const item = subject.current
    if (!open || !item) return
    setOctal(joinMode(splitMode(item.modeOctal)))
    setOwner(item.owner)
    setGroup(item.group)
    setRecursive(false)
    setShowAdvanced(false)
    setFailure('')
  }, [open, subjectPath])

  const parts = splitMode(octal)
  const validOctal = /^[0-7]{3,4}$/.test(octal)
  const isDir = entry?.isDir === true

  const access = useMemo(() => {
    for (const [id, preset] of Object.entries(ACCESS_PRESETS)) {
      if (parts.perms === (isDir ? preset.folder : preset.file)) return id
    }
    return 'custom'
  }, [parts.perms, isDir])

  const applyAccess = (id: string) => {
    const preset = ACCESS_PRESETS[id]
    if (!preset) return
    setFailure('')
    setOctal(joinMode({ special: parts.special, perms: isDir ? preset.folder : preset.file }))
  }

  const modeChanged = validOctal && joinMode(parts) !== originalMode
  const ownerChanged = isAdmin && entry !== null && (owner.trim() !== entry.owner || group.trim() !== entry.group)
  const dirty = modeChanged || ownerChanged
  const readOnly = entry?.readOnly === true

  const save = useMutation({
    mutationFn: async () => {
      if (!entry) throw new Error('Nothing selected.')
      if (modeChanged) await api.chmod(entry.path, joinMode(parts), recursive)
      if (ownerChanged) await api.chown(entry.path, owner.trim(), group.trim(), recursive)
    },
    onSuccess: () => {
      if (entry) {
        void client.invalidateQueries({ queryKey: ['list', parentPath(entry.path)] })
        void client.invalidateQueries({ queryKey: ['stat', entry.path] })
      }
      onChanged?.()
      onClose()
    },
    onError: (error) => setFailure(explain(error, 'The change could not be saved.')),
  })

  const who = [
    { label: 'Owner', hint: entry?.owner ?? '' },
    { label: 'Group', hint: entry?.group ?? '' },
    { label: 'Everyone else', hint: '' },
  ]
  const what = [
    { bit: 4, label: 'Read' },
    { bit: 2, label: 'Write' },
    { bit: 1, label: isDir ? 'Enter' : 'Run' },
  ]

  const summaries: Record<string, { title: string; description: string }> = {
    me: {
      title: 'Only me',
      description: isDir
        ? 'You can open and change this folder. Nobody else has access.'
        : 'You can read and change this file. Nobody else has access.',
    },
    team: {
      title: 'My team',
      description: isDir
        ? `Anyone in the ${entry?.group || 'group'} group can open it. Only you can change it.`
        : `Anyone in the ${entry?.group || 'group'} group can read it. Only you can change it.`,
    },
    everyone: {
      title: 'Everyone',
      description: isDir
        ? 'Any account on this server can open it. Only you can change it.'
        : 'Any account on this server can read it. Only you can change it.',
    },
  }

  return (
    <Modal
      open={open && entry !== null}
      onClose={onClose}
      icon="shield"
      title={entry ? truncateMiddle(entry.name, 38) : 'Properties'}
      width={560}
      footer={
        <>
          <Button onClick={onClose} disabled={save.isPending}>
            Close
          </Button>
          <Button
            variant="primary"
            onClick={() => {
              setFailure('')
              save.mutate()
            }}
            loading={save.isPending}
            disabled={!dirty || !validOctal || readOnly}
          >
            Save changes
          </Button>
        </>
      }
    >
      {entry && (
        <>
          <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1.5 rounded-xl border border-line bg-elevated px-3 py-2.5 text-xs">
            <dt className="text-faint">Where</dt>
            <dd className="min-w-0 truncate text-muted">{truncateMiddle(parentPath(entry.path) || '/', 48)}</dd>
            <dt className="text-faint">Size</dt>
            <dd className="text-muted">{entry.isDir ? 'Folder' : bytes(entry.size)}</dd>
            <dt className="text-faint">Changed</dt>
            <dd className="text-muted">{smartDate(entry.modified)}</dd>
          </dl>

          <div className="mt-4">
            <span className="sx-label">Who can access this</span>
            <div className="space-y-2">
              {(['me', 'team', 'everyone'] as const).map((id) => (
                <RadioCard
                  key={id}
                  group="storix-access"
                  value={id}
                  current={access}
                  onChange={applyAccess}
                  title={summaries[id].title}
                  description={summaries[id].description}
                  disabled={readOnly}
                />
              ))}
            </div>
            {access === 'custom' && (
              <InfoNote>
                This item uses a custom setting right now ({joinMode(parts)}). Picking one of the options above replaces
                it.
              </InfoNote>
            )}
            {readOnly && <InfoNote>This item is in a read only location, so it cannot be changed.</InfoNote>}
          </div>

          {advancedAllowed && (
            <div className="mt-4 border-t border-line pt-3">
              <button
                type="button"
                className="flex w-full items-center gap-2 text-sm text-muted transition-colors hover:text-ink"
                aria-expanded={showAdvanced}
                onClick={() => setShowAdvanced((value) => !value)}
              >
                <Icon name={showAdvanced ? 'chevron-down' : 'chevron-right'} size={15} />
                Advanced
              </button>

              {showAdvanced && (
                <div className="mt-3 space-y-4">
                  <div>
                    <span className="sx-label">Permissions</span>
                    <div className="rounded-xl border border-line bg-elevated px-3 py-2.5">
                      <div className="grid grid-cols-[1fr_repeat(3,56px)] items-center gap-y-2">
                        <span />
                        {what.map((column) => (
                          <span key={column.label} className="text-center text-[11px] uppercase tracking-wide text-faint">
                            {column.label}
                          </span>
                        ))}
                        {who.map((row, rowIndex) => (
                          <Fragment key={row.label}>
                            <span className="min-w-0 truncate text-sm text-ink">
                              {row.label}
                              {row.hint && <span className="ml-1.5 text-xs text-faint">{row.hint}</span>}
                            </span>
                            {what.map((column) => {
                              const digit = Number(parts.perms[rowIndex]) || 0
                              const checked = (digit & column.bit) !== 0
                              return (
                                <PermBox
                                  key={column.label}
                                  checked={checked}
                                  label={`${row.label} can ${column.label.toLowerCase()}`}
                                  onChange={(value) =>
                                    setOctal(
                                      joinMode({
                                        special: parts.special,
                                        perms: setBit(parts.perms, rowIndex, column.bit, value),
                                      }),
                                    )
                                  }
                                />
                              )
                            })}
                          </Fragment>
                        ))}
                      </div>
                    </div>
                  </div>

                  <div>
                    <label className="sx-label" htmlFor="storix-octal">
                      Octal mode
                    </label>
                    <input
                      id="storix-octal"
                      className={clsx('sx-input font-mono', !validOctal && 'border-danger/60')}
                      value={octal}
                      inputMode="numeric"
                      spellCheck={false}
                      autoComplete="off"
                      maxLength={4}
                      onChange={(event) => {
                        setOctal(event.target.value.replace(/[^0-7]/g, ''))
                        setFailure('')
                      }}
                    />
                    <p className={clsx('mt-1.5 text-xs', validOctal ? 'text-faint' : 'text-danger')}>
                      {validOctal ? modeToText(parts.perms) : 'Enter three or four digits from 0 to 7.'}
                    </p>
                  </div>

                  {isAdmin && (
                    <div className="grid grid-cols-2 gap-3">
                      <Field
                        label="Owner"
                        value={owner}
                        spellCheck={false}
                        autoComplete="off"
                        onChange={(event) => {
                          setOwner(event.target.value)
                          setFailure('')
                        }}
                      />
                      <Field
                        label="Group"
                        value={group}
                        spellCheck={false}
                        autoComplete="off"
                        onChange={(event) => {
                          setGroup(event.target.value)
                          setFailure('')
                        }}
                      />
                    </div>
                  )}

                  {entry.isDir && (
                    <Toggle
                      checked={recursive}
                      onChange={setRecursive}
                      label="Apply to everything inside"
                      hint="Every file and folder below this one gets the same settings."
                    />
                  )}
                </div>
              )}
            </div>
          )}

          <ErrorNote message={failure} />
        </>
      )}
    </Modal>
  )
}

// ---- delete -----------------------------------------------------------------

export function DeleteDialog({
  open,
  entries,
  onClose,
  onDone,
}: {
  open: boolean
  entries: Entry[]
  onClose: () => void
  onDone?: () => void
}) {
  const client = useQueryClient()
  const [permanent, setPermanent] = useState(false)
  const [failure, setFailure] = useState('')

  useEffect(() => {
    if (!open) return
    setPermanent(false)
    setFailure('')
  }, [open])

  const single = entries.length === 1 ? entries[0] : null
  const label = single ? truncateMiddle(single.name, 40) : counted(entries.length, 'item')

  const remove = useMutation({
    mutationFn: () => api.remove(entries.map((item) => item.path), permanent),
    onSuccess: () => {
      const parents = new Set(entries.map((item) => parentPath(item.path)))
      for (const parent of parents) void client.invalidateQueries({ queryKey: ['list', parent] })
      void client.invalidateQueries({ queryKey: ['trash'] })
      void client.invalidateQueries({ queryKey: ['dashboard'] })
      void client.invalidateQueries({ queryKey: ['favorites'] })
      void client.invalidateQueries({ queryKey: ['recent'] })
      void client.invalidateQueries({ queryKey: ['jobs'] })
      onDone?.()
      onClose()
    },
    onError: (error) => setFailure(explain(error, 'The items could not be deleted.')),
  })

  return (
    <Modal
      open={open && entries.length > 0}
      onClose={onClose}
      icon={permanent ? 'alert' : 'trash'}
      title={permanent ? 'Delete permanently' : 'Move to the recycle bin'}
      width={440}
      footer={
        <>
          <Button onClick={onClose} disabled={remove.isPending}>
            Cancel
          </Button>
          <Button
            variant={permanent ? 'danger' : 'primary'}
            onClick={() => {
              setFailure('')
              remove.mutate()
            }}
            loading={remove.isPending}
          >
            {permanent ? 'Delete permanently' : 'Move to recycle bin'}
          </Button>
        </>
      }
    >
      <p className="text-sm text-muted">
        {permanent ? (
          <>
            <span className="text-ink">{label}</span> will be removed from the server straight away.
          </>
        ) : (
          <>
            <span className="text-ink">{label}</span> will go to the recycle bin, where you can restore it later.
          </>
        )}
      </p>

      {entries.length > 1 && (
        <div className="sx-scroll mt-3 max-h-28 rounded-xl border border-line bg-elevated/40 p-2">
          {entries.slice(0, 50).map((item) => (
            <div key={item.path} className="flex h-7 items-center gap-2 px-1 text-xs text-muted">
              <Icon
                name={item.isDir ? 'folder' : 'file'}
                size={14}
                className={clsx('shrink-0', item.isDir ? 'text-primary' : 'text-faint')}
              />
              <span className="min-w-0 flex-1 truncate">{item.name}</span>
            </div>
          ))}
          {entries.length > 50 && (
            <p className="px-1 pt-1 text-xs text-faint">and {counted(entries.length - 50, 'more item')}</p>
          )}
        </div>
      )}

      <div className="mt-4">
        <Checkbox checked={permanent} onChange={setPermanent} label="Delete permanently instead" />
      </div>

      {permanent && (
        <div className="mt-3 flex items-start gap-2.5 rounded-xl border border-danger/35 bg-danger/10 px-3 py-2.5 text-xs text-danger">
          <Icon name="alert" size={15} className="mt-0.5 shrink-0" />
          <span>This cannot be undone. Nothing is kept in the recycle bin.</span>
        </div>
      )}

      <ErrorNote message={failure} />
    </Modal>
  )
}
