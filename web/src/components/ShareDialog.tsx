// Create or edit a public link for one file or folder.
// Developed by X Project.

import clsx from 'clsx'
import { useEffect, useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ApiError, api } from '../lib/api'
import { baseName, truncateMiddle } from '../lib/format'
import type { Share, ShareKind } from '../lib/types'
import { Icon, colourForKind, iconForKind } from './Icon'
import { QRCode } from './QRCode'
import { Button, Field, IconButton, Modal, Select, Toggle, useToast } from './ui'

// ---- helpers shared with the links screen ------------------------------------

/** shareLink returns the address to hand out for a link. */
export function shareLink(share: Share): string {
  if (share.url) return share.url
  return `${window.location.origin}/s/${share.token}`
}

/**
 * copyText puts a string on the clipboard. The modern clipboard API is only
 * available on a secure origin, so a plain textarea keeps plain HTTP working.
 */
export async function copyText(text: string): Promise<boolean> {
  try {
    if (navigator.clipboard && window.isSecureContext) {
      await navigator.clipboard.writeText(text)
      return true
    }
  } catch {
    // Fall through to the manual path below.
  }
  try {
    const area = document.createElement('textarea')
    area.value = text
    area.setAttribute('readonly', 'true')
    area.style.position = 'fixed'
    area.style.opacity = '0'
    document.body.appendChild(area)
    area.select()
    const ok = document.execCommand('copy')
    document.body.removeChild(area)
    return ok
  } catch {
    return false
  }
}

// ---- expiry ------------------------------------------------------------------

const EXPIRY_WINDOWS: Record<string, number> = {
  '1h': 3600_000,
  '24h': 86_400_000,
  '7d': 7 * 86_400_000,
  '30d': 30 * 86_400_000,
  '90d': 90 * 86_400_000,
}

const EXPIRY_OPTIONS = [
  { value: '1h', label: '1 hour' },
  { value: '24h', label: '24 hours' },
  { value: '7d', label: '7 days' },
  { value: '30d', label: '30 days' },
  { value: '90d', label: '90 days' },
  { value: 'never', label: 'Never' },
]

/** longDate renders a calendar day the way the consequence line reads it. */
export function longDate(input: string | number | Date | undefined | null): string {
  if (!input) return ''
  const date = new Date(input)
  if (Number.isNaN(date.getTime())) return ''
  const sameYear = date.getFullYear() === new Date().getFullYear()
  return date.toLocaleDateString(undefined, {
    day: 'numeric',
    month: 'long',
    year: sameYear ? undefined : 'numeric',
  })
}

function expiryDate(value: string): Date | null {
  const window = EXPIRY_WINDOWS[value]
  if (!window) return null
  return new Date(Date.now() + window)
}

// ---- props -------------------------------------------------------------------

export interface ShareDialogProps {
  open: boolean
  path: string
  isDir: boolean
  onClose: () => void
  onCreated?: (share: Share) => void
  /** share switches the dialog into edit mode for an existing link. */
  share?: Share | null
  /** onUpdated fires after an edit is saved. */
  onUpdated?: (share: Share) => void
}

type Step = 'form' | 'done'

