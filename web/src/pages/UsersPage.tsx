// The people screen: who can sign in, what they are allowed to do and which
// folders they can reach.
// Developed by X Project.

import { useMemo, useState, type MouseEvent, type ReactNode } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ApiError, api } from '../lib/api'
import type { Permission, Role, User } from '../lib/types'
import { ago, baseName, counted, initials, truncateMiddle } from '../lib/format'
import { useSession } from '../lib/session'
import { Icon } from '../components/Icon'
import {
  Button,
  Checkbox,
  ConfirmDialog,
  EmptyState,
  Field,
  IconButton,
  Menu,
  Modal,
  Select,
  Skeleton,
  Toggle,
  Tooltip,
  useToast,
  type MenuItem,
} from '../components/ui'
import { PathPickerDialog } from '../components/dialogs'

// ---- helpers ----------------------------------------------------------------

/** explain turns any thrown value into one calm sentence. */
function explain(error: unknown): string {
  if (error instanceof ApiError) return error.detail ? `${error.message}. ${error.detail}` : error.message
  if (error instanceof Error && error.message) return error.message
  return 'Something went wrong. Try again.'
}

/** firstName is what the folder note calls the account. */
function firstName(displayName: string, username: string): string {
  const source = displayName.trim() || username.trim()
  const first = source.split(/\s+/)[0]
  return first || 'This account'
}

const PASSWORD_ALPHABET = 'abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789!?@#%+='

/** generatePassword builds a long random password from browser entropy. */
function generatePassword(length = 20): string {
  const values = new Uint32Array(length)
  crypto.getRandomValues(values)
  let out = ''
  for (let i = 0; i < length; i++) out += PASSWORD_ALPHABET[values[i] % PASSWORD_ALPHABET.length]
  return out
}

interface Strength {
  score: number
  label: string
}

/** passwordScore rates a password from 0 to 4 for the meter. */
function passwordScore(value: string): Strength {
  if (!value) return { score: 0, label: 'Empty' }
  let score = 0
  if (value.length >= 8) score += 1
  if (value.length >= 14) score += 1
  if (/[a-z]/.test(value) && /[A-Z]/.test(value)) score += 1
  if (/[0-9]/.test(value)) score += 1
  if (/[^A-Za-z0-9]/.test(value)) score += 1
  if (value.length < 8) score = 0
  const capped = Math.min(4, score)
  const labels = ['Too short', 'Weak', 'Fair', 'Good', 'Strong']
  return { score: capped, label: labels[capped] }
}

const ROLE_FALLBACK: Array<{ value: string; label: string }> = [
  { value: 'admin', label: 'Administrator' },
  { value: 'manager', label: 'Manager' },
  { value: 'user', label: 'User' },
  { value: 'readonly', label: 'Read only' },
  { value: 'custom', label: 'Custom' },
]

// ---- password strength meter -------------------------------------------------

function StrengthMeter({ value }: { value: string }) {
  const { score, label } = passwordScore(value)
  const tone =
    score <= 1 ? 'bg-danger' : score === 2 ? 'bg-warning' : score === 3 ? 'bg-secondary' : 'bg-success'
  return (
    <div className="mt-2 flex items-center gap-3">
      <div className="flex flex-1 gap-1.5" role="img" aria-label={`Password strength: ${label}`}>
        {[0, 1, 2, 3].map((index) => (
          <span
            key={index}
            className={`h-1.5 flex-1 rounded-full ${index < score ? tone : 'bg-line'}`}
          />
        ))}
      </div>
      <span className="w-16 shrink-0 text-right text-xs text-muted">{value ? label : ''}</span>
    </div>
  )
}

// ---- password control --------------------------------------------------------

function PasswordControl({
  value,
  onChange,
  label,
  hint,
}: {
  value: string
  onChange: (next: string) => void
  label: string
  hint: string
}) {
  const [visible, setVisible] = useState(false)
  return (
    <div>
      <label className="sx-label" htmlFor="sx-password-field">
        {label}
      </label>
      <div className="flex items-center gap-2">
        <div className="relative flex-1">
          <input
            id="sx-password-field"
            className="sx-input pr-10 font-mono"
            type={visible ? 'text' : 'password'}
            autoComplete="new-password"
            spellCheck={false}
            value={value}
            placeholder="At least 8 characters"
            onChange={(event) => onChange(event.target.value)}
          />
          <IconButton
            icon={visible ? 'eye-off' : 'eye'}
            label={visible ? 'Hide password' : 'Show password'}
            size={15}
            className="absolute right-0.5 top-1/2 h-8 w-8 -translate-y-1/2"
            onClick={() => setVisible((current) => !current)}
          />
        </div>
        <Button
          icon="refresh"
          onClick={() => {
            onChange(generatePassword())
            setVisible(true)
          }}
        >
          Generate
        </Button>
      </div>
      <StrengthMeter value={value} />
      <p className="mt-1.5 text-xs text-faint">{hint}</p>
    </div>
  )
}

