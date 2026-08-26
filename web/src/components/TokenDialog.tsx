// Create one access token: what it is called, how much it may do and how long
// it lasts. The secret is shown once, here, and is never held anywhere else.
// Developed by X Project.

import clsx from 'clsx'
import { useEffect, useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { ApiError, api } from '../lib/api'
import type { ApiToken, TokenScope } from '../lib/types'
import { Icon, type IconName } from './Icon'
import { copyText } from './ShareDialog'
import { Button, Field, IconButton, Modal, Select, useToast } from './ui'

// ---- helpers ----------------------------------------------------------------

/** tokenExplain turns any thrown value into one calm sentence. */
function tokenExplain(error: unknown): string {
  if (error instanceof ApiError) {
    if (error.status === 409) return error.message || 'You already have a token with that name.'
    return error.detail ? `${error.message}. ${error.detail}` : error.message
  }
  if (error instanceof Error && error.message) return error.message
  return 'The token could not be created. Try again.'
}

// The windows the server accepts, in the same short form the share links use.
const TOKEN_EXPIRY_OPTIONS = [
  { value: '30d', label: '30 days' },
  { value: '90d', label: '90 days' },
  { value: '1y', label: '1 year' },
  { value: 'never', label: 'Never' },
]

const TOKEN_DEFAULT_EXPIRY = '90d'

// ---- one scope choice --------------------------------------------------------

function TokenScopeOption({
  selected,
  icon,
  title,
  hint,
  onSelect,
}: {
  selected: boolean
  icon: IconName
  title: string
  hint: string
  onSelect: () => void
}) {
  return (
    <button
      type="button"
      role="radio"
      aria-checked={selected}
      onClick={onSelect}
      className={clsx(
        'flex h-full flex-col gap-1 rounded-xl border px-3 py-2.5 text-left transition-colors',
        selected ? 'border-primary/60 bg-primary/10' : 'border-line bg-elevated hover:border-line/80 hover:bg-line/40',
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

// ---- the dialog --------------------------------------------------------------

export function TokenDialog({
  open,
  onClose,
  onCreated,
}: {
  open: boolean
  onClose: () => void
  onCreated?: (token: ApiToken) => void
}) {
  const client = useQueryClient()
  const toast = useToast()

  const [name, setName] = useState('')
  const [scope, setScope] = useState<TokenScope>('read')
  const [expiry, setExpiry] = useState(TOKEN_DEFAULT_EXPIRY)
  const [problem, setProblem] = useState('')
  const [secret, setSecret] = useState('')
  const [issued, setIssued] = useState<ApiToken | null>(null)

  // Every opening starts clean, so nothing from the last one is still on screen.
  useEffect(() => {
    if (!open) return
    setName('')
    setScope('read')
    setExpiry(TOKEN_DEFAULT_EXPIRY)
    setProblem('')
    setSecret('')
    setIssued(null)
  }, [open])

  const create = useMutation({
    mutationFn: async (): Promise<ApiToken> => {
      const made = await api.createToken({ name: name.trim(), scope, expiresIn: expiry })
      // The secret is handed straight to this component and nothing else is
      // ever given it: the mutation result and the query cache hold the plain
      // token record only, which carries the prefix and no usable credential.
      setSecret(made.secret)
      return made.token
    },
    onSuccess: (token) => {
      setIssued(token)
      setProblem('')
      void client.invalidateQueries({ queryKey: ['tokens'] })
      onCreated?.(token)
    },
    onError: (failure: unknown) => setProblem(tokenExplain(failure)),
  })

  /** finish drops the secret before the dialog goes away. */
  function finish() {
    setSecret('')
    setIssued(null)
    onClose()
  }

  function submit() {
    if (!name.trim() || create.isPending) return
    setProblem('')
    create.mutate()
  }

  // ---- the token itself, shown this once ------------------------------------

  if (issued && secret) {
    return (
      <Modal
        open={open}
        onClose={finish}
        width={520}
        icon="key"
        title="Your new token"
        description={`${issued.name} is ready. This is the only time the token is shown.`}
        footer={
          <Button variant="primary" icon="check" onClick={finish}>
            I have copied it
          </Button>
        }
      >
        <div className="space-y-4">
          <div>
            <label className="sx-label" htmlFor="sx-token-secret">
              Token
            </label>
            <div className="flex gap-2">
              <input
                id="sx-token-secret"
                className="sx-input font-mono text-xs"
                value={secret}
                readOnly
                spellCheck={false}
                onFocus={(event) => event.currentTarget.select()}
              />
              <IconButton
                icon="copy"
                label="Copy the token"
                onClick={() => {
                  void copyText(secret).then((ok) =>
                    ok
                      ? toast.success('Token copied', 'It is on your clipboard now.')
                      : toast.error('The browser blocked the clipboard', 'Select the token and copy it by hand.'),
                  )
                }}
              />
            </div>
          </div>

          <div className="flex items-start gap-2.5 rounded-xl border border-warning/40 bg-warning/10 px-3 py-2.5 text-sm text-warning">
            <Icon name="alert" size={16} className="mt-0.5 shrink-0" />
            <div className="min-w-0 flex-1">
              <p className="font-medium">Copy it before you close this.</p>
              <p className="mt-1">
                Storix keeps only a hashed copy, so it cannot be shown again. A token that is lost has to be
                revoked and replaced with a new one.
              </p>
            </div>
          </div>
        </div>
      </Modal>
    )
  }

  // ---- the form -------------------------------------------------------------

  return (
    <Modal
      open={open}
      onClose={onClose}
      width={520}
      icon="key"
      title="Create an access token"
      description="A token stands in for your password in a script, a backup job or a mounted drive."
      footer={
        <>
          <Button onClick={onClose} disabled={create.isPending}>
            Cancel
          </Button>
          <Button
            variant="primary"
            icon="key"
            loading={create.isPending}
            disabled={!name.trim()}
            onClick={submit}
          >
            Create token
          </Button>
        </>
      }
    >
      <div className="space-y-4">
        <Field
          label="Name"
          placeholder="Nightly backup"
          maxLength={80}
          value={name}
          hint="Only you see this. It is how you will recognise the token in the list."
          onChange={(event) => setName(event.target.value)}
          onKeyDown={(event) => {
            if (event.key !== 'Enter') return
            event.preventDefault()
            submit()
          }}
        />

        <div>
          <span className="sx-label">What it may do</span>
          <div role="radiogroup" aria-label="What this token may do" className="grid gap-2 sm:grid-cols-2">
            <TokenScopeOption
              selected={scope === 'read'}
              icon="eye"
              title="Read only"
              hint="List and download anything you can reach, and change none of it."
              onSelect={() => setScope('read')}
            />
            <TokenScopeOption
              selected={scope === 'write'}
              icon="edit"
              title="Read and write"
              hint="Upload, rename, move and delete as well, exactly as you can."
              onSelect={() => setScope('write')}
            />
          </div>
        </div>

        <Select label="Expires" value={expiry} onChange={setExpiry} options={TOKEN_EXPIRY_OPTIONS} />
        <p className="text-xs text-faint">
          A token that runs out on its own is one less thing to remember. You can revoke any token at any time.
        </p>

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

export default TokenDialog