export function ShareDialog({ open, path, isDir, onClose, onCreated, share, onUpdated }: ShareDialogProps) {
  const toast = useToast()
  const queryClient = useQueryClient()
  const editing = Boolean(share)

  const [step, setStep] = useState<Step>('form')
  const [result, setResult] = useState<Share | null>(null)
  const [replacing, setReplacing] = useState<Share | null>(null)
  const [choiceHandled, setChoiceHandled] = useState(false)

  const [kind, setKind] = useState<ShareKind>('download')
  const [expiry, setExpiry] = useState('7d')
  const [password, setPassword] = useState('')
  const [showPassword, setShowPassword] = useState(false)
  const [clearPassword, setClearPassword] = useState(false)
  const [maxDownloads, setMaxDownloads] = useState('')
  const [note, setNote] = useState('')
  const [allowDownload, setAllowDownload] = useState(true)
  const [allowUpload, setAllowUpload] = useState(false)
  const [allowList, setAllowList] = useState(true)
  const [moreOpen, setMoreOpen] = useState(false)
  const [problem, setProblem] = useState('')

  // Existing links are only interesting when a new one is being made.
  const shares = useQuery({
    queryKey: ['shares', 'mine'],
    queryFn: () => api.shares(),
    enabled: open && !editing,
  })

  const existing = useMemo(() => {
    if (editing || !open) return null
    return shares.data?.shares.find((item) => item.path === path) ?? null
  }, [shares.data, path, editing, open])

  // Reset every time the dialog is opened so it never shows the last answer.
  useEffect(() => {
    if (!open) return
    setStep('form')
    setResult(null)
    setReplacing(null)
    setChoiceHandled(false)
    setProblem('')
    setShowPassword(false)
    setPassword('')
    setClearPassword(false)
    setMoreOpen(false)
    if (share) {
      setKind(share.kind)
      setExpiry('keep')
      setMaxDownloads(share.maxDownloads > 0 ? String(share.maxDownloads) : '')
      setNote(share.note)
      setAllowDownload(share.allowDownload)
      setAllowUpload(share.allowUpload)
      setAllowList(share.allowList)
      return
    }
    setKind('download')
    setExpiry('7d')
    setMaxDownloads('')
    setNote('')
    setAllowDownload(true)
    setAllowUpload(false)
    setAllowList(isDir)
  }, [open, share, isDir])

  /** chooseKind resets the permissions to the ones that kind implies. */
  const chooseKind = (next: ShareKind) => {
    setKind(next)
    if (next === 'upload') {
      setAllowDownload(false)
      setAllowUpload(true)
      setAllowList(false)
    } else {
      setAllowDownload(true)
      setAllowUpload(false)
      setAllowList(isDir)
    }
  }

  const limit = maxDownloads.trim() === '' ? 0 : Number(maxDownloads)
  const limitValid = Number.isInteger(limit) && limit >= 0

  const create = useMutation({
    mutationFn: async () => {
      const made = await api.createShare({
        path,
        kind,
        password: password.trim() ? password : undefined,
        expiresIn: expiry === 'keep' ? 'never' : expiry,
        maxDownloads: limit,
        allowDownload,
        allowUpload,
        allowList,
        note: note.trim(),
      })
      // Replacing means the previous link must stop working right away.
      if (replacing) {
        try {
          await api.deleteShare(replacing.id)
        } catch {
          // The new link is already live, a stale old one is not worth failing for.
        }
      }
      return made
    },
    onSuccess: (made) => {
      void queryClient.invalidateQueries({ queryKey: ['shares'] })
      void queryClient.invalidateQueries({ queryKey: ['dashboard'] })
      setResult(made)
      setStep('done')
      setProblem('')
      onCreated?.(made)
    },
    onError: (error: unknown) => setProblem(messageFor(error)),
  })

  const update = useMutation({
    mutationFn: async () => {
      if (!share) throw new Error('There is no link to save')
      const body: Record<string, unknown> = {
        kind,
        maxDownloads: limit,
        note: note.trim(),
        allowDownload,
        allowUpload,
        allowList,
      }
      if (expiry !== 'keep') body.expiresIn = expiry
      if (clearPassword) body.clearPassword = true
      else if (password.trim()) body.password = password
      return api.updateShare(share.id, body)
    },
    onSuccess: (saved) => {
      void queryClient.invalidateQueries({ queryKey: ['shares'] })
      toast.success('Link updated')
      onUpdated?.(saved)
      onClose()
    },
    onError: (error: unknown) => setProblem(messageFor(error)),
  })

  const busy = create.isPending || update.isPending
  const name = share?.name || baseName(path) || path
  const targetIsDir = share ? share.isDir : isDir

  const copy = async (value: string) => {
    const ok = await copyText(value)
    if (ok) toast.success('Link copied', 'The address is on your clipboard')
    else toast.error('Could not copy', 'Select the address and copy it by hand')
  }

  // ---- the reuse or replace question ----------------------------------------

  const askAboutExisting = !editing && step === 'form' && !choiceHandled && existing !== null

  if (askAboutExisting && existing) {
    return (
      <Modal
        open={open}
        onClose={onClose}
        icon="link"
        title="This already has a link"
        description={`A link for ${name} was made ${longDate(existing.createdAt)}.`}
        width={470}
        footer={
          <>
            <Button onClick={onClose}>Cancel</Button>
            <Button
              onClick={() => {
                setReplacing(existing)
                setChoiceHandled(true)
              }}
            >
              Replace it
            </Button>
            <Button
              variant="primary"
              icon="copy"
              onClick={() => {
                setResult(existing)
                setChoiceHandled(true)
                setStep('done')
              }}
            >
              Use that link
            </Button>
          </>
        }
      >
        <div className="space-y-3">
          <TargetRow name={existing.name} path={existing.path} isDir={existing.isDir} />
          <p className="text-sm text-muted">
            You can hand out the link that already exists, or replace it with a new one. Replacing stops the old link
            working straight away, so anyone who has it will need the new address.
          </p>
        </div>
      </Modal>
    )
  }

  // ---- the created link -----------------------------------------------------

  if (step === 'done' && result) {
    const url = shareLink(result)
    return (
      <Modal
        open={open}
        onClose={onClose}
        icon="check-circle"
        title="The link is ready"
        width={520}
        footer={
          <>
            <Button icon="external" onClick={() => window.open(url, '_blank', 'noopener')}>
              Open link
            </Button>
            <Button variant="primary" icon="copy" onClick={() => void copy(url)}>
              Copy link
            </Button>
          </>
        }
      >
        <div className="space-y-4">
          <TargetRow name={result.name} path={result.path} isDir={result.isDir} />
          <div className="flex flex-col gap-4 sm:flex-row sm:items-start">
            <div className="min-w-0 flex-1 space-y-4">
              <div>
                <label className="sx-label" htmlFor="sx-share-url">
                  Address
                </label>
                <div className="flex gap-2">
                  <input
                    id="sx-share-url"
                    className="sx-input font-mono text-xs"
                    value={url}
                    readOnly
                    onFocus={(event) => event.currentTarget.select()}
                  />
                  <IconButton icon="copy" label="Copy link" onClick={() => void copy(url)} />
                </div>
              </div>
              <p className="text-sm text-muted">
                {consequence(result.kind, result.isDir, result.expiresAt, result.hasPassword)}
              </p>
            </div>
            <div className="flex shrink-0 flex-col items-center gap-2 self-center sm:self-start">
              <div className="rounded-xl bg-white p-1.5 text-black">
                <QRCode value={url} size={150} />
              </div>
              <p className="w-[162px] text-center text-[11px] text-faint">Point a phone camera at this</p>
            </div>
          </div>
        </div>
      </Modal>
    )
  }

  // ---- the form -------------------------------------------------------------

  const preview = consequence(kind, targetIsDir, expiryPreview(expiry, share), Boolean(passwordInEffect(share, password, clearPassword)))

  return (
    <Modal
      open={open}
      onClose={onClose}
      icon={kind === 'upload' ? 'cloud-upload' : 'link'}
      title={editing ? 'Edit link' : 'Create a link'}
      description={editing ? 'Changes apply to the address people already have.' : 'Share this without giving anyone an account.'}
      width={560}
      footer={
        <>
          <Button onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          <Button
            variant="primary"
            icon={editing ? 'check' : 'link'}
            loading={busy}
            disabled={!limitValid}
            onClick={() => {
              setProblem('')
              if (editing) update.mutate()
              else create.mutate()
            }}
          >
            {editing ? 'Save changes' : 'Create link'}
          </Button>
        </>
      }
    >
      <div className="space-y-4">
        <TargetRow name={name} path={share?.path ?? path} isDir={targetIsDir} />

        {replacing && (
          <p className="rounded-xl bg-warning/10 px-3 py-2 text-xs text-warning">
            The link made {longDate(replacing.createdAt)} will be revoked once the new one is created.
          </p>
        )}

        <div role="radiogroup" aria-label="What this link does" className="grid gap-2 sm:grid-cols-2">
          <KindOption
            selected={kind === 'download'}
            icon="download"
            title="Share a copy to download"
            hint={targetIsDir ? 'People can browse and download what is inside' : 'People can view and download this file'}
            onSelect={() => chooseKind('download')}
          />
          <KindOption
            selected={kind === 'upload'}
            icon="cloud-upload"
            title="Ask for files to be uploaded here"
            hint={targetIsDir ? 'People can send you files, without seeing what is inside' : 'Only a folder can receive files'}
            disabled={!targetIsDir}
            onSelect={() => chooseKind('upload')}
          />
        </div>

        <Select
          label="Expires"
          value={expiry}
          onChange={setExpiry}
          options={
            editing
              ? [{ value: 'keep', label: keepLabel(share?.expiresAt) }, ...EXPIRY_OPTIONS]
              : EXPIRY_OPTIONS
          }
        />

        <div>
          <label className="sx-label" htmlFor="sx-share-password">
            Password {editing && share?.hasPassword ? '' : '(optional)'}
          </label>
          <div className="relative">
            <input
              id="sx-share-password"
              className="sx-input pr-11"
              type={showPassword ? 'text' : 'password'}
              autoComplete="new-password"
              placeholder={
                clearPassword
                  ? 'The password will be removed'
                  : editing && share?.hasPassword
                    ? 'Leave empty to keep the current password'
                    : 'No password'
              }
              value={password}
              disabled={clearPassword}
              onChange={(event) => setPassword(event.target.value)}
            />
            <span className="absolute right-1 top-1/2 -translate-y-1/2">
              <IconButton
                icon={showPassword ? 'eye-off' : 'eye'}
                label={showPassword ? 'Hide password' : 'Show password'}
                size={15}
                onClick={() => setShowPassword((value) => !value)}
              />
            </span>
          </div>
          {editing && share?.hasPassword && (
            <button
              type="button"
              className="mt-1.5 text-xs font-medium text-danger hover:underline"
              onClick={() => {
                setClearPassword((value) => !value)
                setPassword('')
              }}
            >
              {clearPassword ? 'Keep the password' : 'Remove password'}
            </button>
          )}
        </div>

        <div className="rounded-xl border border-line bg-elevated/60 px-3 py-2.5">
          <p className="text-sm text-muted">{preview}</p>
        </div>

        <button
          type="button"
          className="flex w-full items-center gap-2 text-xs font-medium text-muted hover:text-ink"
          aria-expanded={moreOpen}
          onClick={() => setMoreOpen((value) => !value)}
        >
          <Icon name={moreOpen ? 'chevron-down' : 'chevron-right'} size={14} />
          More options
        </button>

        {(moreOpen || !limitValid) && (
          <div className="space-y-4 border-t border-line pt-4">
            <Field
              label="Maximum downloads (optional)"
              type="number"
              min={0}
              inputMode="numeric"
              placeholder="No limit"
              value={maxDownloads}
              onChange={(event) => setMaxDownloads(event.target.value)}
              error={limitValid ? undefined : 'Enter a whole number, or leave it empty for no limit'}
              hint={limitValid ? 'The link stops working once this many downloads have happened' : undefined}
            />
            <Field
              label="Note (optional)"
              placeholder="Shown to anyone who opens the link"
              maxLength={500}
              value={note}
              onChange={(event) => setNote(event.target.value)}
            />
            <div className="space-y-3">
              <span className="sx-label">What the link allows</span>
              <Toggle checked={allowDownload} onChange={setAllowDownload} label="Allow download" />
              <Toggle
                checked={allowUpload}
                onChange={setAllowUpload}
                label="Allow upload"
                hint={targetIsDir ? undefined : 'Only a folder can receive files'}
                disabled={!targetIsDir}
              />
              <Toggle
                checked={allowList}
                onChange={setAllowList}
                label="Allow browsing"
                hint={targetIsDir ? 'People can see the names of the files inside' : 'Only a folder can be browsed'}
                disabled={!targetIsDir}
              />
            </div>
          </div>
        )}

        {problem && (
          <p className="flex items-start gap-2 rounded-xl bg-danger/10 px-3 py-2 text-sm text-danger" role="alert">
            <Icon name="alert" size={15} className="mt-0.5 shrink-0" />
            <span>{problem}</span>
          </p>
        )}
      </div>
    </Modal>
  )
}