// ---- account dialog ----------------------------------------------------------

interface MountDraft {
  key: string
  path: string
  label: string
  readOnly: boolean
}

interface UserDraft {
  username: string
  displayName: string
  email: string
  password: string
  role: Role
  permissions: Permission[]
  mounts: MountDraft[]
  active: boolean
  mustChangePassword: boolean
}

let mountKeySeed = 0
function nextMountKey(): string {
  mountKeySeed += 1
  return `mount-${mountKeySeed}`
}

function draftFromUser(user: User | null): UserDraft {
  if (!user) {
    return {
      username: '',
      displayName: '',
      email: '',
      password: '',
      role: 'user',
      permissions: [],
      mounts: [],
      active: true,
      mustChangePassword: false,
    }
  }
  return {
    username: user.username,
    displayName: user.displayName,
    email: user.email,
    password: '',
    role: user.role,
    permissions: [...user.permissions],
    mounts: user.mounts.map((mount) => ({
      key: nextMountKey(),
      path: mount.path,
      label: mount.label,
      readOnly: mount.readOnly,
    })),
    active: user.active,
    mustChangePassword: user.mustChangePassword,
  }
}

function UserDialog({
  open,
  editing,
  onClose,
}: {
  open: boolean
  editing: User | null
  onClose: () => void
}) {
  const client = useQueryClient()
  const toast = useToast()
  const [draft, setDraft] = useState<UserDraft>(() => draftFromUser(editing))
  const [advanced, setAdvanced] = useState(false)
  const [changePassword, setChangePassword] = useState(!editing)
  const [error, setError] = useState('')
  const [pickerFor, setPickerFor] = useState<string | null>(null)

  const roles = useQuery({ queryKey: ['roles'], queryFn: api.roles, staleTime: 5 * 60_000 })

  const roleOptions = useMemo(() => {
    if (!roles.data) return ROLE_FALLBACK
    return roles.data.roles.map((role) => ({ value: role.id, label: role.label }))
  }, [roles.data])

  const presetFor = useMemo(() => {
    const table = new Map<string, Permission[]>()
    for (const role of roles.data?.roles ?? []) table.set(role.id, role.permissions)
    return table
  }, [roles.data])

  const save = useMutation({
    mutationFn: async (body: Record<string, unknown>) =>
      editing ? api.updateUser(editing.id, body) : api.createUser(body),
    onSuccess: async () => {
      await client.invalidateQueries({ queryKey: ['users'] })
      toast.success(editing ? 'Account updated' : 'Account created')
      onClose()
    },
    onError: (failure: unknown) => setError(explain(failure)),
  })

  const isAdminRole = draft.role === 'admin'
  const person = firstName(draft.displayName, draft.username || 'this account')

  function patch(next: Partial<UserDraft>) {
    setDraft((current) => ({ ...current, ...next }))
  }

  function setRole(value: string) {
    const role = value as Role
    const preset = presetFor.get(role)
    patch({ role, permissions: role === 'custom' || !preset ? draft.permissions : [...preset] })
  }

  function togglePermission(permission: Permission, on: boolean) {
    const next = on
      ? [...draft.permissions, permission]
      : draft.permissions.filter((item) => item !== permission)
    // A hand picked set is by definition a custom role.
    patch({ permissions: next, role: 'custom' })
  }

  function addMount() {
    patch({ mounts: [...draft.mounts, { key: nextMountKey(), path: '', label: '', readOnly: false }] })
  }

  function updateMount(key: string, next: Partial<MountDraft>) {
    patch({
      mounts: draft.mounts.map((mount) => (mount.key === key ? { ...mount, ...next } : mount)),
    })
  }

  function removeMount(key: string) {
    patch({ mounts: draft.mounts.filter((mount) => mount.key !== key) })
  }

  function submit() {
    setError('')
    const username = draft.username.trim()
    if (!username) {
      setError('Enter a username.')
      return
    }
    if (!/^[A-Za-z0-9._-]{2,32}$/.test(username)) {
      setError('A username is 2 to 32 characters and may hold letters, digits, dots, dashes and underscores.')
      return
    }
    const wantsPassword = !editing || changePassword
    if (wantsPassword && draft.password.length < 8) {
      setError('Use at least 8 characters for the password.')
      return
    }
    if (draft.role === 'custom' && draft.permissions.length === 0) {
      setError('Choose at least one permission for a custom role.')
      return
    }
    if (!isAdminRole) {
      const missing = draft.mounts.some((mount) => !mount.path.trim())
      if (missing) {
        setError('Every folder row needs a path. Pick one or remove the row.')
        return
      }
    }

    const body: Record<string, unknown> = {
      username,
      displayName: draft.displayName.trim(),
      email: draft.email.trim(),
      role: draft.role,
      permissions: draft.permissions,
      active: draft.active,
      mustChangePassword: draft.mustChangePassword,
    }
    // An administrator reaches everything, so the stored folder list is left
    // untouched rather than wiped, in case the role is lowered again later.
    if (!isAdminRole) {
      body.mounts = draft.mounts.map((mount) => ({
        path: mount.path.trim(),
        label: mount.label.trim() || baseName(mount.path.trim()),
        icon: 'folder',
        readOnly: mount.readOnly,
      }))
    }
    if (wantsPassword) body.password = draft.password
    save.mutate(body)
  }

  const catalogue = roles.data?.permissions ?? []

  return (
    <>
      <Modal
        open={open}
        onClose={onClose}
        width={720}
        icon={editing ? 'user' : 'users'}
        title={editing ? `Edit ${editing.displayName || editing.username}` : 'Add a person'}
        description={
          editing
            ? 'Change what this account can do and where it can go.'
            : 'Create an account and choose the folders it can reach.'
        }
        footer={
          <>
            <Button onClick={onClose} disabled={save.isPending}>
              Cancel
            </Button>
            <Button variant="primary" onClick={submit} loading={save.isPending}>
              {editing ? 'Save changes' : 'Create account'}
            </Button>
          </>
        }
      >
        <div className="sx-scroll -mr-2 max-h-[62vh] space-y-5 pr-2">
          {error && (
            <div className="flex items-start gap-2.5 rounded-xl border border-danger/40 bg-danger/10 px-3 py-2.5 text-sm text-danger">
              <Icon name="alert" size={16} className="mt-0.5 shrink-0" />
              <span>{error}</span>
            </div>
          )}

          <div className="grid gap-4 sm:grid-cols-2">
            <Field
              label="Username"
              value={draft.username}
              autoComplete="off"
              spellCheck={false}
              placeholder="jdoe"
              onChange={(event) => patch({ username: event.target.value })}
            />
            <Field
              label="Display name"
              value={draft.displayName}
              placeholder="John Doe"
              onChange={(event) => patch({ displayName: event.target.value })}
            />
          </div>

          <Field
            label="Email"
            type="email"
            value={draft.email}
            placeholder="john@example.com"
            hint="Used for notices only. Storix never sends marketing mail."
            onChange={(event) => patch({ email: event.target.value })}
          />

          {editing && !changePassword ? (
            <div className="flex items-center justify-between rounded-xl border border-line bg-elevated px-3 py-2.5">
              <div className="min-w-0">
                <div className="text-sm text-ink">Password</div>
                <div className="mt-0.5 text-xs text-faint">The current password stays in place.</div>
              </div>
              <Button icon="key" onClick={() => setChangePassword(true)}>
                Set a new password
              </Button>
            </div>
          ) : (
            <div>
              <PasswordControl
                label={editing ? 'New password' : 'Password'}
                value={draft.password}
                onChange={(password) => patch({ password })}
                hint={
                  editing
                    ? 'Saving a new password signs this account out of every browser except your own.'
                    : 'Share it with the person over a channel you trust. Storix stores only a hash.'
                }
              />
              <div className="mt-3 flex flex-wrap items-center gap-4">
                <Checkbox
                  checked={draft.mustChangePassword}
                  onChange={(value) => patch({ mustChangePassword: value })}
                  label="Ask for a new password at first sign in"
                />
                {editing && (
                  <button
                    type="button"
                    className="text-xs font-medium text-primary hover:underline"
                    onClick={() => {
                      setChangePassword(false)
                      patch({ password: '' })
                    }}
                  >
                    Keep the current password
                  </button>
                )}
              </div>
            </div>
          )}

          <div className="grid gap-4 sm:grid-cols-2">
            <Select label="Role" value={draft.role} options={roleOptions} onChange={setRole} />
            <div className="flex items-end pb-1">
              <Toggle
                checked={draft.active}
                onChange={(value) => patch({ active: value })}
                label="Account is active"
                hint="An inactive account cannot sign in."
              />
            </div>
          </div>

          {/* Advanced permissions ------------------------------------------ */}
          <div className="rounded-xl border border-line">
            <button
              type="button"
              className="flex w-full items-center gap-2 px-3 py-2.5 text-left text-sm text-ink"
              aria-expanded={advanced}
              onClick={() => setAdvanced((current) => !current)}
            >
              <Icon name={advanced ? 'chevron-down' : 'chevron-right'} size={16} className="text-faint" />
              <span className="flex-1">Advanced permissions</span>
              <span className="text-xs text-faint">{counted(draft.permissions.length, 'permission')}</span>
            </button>
            {advanced && (
              <div className="border-t border-line px-3 py-3">
                <p className="mb-3 text-xs text-faint">
                  The role above already sets a sensible list. Changing anything here makes the role custom.
                </p>
                {roles.isPending ? (
                  <div className="space-y-2">
                    {[0, 1, 2, 3].map((index) => (
                      <Skeleton key={index} className="h-9 w-full" />
                    ))}
                  </div>
                ) : roles.isError ? (
                  <div className="text-sm text-danger">
                    The permission list could not be loaded. {explain(roles.error)}
                  </div>
                ) : (
                  <div className="grid gap-2.5 sm:grid-cols-2">
                    {catalogue.map((item) => (
                      <label
                        key={item.id}
                        className="flex cursor-pointer items-start gap-2.5 rounded-lg px-2 py-1.5 hover:bg-elevated"
                      >
                        <span className="mt-0.5">
                          <Checkbox
                            checked={draft.permissions.includes(item.id)}
                            onChange={(value) => togglePermission(item.id, value)}
                          />
                        </span>
                        <span className="min-w-0">
                          <span className="block text-sm text-ink">{item.label}</span>
                          <span className="mt-0.5 block text-xs text-faint">{item.description}</span>
                        </span>
                      </label>
                    ))}
                  </div>
                )}
              </div>
            )}
          </div>

          {/* Folder access -------------------------------------------------- */}
          <div>
            <div className="mb-2 flex items-center justify-between gap-3">
              <div>
                <div className="text-sm font-medium text-ink">Folder access</div>
                <p className="mt-0.5 text-xs text-faint">
                  {isAdminRole
                    ? 'Administrators always reach every folder, so there is nothing to choose here.'
                    : `${person} will only see the folders you add here.`}
                </p>
              </div>
              {!isAdminRole && (
                <Button icon="folder-plus" onClick={addMount}>
                  Add folder
                </Button>
              )}
            </div>

            {isAdminRole ? (
              <div className="flex items-start gap-2.5 rounded-xl border border-line bg-elevated px-3 py-2.5 text-sm text-muted">
                <Icon name="shield" size={16} className="mt-0.5 shrink-0 text-primary" />
                <span>Every storage location you configured in settings is available to this account.</span>
              </div>
            ) : draft.mounts.length === 0 ? (
              <div className="rounded-xl border border-dashed border-line px-3 py-6 text-center text-sm text-muted">
                No folders yet. This account will not see anything until you add one.
              </div>
            ) : (
              <div className="space-y-2">
                {draft.mounts.map((mount) => (
                  <div key={mount.key} className="rounded-xl border border-line bg-elevated/60 p-2.5">
                    <div className="grid items-center gap-2 sm:grid-cols-[minmax(0,1fr)_11rem_auto]">
                      <button
                        type="button"
                        className="sx-input flex items-center gap-2 text-left hover:border-primary/60"
                        onClick={() => setPickerFor(mount.key)}
                      >
                        <Icon name="folder" size={15} className="shrink-0 text-primary" />
                        <span className={`truncate ${mount.path ? 'text-ink' : 'text-faint'}`}>
                          {mount.path || 'Choose a folder'}
                        </span>
                      </button>
                      <input
                        className="sx-input"
                        aria-label="Folder label"
                        placeholder={mount.path ? baseName(mount.path) : 'Label'}
                        value={mount.label}
                        onChange={(event) => updateMount(mount.key, { label: event.target.value })}
                      />
                      <IconButton
                        icon="trash"
                        label="Remove this folder"
                        tone="danger"
                        onClick={() => removeMount(mount.key)}
                      />
                    </div>
                    <div className="mt-2.5 px-0.5">
                      <Toggle
                        checked={mount.readOnly}
                        onChange={(value) => updateMount(mount.key, { readOnly: value })}
                        label="Read only"
                        hint="The folder can be opened and downloaded, never changed."
                      />
                    </div>
                  </div>
                ))}
              </div>
            )}
          </div>
        </div>
      </Modal>

      {pickerFor !== null && (
        <PathPickerDialog
          open
          title="Choose a folder"
          initialPath={draft.mounts.find((mount) => mount.key === pickerFor)?.path || '/'}
          onClose={() => setPickerFor(null)}
          onPick={(path: string) => {
            const current = draft.mounts.find((mount) => mount.key === pickerFor)
            updateMount(pickerFor, {
              path,
              label: current && current.label ? current.label : baseName(path) || path,
            })
            setPickerFor(null)
          }}
        />
      )}
    </>
  )
}