export default ShareDialog

// ---- pieces ------------------------------------------------------------------

function TargetRow({ name, path, isDir }: { name: string; path: string; isDir: boolean }) {
  return (
    <div className="flex items-center gap-3 rounded-xl border border-line bg-elevated/60 px-3 py-2.5">
      <span className={clsx('shrink-0', colourForKind('other', isDir))}>
        <Icon name={iconForKind('other', isDir)} size={20} />
      </span>
      <div className="min-w-0">
        <div className="truncate text-sm font-medium text-ink">{name}</div>
        <div className="truncate font-mono text-[11px] text-faint" title={path}>
          {truncateMiddle(path, 52)}
        </div>
      </div>
    </div>
  )
}

function KindOption({
  selected,
  icon,
  title,
  hint,
  disabled,
  onSelect,
}: {
  selected: boolean
  icon: 'download' | 'cloud-upload'
  title: string
  hint: string
  disabled?: boolean
  onSelect: () => void
}) {
  return (
    <button
      type="button"
      role="radio"
      aria-checked={selected}
      disabled={disabled}
      onClick={onSelect}
      className={clsx(
        'flex h-full flex-col gap-1 rounded-xl border px-3 py-2.5 text-left transition-colors',
        selected ? 'border-primary/60 bg-primary/10' : 'border-line bg-elevated hover:border-line/80 hover:bg-line/40',
        disabled && 'cursor-not-allowed opacity-45',
      )}
    >
      <span className={clsx('flex items-center gap-2 text-sm font-medium', selected ? 'text-ink' : 'text-muted')}>
        <Icon name={icon} size={16} />
        {title}
      </span>
      <span className="text-xs text-faint">{hint}</span>
    </button>
  )
}