// ---- reset password dialog ---------------------------------------------------

function ResetPasswordDialog({ user, onClose }: { user: User; onClose: () => void }) {
  const client = useQueryClient()
  const toast = useToast()
  const [password, setPassword] = useState('')
  const [signOut, setSignOut] = useState(true)
  const [error, setError] = useState('')

  const reset = useMutation({
    mutationFn: () => api.updateUser(user.id, { password, mustChangePassword: signOut }),
    onSuccess: async () => {
      await client.invalidateQueries({ queryKey: ['users'] })
      toast.success('Password reset', `${user.displayName || user.username} can sign in with the new password.`)
      onClose()
    },
    onError: (failure: unknown) => setError(explain(failure)),
  })

  return (
    <Modal
      open
      onClose={onClose}
      width={520}
      icon="key"
      title="Reset password"
      description={`Set a new password for ${user.displayName || user.username}.`}
      footer={
        <>
          <Button onClick={onClose} disabled={reset.isPending}>
            Cancel
          </Button>
          <Button
            variant="primary"
            loading={reset.isPending}
            onClick={() => {
              setError('')
              if (password.length < 8) {
                setError('Use at least 8 characters for the password.')
                return
              }
              reset.mutate()
            }}
          >
            Reset password
          </Button>
        </>
      }
    >
      <div className="space-y-4">
        {error && (
          <div className="flex items-start gap-2.5 rounded-xl border border-danger/40 bg-danger/10 px-3 py-2.5 text-sm text-danger">
            <Icon name="alert" size={16} className="mt-0.5 shrink-0" />
            <span>{error}</span>
          </div>
        )}
        <PasswordControl
          label="New password"
          value={password}
          onChange={setPassword}
          hint="Every signed in browser for this account is signed out once the password changes."
        />
        <Checkbox
          checked={signOut}
          onChange={setSignOut}
          label="Ask for a new password at the next sign in"
        />
      </div>
    </Modal>
  )
}

// ---- table pieces ------------------------------------------------------------

function RoleChip({ role, label }: { role: Role; label: string }) {
  const tone =
    role === 'admin'
      ? 'border-primary/40 bg-primary/12 text-primary'
      : role === 'manager'
        ? 'border-accent/40 bg-accent/12 text-accent'
        : role === 'readonly'
          ? 'border-line bg-elevated text-muted'
          : 'border-line bg-elevated text-ink'
  return <span className={`sx-chip ${tone}`}>{label}</span>
}

function Avatar({ user }: { user: User }) {
  return (
    <span
      className="flex h-9 w-9 shrink-0 items-center justify-center rounded-xl text-xs font-semibold text-white"
      style={{
        backgroundImage: 'linear-gradient(135deg, rgb(var(--sx-secondary)), rgb(var(--sx-violet)))',
      }}
      aria-hidden="true"
    >
      {initials(user.displayName || user.username)}
    </span>
  )
}

function FolderCell({ user }: { user: User }) {
  if (user.role === 'admin') {
    return (
      <span className="inline-flex items-center gap-1.5 text-sm text-muted">
        <Icon name="shield" size={14} className="text-primary" />
        All folders
      </span>
    )
  }
  if (user.mounts.length === 0) {
    return <span className="text-sm text-faint">No folders</span>
  }
  const shown = user.mounts.slice(0, 4).map((mount) => truncateMiddle(mount.path, 34))
  const rest = user.mounts.length - shown.length
  const label = rest > 0 ? `${shown.join('   ')}   and ${rest} more` : shown.join('   ')
  return (
    <Tooltip label={label}>
      <span className="inline-flex cursor-default items-center gap-1.5 border-b border-dashed border-line text-sm text-muted">
        <Icon name="folder" size={14} />
        {counted(user.mounts.length, 'folder')}
      </span>
    </Tooltip>
  )
}