// ---- copy --------------------------------------------------------------------

/** consequence is the one line that says what the link actually lets people do. */
export function consequence(
  kind: ShareKind,
  isDir: boolean,
  expiresAt: string | Date | null | undefined,
  hasPassword: boolean,
): string {
  const until = expiresAt ? `until ${longDate(expiresAt)}` : 'until you revoke it'
  const what = kind === 'upload' ? 'upload files into this folder' : `download this ${isDir ? 'folder' : 'file'}`
  const lock = hasPassword ? ', once they enter the password' : ''
  return `Anyone with this link can ${what} ${until}${lock}.`
}

function keepLabel(expiresAt: string | undefined): string {
  if (!expiresAt) return 'Keep it open with no end date'
  return `Keep the current date, ${longDate(expiresAt)}`
}

function expiryPreview(expiry: string, share: Share | null | undefined): Date | string | null {
  if (expiry === 'keep') return share?.expiresAt ?? null
  return expiryDate(expiry)
}

function passwordInEffect(share: Share | null | undefined, password: string, clearPassword: boolean): boolean {
  if (clearPassword) return false
  if (password.trim()) return true
  return Boolean(share?.hasPassword)
}

function messageFor(error: unknown): string {
  if (error instanceof ApiError) return error.detail ? `${error.message}. ${error.detail}` : error.message
  if (error instanceof Error) return error.message
  return 'Something went wrong, try again'
}