function LoadingRows() {
  return (
    <tbody>
      {[0, 1, 2, 3, 4].map((index) => (
        <tr key={index} className="border-t border-line">
          <td className="px-4 py-3">
            <div className="flex items-center gap-3">
              <Skeleton className="h-9 w-9 rounded-xl" />
              <div className="space-y-1.5">
                <Skeleton className="h-3.5 w-32" />
                <Skeleton className="h-3 w-20" />
              </div>
            </div>
          </td>
          <td className="px-4 py-3">
            <Skeleton className="h-5 w-20" />
          </td>
          <td className="px-4 py-3">
            <Skeleton className="h-4 w-24" />
          </td>
          <td className="px-4 py-3">
            <Skeleton className="h-4 w-10" />
          </td>
          <td className="px-4 py-3">
            <Skeleton className="h-4 w-24" />
          </td>
          <td className="px-4 py-3">
            <Skeleton className="h-5 w-16" />
          </td>
          <td className="px-4 py-3" />
        </tr>
      ))}
    </tbody>
  )
}

// ---- page --------------------------------------------------------------------

export default function UsersPage() {
  const { can, user: me } = useSession()
  const client = useQueryClient()
  const toast = useToast()

  const [search, setSearch] = useState('')
  const [dialogOpen, setDialogOpen] = useState(false)
  const [editing, setEditing] = useState<User | null>(null)
  const [resetting, setResetting] = useState<User | null>(null)
  const [menu, setMenu] = useState<{ user: User; x: number; y: number } | null>(null)
  const [confirm, setConfirm] = useState<{ user: User; action: 'delete' | 'disable' | 'enable' } | null>(null)
  const [confirmError, setConfirmError] = useState('')

  const users = useQuery({ queryKey: ['users'], queryFn: api.users })
  const roles = useQuery({ queryKey: ['roles'], queryFn: api.roles, staleTime: 5 * 60_000 })

  const roleLabels = useMemo(() => {
    const table = new Map<string, string>()
    for (const role of roles.data?.roles ?? []) table.set(role.id, role.label)
    for (const fallback of ROLE_FALLBACK) if (!table.has(fallback.value)) table.set(fallback.value, fallback.label)
    return table
  }, [roles.data])

  const setActive = useMutation({
    mutationFn: (input: { id: number; active: boolean }) => api.updateUser(input.id, { active: input.active }),
    onSuccess: async (_result, input) => {
      await client.invalidateQueries({ queryKey: ['users'] })
      toast.success(input.active ? 'Account enabled' : 'Account disabled')
      setConfirm(null)
    },
    onError: (failure: unknown) => setConfirmError(explain(failure)),
  })

  const remove = useMutation({
    mutationFn: (id: number) => api.deleteUser(id),
    onSuccess: async () => {
      await client.invalidateQueries({ queryKey: ['users'] })
      toast.success('Account deleted')
      setConfirm(null)
    },
    onError: (failure: unknown) => setConfirmError(explain(failure)),
  })

  const rows = useMemo(() => {
    const list = users.data?.users ?? []
    const needle = search.trim().toLowerCase()
    if (!needle) return list
    return list.filter((user) =>
      [user.username, user.displayName, user.email].some((field) => field.toLowerCase().includes(needle)),
    )
  }, [users.data, search])

  if (!can('users')) {
    return (
      <div className="flex h-full items-center justify-center p-6">
        <EmptyState
          icon="lock"
          title="You cannot manage accounts"
          message="Ask an administrator to give you the people permission if you need this screen."
        />
      </div>
    )
  }

  function openMenu(user: User, event: MouseEvent<HTMLButtonElement>) {
    const rect = event.currentTarget.getBoundingClientRect()
    setMenu({ user, x: rect.right, y: rect.bottom + 6 })
  }

  function menuItems(user: User): MenuItem[] {
    const self = me?.id === user.id
    return [
      {
        id: 'edit',
        label: 'Edit account',
        icon: 'edit',
        onSelect: () => {
          setEditing(user)
          setDialogOpen(true)
        },
      },
      { id: 'reset', label: 'Reset password', icon: 'key', onSelect: () => setResetting(user) },
      { id: 'divider-1', label: '', divider: true },
      user.active
        ? {
            id: 'disable',
            label: 'Disable account',
            icon: 'lock',
            disabled: self,
            onSelect: () => {
              setConfirmError('')
              setConfirm({ user, action: 'disable' })
            },
          }
        : {
            id: 'enable',
            label: 'Enable account',
            icon: 'check-circle',
            onSelect: () => {
              setConfirmError('')
              setConfirm({ user, action: 'enable' })
            },
          },
      {
        id: 'delete',
        label: 'Delete account',
        icon: 'trash',
        danger: true,
        disabled: self,
        onSelect: () => {
          setConfirmError('')
          setConfirm({ user, action: 'delete' })
        },
      },
    ]
  }

  const confirmCopy = (() => {
    if (!confirm) return { title: '', message: null as ReactNode, label: '', danger: false }
    const name = confirm.user.displayName || confirm.user.username
    const sessions = confirm.user.sessions ?? 0
    if (confirm.action === 'delete') {
      return {
        title: 'Delete this account',
        danger: true,
        label: 'Delete account',
        message: (
          <div className="space-y-2">
            <p>
              {name} will no longer be able to sign in. Their share links, pinned folders and recycle bin
              contents are removed with the account.
            </p>
            <p>Files on disk are left exactly where they are.</p>
            {sessions > 0 && <p>{counted(sessions, 'signed in browser')} will be signed out right away.</p>}
          </div>
        ),
      }
    }
    if (confirm.action === 'disable') {
      return {
        title: 'Disable this account',
        danger: true,
        label: 'Disable account',
        message: (
          <div className="space-y-2">
            <p>{name} will not be able to sign in until you enable the account again. Nothing is deleted.</p>
            {sessions > 0 ? (
              <p>{counted(sessions, 'signed in browser')} will be signed out right away.</p>
            ) : (
              <p>There are no signed in browsers for this account right now.</p>
            )}
          </div>
        ),
      }
    }
    return {
      title: 'Enable this account',
      danger: false,
      label: 'Enable account',
      message: <p>{name} will be able to sign in again with the same password and the same folders.</p>,
    }
  })()

  return (
    <div className="flex h-full min-h-0 flex-col">
      <header className="flex flex-wrap items-center gap-3 border-b border-line px-6 py-4">
        <div className="min-w-0 flex-1">
          <h1 className="text-lg font-semibold text-ink">People</h1>
          <p className="mt-0.5 text-sm text-muted">Accounts that can sign in to this server.</p>
        </div>
        <div className="w-full sm:w-64">
          <Field
            icon="search"
            aria-label="Search accounts"
            placeholder="Search accounts"
            value={search}
            onChange={(event) => setSearch(event.target.value)}
          />
        </div>
        <Button
          variant="primary"
          icon="plus"
          onClick={() => {
            setEditing(null)
            setDialogOpen(true)
          }}
        >
          Add user
        </Button>
      </header>

      <div className="sx-scroll flex-1 p-6">
        <div className="sx-panel overflow-hidden">
          <div className="overflow-x-auto">
            <table className="w-full min-w-[56rem] border-collapse text-left">
              <thead>
                <tr className="text-[11px] uppercase tracking-[0.12em] text-faint">
                  <th className="px-4 py-3 font-semibold">Person</th>
                  <th className="px-4 py-3 font-semibold">Role</th>
                  <th className="px-4 py-3 font-semibold">Folders</th>
                  <th className="px-4 py-3 font-semibold">Two step</th>
                  <th className="px-4 py-3 font-semibold">Last sign in</th>
                  <th className="px-4 py-3 font-semibold">State</th>
                  <th className="w-12 px-4 py-3" />
                </tr>
              </thead>

              {users.isPending ? (
                <LoadingRows />
              ) : users.isError ? (
                <tbody>
                  <tr>
                    <td colSpan={7}>
                      <EmptyState
                        icon="alert"
                        title="The account list could not be loaded"
                        message={explain(users.error)}
                        action={
                          <Button icon="refresh" onClick={() => void users.refetch()}>
                            Try again
                          </Button>
                        }
                      />
                    </td>
                  </tr>
                </tbody>
              ) : rows.length === 0 ? (
                <tbody>
                  <tr>
                    <td colSpan={7}>
                      {search ? (
                        <EmptyState
                          icon="search"
                          title="No accounts match that search"
                          message="Try a different name, username or email address."
                          action={<Button onClick={() => setSearch('')}>Clear search</Button>}
                        />
                      ) : (
                        <EmptyState
                          icon="users"
                          title="No accounts yet"
                          message="Add a person to give someone their own sign in and their own folders."
                          action={
                            <Button
                              variant="primary"
                              icon="plus"
                              onClick={() => {
                                setEditing(null)
                                setDialogOpen(true)
                              }}
                            >
                              Add user
                            </Button>
                          }
                        />
                      )}
                    </td>
                  </tr>
                </tbody>
              ) : (
                <tbody>
                  {rows.map((user) => (
                    <tr key={user.id} className="border-t border-line transition-colors hover:bg-elevated">
                      <td className="px-4 py-3">
                        <div className="flex items-center gap-3">
                          <Avatar user={user} />
                          <div className="min-w-0">
                            <div className="truncate text-sm font-medium text-ink">
                              {user.displayName || user.username}
                              {me?.id === user.id && <span className="ml-2 text-xs text-faint">You</span>}
                            </div>
                            <div className="truncate text-xs text-faint">{user.username}</div>
                          </div>
                        </div>
                      </td>
                      <td className="px-4 py-3">
                        <RoleChip role={user.role} label={roleLabels.get(user.role) ?? user.role} />
                      </td>
                      <td className="px-4 py-3">
                        <FolderCell user={user} />
                      </td>
                      <td className="px-4 py-3">
                        {user.totpEnabled ? (
                          <span className="inline-flex items-center gap-1.5 text-sm text-success">
                            <Icon name="shield" size={14} />
                            On
                          </span>
                        ) : (
                          <span className="text-sm text-faint">Off</span>
                        )}
                      </td>
                      <td className="px-4 py-3 text-sm text-muted">
                        {user.lastLoginAt ? ago(user.lastLoginAt) : 'Never'}
                      </td>
                      <td className="px-4 py-3">
                        {user.active ? (
                          <span className="sx-chip border-success/35 bg-success/12 text-success">Active</span>
                        ) : (
                          <span className="sx-chip">Disabled</span>
                        )}
                      </td>
                      <td className="px-4 py-3 text-right">
                        <IconButton
                          icon="more"
                          label={`Actions for ${user.displayName || user.username}`}
                          onClick={(event) => openMenu(user, event)}
                        />
                      </td>
                    </tr>
                  ))}
                </tbody>
              )}
            </table>
          </div>
        </div>

        {!users.isPending && !users.isError && rows.length > 0 && (
          <p className="mt-3 text-xs text-faint">
            {counted(rows.length, 'account')}. Administrators reach every folder, everyone else reaches only
            what you assign.
          </p>
        )}
      </div>

      {menu && (
        <Menu items={menuItems(menu.user)} x={menu.x} y={menu.y} anchorRight onClose={() => setMenu(null)} />
      )}

      {dialogOpen && (
        <UserDialog
          open
          editing={editing}
          onClose={() => {
            setDialogOpen(false)
            setEditing(null)
          }}
        />
      )}

      {resetting && <ResetPasswordDialog user={resetting} onClose={() => setResetting(null)} />}

      <ConfirmDialog
        open={confirm !== null}
        title={confirmCopy.title}
        danger={confirmCopy.danger}
        confirmLabel={confirmCopy.label}
        busy={setActive.isPending || remove.isPending}
        message={
          <div className="space-y-3">
            {confirmCopy.message}
            {confirmError && (
              <div className="flex items-start gap-2.5 rounded-xl border border-danger/40 bg-danger/10 px-3 py-2.5 text-danger">
                <Icon name="alert" size={16} className="mt-0.5 shrink-0" />
                <span>{confirmError}</span>
              </div>
            )}
          </div>
        }
        onCancel={() => {
          setConfirm(null)
          setConfirmError('')
        }}
        onConfirm={() => {
          if (!confirm) return
          setConfirmError('')
          if (confirm.action === 'delete') remove.mutate(confirm.user.id)
          else setActive.mutate({ id: confirm.user.id, active: confirm.action === 'enable' })
        }}
      />
    </div>
  )
}
