// The settings screen: branding, storage locations, access rules, the public
// address, updates, the security log and what this build is.
// Developed by X Project.

import { useEffect, useMemo, useState, type ReactNode } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ApiError, api } from '../lib/api'
import type { ApiToken, Release, RootFolder, Settings, SortField, ViewMode } from '../lib/types'
import { bytes, dateShort, dateTime, duration, percent, truncateMiddle } from '../lib/format'
import { useSession } from '../lib/session'
import { useApp, type Theme } from '../state/app'
import { Icon, type IconName } from '../components/Icon'
import { TokenDialog } from '../components/TokenDialog'
import {
  Button,
  ConfirmDialog,
  EmptyState,
  Field,
  IconButton,
  Modal,
  Progress,
  SectionTitle,
  Select,
  Skeleton,
  Toggle,
  useToast,
} from '../components/ui'

const GITHUB_URL = 'https://github.com/XProject25/Storix'
const RESTART_COMMAND = 'sudo systemctl restart storix'
const UPDATE_COMMAND = 'sudo storix update'

// ---- helpers ----------------------------------------------------------------

/** explain turns any thrown value into one calm sentence. */
function explain(error: unknown): string {
  if (error instanceof ApiError) return error.detail ? `${error.message}. ${error.detail}` : error.message
  if (error instanceof Error && error.message) return error.message
  return 'Something went wrong. Try again.'
}

/** cloneSettings makes an editable copy so the form never mutates the cache. */
function cloneSettings(value: Settings): Settings {
  return {
    branding: { ...value.branding },
    security: { ...value.security, ipAllowlist: [...value.security.ipAllowlist] },
    limits: { ...value.limits },
    updates: { ...value.updates },
    server: { ...value.server },
    trash: { ...value.trash },
  }
}

/** SaveEnvelope is what the server answers with, which wraps the document. */
interface SaveEnvelope extends Settings {
  settings?: Settings
}

const ICON_CHOICES: IconName[] = [
  'drive',
  'folder',
  'home',
  'server',
  'database',
  'archive',
  'image',
  'video',
  'music',
  'code',
  'globe',
  'star',
]

/** safeIcon keeps an unknown stored icon name from breaking the rail. */
function safeIcon(name: string): IconName {
  return (ICON_CHOICES as string[]).includes(name) ? (name as IconName) : 'drive'
}

/** copy puts text on the clipboard when the browser allows it. */
async function copyText(value: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(value)
    return true
  } catch {
    return false
  }
}

// ---- small building blocks ---------------------------------------------------

function Notice({
  tone,
  icon,
  children,
}: {
  tone: 'info' | 'warning' | 'danger' | 'success'
  icon: IconName
  children: ReactNode
}) {
  const skin =
    tone === 'danger'
      ? 'border-danger/40 bg-danger/10 text-danger'
      : tone === 'warning'
        ? 'border-warning/40 bg-warning/10 text-warning'
        : tone === 'success'
          ? 'border-success/40 bg-success/10 text-success'
          : 'border-line bg-elevated text-muted'
  return (
    <div className={`flex items-start gap-2.5 rounded-xl border px-3 py-2.5 text-sm ${skin}`}>
      <Icon name={icon} size={16} className="mt-0.5 shrink-0" />
      <div className="min-w-0 flex-1">{children}</div>
    </div>
  )
}

function CommandLine({ command }: { command: string }) {
  const toast = useToast()
  return (
    <div className="flex items-center gap-2 rounded-xl border border-line bg-elevated px-3 py-2">
      <Icon name="terminal" size={15} className="shrink-0 text-faint" />
      <code className="flex-1 truncate font-mono text-xs text-ink">{command}</code>
      <IconButton
        icon="copy"
        label="Copy command"
        size={15}
        className="h-7 w-7"
        onClick={() => {
          void copyText(command).then((ok) =>
            ok ? toast.success('Command copied') : toast.error('The browser blocked the clipboard'),
          )
        }}
      />
    </div>
  )
}

function Row({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="flex flex-wrap items-baseline justify-between gap-x-6 gap-y-1 border-b border-line py-2.5 last:border-0">
      <span className="text-sm text-muted">{label}</span>
      <span className="min-w-0 max-w-full break-all text-right text-sm text-ink">{children}</span>
    </div>
  )
}

// ---- server directory picker -------------------------------------------------

function ServerFolderDialog({ onClose }: { onClose: () => void }) {
  const client = useQueryClient()
  const toast = useToast()
  const [path, setPath] = useState('/')
  const [typed, setTyped] = useState('')
  const [label, setLabel] = useState('')
  const [readOnly, setReadOnly] = useState(false)
  const [error, setError] = useState('')

  const browse = useQuery({ queryKey: ['browse', path], queryFn: () => api.browseServer(path) })

  const add = useMutation({
    mutationFn: () => api.addRoot({ path, label: label.trim(), readOnly }),
    onSuccess: async () => {
      await client.invalidateQueries({ queryKey: ['roots'] })
      toast.success('Folder added', 'It is now available in Storix.')
      onClose()
    },
    onError: (failure: unknown) => setError(explain(failure)),
  })

  const dirs = browse.data?.dirs ?? []
  const atRoot = path === '/' || path === browse.data?.parent

  return (
    <Modal
      open
      onClose={onClose}
      width={640}
      icon="folder-plus"
      title="Add a folder"
      description="Choose a folder on this server. Storix shows its contents to the people you give access to."
      footer={
        <>
          <Button onClick={onClose} disabled={add.isPending}>
            Cancel
          </Button>
          <Button variant="primary" loading={add.isPending} onClick={() => add.mutate()}>
            Add this folder
          </Button>
        </>
      }
    >
      <div className="space-y-4">
        {error && (
          <Notice tone="danger" icon="alert">
            {error}
          </Notice>
        )}

        <div className="flex items-center gap-2">
          <IconButton
            icon="arrow-up"
            label="Go to the folder above"
            disabled={atRoot}
            onClick={() => setPath(browse.data?.parent ?? '/')}
          />
          <div className="flex h-9 min-w-0 flex-1 items-center rounded-xl border border-line bg-elevated px-3">
            <code className="truncate font-mono text-xs text-ink">{path}</code>
          </div>
          <IconButton icon="refresh" label="Reload this folder" onClick={() => void browse.refetch()} />
        </div>

        <div className="sx-scroll h-64 rounded-xl border border-line">
          {browse.isPending ? (
            <div className="space-y-2 p-3">
              {[0, 1, 2, 3, 4, 5].map((index) => (
                <Skeleton key={index} className="h-8 w-full" />
              ))}
            </div>
          ) : browse.isError ? (
            <EmptyState
              icon="alert"
              title="That folder could not be opened"
              message={explain(browse.error)}
              action={<Button onClick={() => setPath('/')}>Start again at the top</Button>}
            />
          ) : dirs.length === 0 ? (
            <EmptyState
              icon="folder-open"
              title="No folders inside this one"
              message="You can still add this folder itself, or go back up and choose another."
            />
          ) : (
            <div className="p-1.5">
              {dirs.map((dir) => (
                <button
                  key={dir.path}
                  type="button"
                  disabled={!dir.readable}
                  className="flex h-9 w-full items-center gap-2.5 rounded-xl px-2.5 text-left text-sm text-ink transition-colors hover:bg-elevated disabled:opacity-45"
                  onClick={() => {
                    setPath(dir.path)
                    setError('')
                  }}
                >
                  <Icon name="folder" size={16} className="shrink-0 text-primary" />
                  <span className="flex-1 truncate">{dir.name}</span>
                  {dir.readable ? (
                    <Icon name="chevron-right" size={15} className="text-faint" />
                  ) : (
                    <span className="text-xs text-faint">No permission</span>
                  )}
                </button>
              ))}
            </div>
          )}
        </div>

        <div className="flex items-end gap-2">
          <Field
            label="Or type a path"
            className="flex-1"
            placeholder="/srv/media"
            value={typed}
            spellCheck={false}
            onChange={(event) => setTyped(event.target.value)}
            onKeyDown={(event) => {
              if (event.key !== 'Enter') return
              event.preventDefault()
              if (typed.trim()) setPath(typed.trim())
            }}
          />
          <Button
            onClick={() => {
              if (typed.trim()) setPath(typed.trim())
            }}
          >
            Go
          </Button>
        </div>

        <div className="grid gap-4 sm:grid-cols-2">
          <Field
            label="Name in Storix"
            placeholder="Media"
            hint="Leave empty to use the folder name."
            value={label}
            onChange={(event) => setLabel(event.target.value)}
          />
          <div className="flex items-end pb-2">
            <Toggle
              checked={readOnly}
              onChange={setReadOnly}
              label="Read only"
              hint="Nobody can change anything inside it."
            />
          </div>
        </div>
      </div>
    </Modal>
  )
}

// ---- one storage location ----------------------------------------------------

function RootCard({ root, onRemove }: { root: RootFolder; onRemove: () => void }) {
  const client = useQueryClient()
  const toast = useToast()
  const [label, setLabel] = useState(root.label)

  useEffect(() => setLabel(root.label), [root.label])

  const update = useMutation({
    mutationFn: (body: Record<string, unknown>) => api.updateRoot(root.id, body),
    onSuccess: async () => {
      await client.invalidateQueries({ queryKey: ['roots'] })
    },
    onError: (failure: unknown) => toast.error('That change was not saved', explain(failure)),
  })

  const usage = root.usage

  return (
    <div className="sx-panel space-y-4 p-4">
      <div className="flex items-start gap-3">
        <span className="flex h-10 w-10 shrink-0 items-center justify-center rounded-xl bg-primary/12 text-primary">
          <Icon name={safeIcon(root.icon)} size={19} />
        </span>
        <div className="min-w-0 flex-1">
          <input
            className="sx-input"
            aria-label="Folder name in Storix"
            value={label}
            onChange={(event) => setLabel(event.target.value)}
            onBlur={() => {
              const next = label.trim()
              if (next && next !== root.label) update.mutate({ label: next })
              else setLabel(root.label)
            }}
          />
          <p className="mt-1.5 truncate font-mono text-xs text-faint" title={root.path}>
            {root.path}
          </p>
        </div>
        <Button variant="danger" icon="trash" onClick={onRemove}>
          Remove
        </Button>
      </div>

      {root.exists === false && (
        <Notice tone="warning" icon="alert">
          There is no folder at this path any more. Storix cannot show anything from it until the folder is
          back.
        </Notice>
      )}

      <div className="grid gap-4 sm:grid-cols-2">
        <Select
          label="Icon"
          value={safeIcon(root.icon)}
          options={ICON_CHOICES.map((name) => ({ value: name, label: name.replace(/-/g, ' ') }))}
          onChange={(value) => update.mutate({ icon: value })}
        />
        <div className="flex items-end pb-2">
          <Toggle
            checked={root.readOnly}
            onChange={(value) => update.mutate({ readOnly: value })}
            label="Read only"
            hint="Everyone can look, nobody can change anything."
          />
        </div>
      </div>

      {usage && usage.total > 0 && (
        <div>
          <Progress value={usage.percent} />
          <div className="mt-1.5 flex flex-wrap items-center justify-between gap-2 text-xs text-faint">
            <span>
              {bytes(usage.used)} of {bytes(usage.total)} used
            </span>
            <span>
              {bytes(usage.free)} free, {percent(usage.percent)} full
            </span>
          </div>
        </div>
      )}
    </div>
  )
}

// ---- tabs --------------------------------------------------------------------

type TabId = 'general' | 'folders' | 'access' | 'domain' | 'tokens' | 'updates' | 'audit' | 'about'

const TABS: Array<{ id: TabId; label: string; icon: IconName }> = [
  { id: 'general', label: 'General', icon: 'settings' },
  { id: 'folders', label: 'Folders', icon: 'drive' },
  { id: 'access', label: 'Access', icon: 'shield' },
  { id: 'domain', label: 'Domain and HTTPS', icon: 'globe' },
  { id: 'tokens', label: 'Network drive and tokens', icon: 'key' },
  { id: 'updates', label: 'Updates', icon: 'download' },
  { id: 'audit', label: 'Security log', icon: 'activity' },
  { id: 'about', label: 'About', icon: 'info' },
]

const AUDIT_ACTIONS = [
  'auth.login',
  'auth.logout',
  'user.create',
  'user.update',
  'user.delete',
  'root.add',
  'root.update',
  'root.remove',
  'settings.save',
  'system.domain',
  'system.update',
]

// ---- page --------------------------------------------------------------------

export default function SettingsPage() {
  const { can, version } = useSession()
  const client = useQueryClient()
  const toast = useToast()

  const [tab, setTab] = useState<TabId>('general')
  const [draft, setDraft] = useState<Settings | null>(null)
  const [saveError, setSaveError] = useState('')
  const [dropped, setDropped] = useState<string[]>([])
  const [restart, setRestart] = useState('')

  const settings = useQuery({ queryKey: ['settings'], queryFn: api.settings })

  useEffect(() => {
    if (settings.data) setDraft(cloneSettings(settings.data))
  }, [settings.data])

  const save = useMutation({
    mutationFn: (body: Partial<Settings>) => api.saveSettings(body),
    onSuccess: (result, body) => {
      const envelope = result as SaveEnvelope
      const saved = envelope.settings ?? envelope
      const needsRestart = envelope.restartRequired ?? saved.restartRequired ?? false

      client.setQueryData(['settings'], saved)
      setDraft(cloneSettings(saved))
      setSaveError('')

      // The server is allowed to keep its own value. Say so instead of showing
      // a form that quietly disagrees with the machine.
      const kept: string[] = []
      const sentBranding = body.branding
      if (sentBranding && sentBranding.name !== saved.branding.name) kept.push('Product name')
      if (sentBranding && sentBranding.tagline !== saved.branding.tagline) kept.push('Tagline')
      const sentSecurity = body.security
      if (sentSecurity) {
        if (sentSecurity.sessionTtlHours !== saved.security.sessionTtlHours) kept.push('Session lifetime')
        if (sentSecurity.loginRateBurst !== saved.security.loginRateBurst) kept.push('Sign in attempt limit')
        if (sentSecurity.allowAdvanced !== saved.security.allowAdvanced) kept.push('Advanced Unix controls')
        if (sentSecurity.ipAllowlist.join('|') !== saved.security.ipAllowlist.join('|'))
          kept.push('Address allowlist')
      }
      const sentUpdates = body.updates
      if (sentUpdates) {
        if (sentUpdates.channel !== saved.updates.channel) kept.push('Update channel')
        if (sentUpdates.autoCheck !== saved.updates.autoCheck) kept.push('Automatic update check')
      }
      setDropped(kept)
      setRestart(needsRestart ? RESTART_COMMAND : '')
      toast.success('Saved', needsRestart ? 'One change needs a restart to take effect.' : undefined)
    },
    onError: (failure: unknown) => setSaveError(explain(failure)),
  })

  const dirty = useMemo(() => {
    if (!draft || !settings.data) return false
    const a = draft
    const b = settings.data
    return (
      a.branding.name !== b.branding.name ||
      a.branding.tagline !== b.branding.tagline ||
      a.security.sessionTtlHours !== b.security.sessionTtlHours ||
      a.security.loginRateBurst !== b.security.loginRateBurst ||
      a.security.allowAdvanced !== b.security.allowAdvanced ||
      a.security.ipAllowlist.join('|') !== b.security.ipAllowlist.join('|') ||
      a.updates.channel !== b.updates.channel ||
      a.updates.autoCheck !== b.updates.autoCheck
    )
  }, [draft, settings.data])

  if (!can('settings')) {
    return (
      <div className="flex h-full items-center justify-center p-6">
        <EmptyState
          icon="lock"
          title="You cannot change settings"
          message="Ask an administrator to give you the settings permission if you need this screen."
        />
      </div>
    )
  }

  function patchDraft(update: (current: Settings) => Settings) {
    setDraft((current) => (current ? update(current) : current))
  }

  function submit() {
    if (!draft) return
    setSaveError('')
    setDropped([])
    save.mutate({
      branding: draft.branding,
      security: draft.security,
      updates: draft.updates,
    })
  }

  const showSaveBar = dirty && (tab === 'general' || tab === 'access' || tab === 'updates')

  return (
    <div className="flex h-full min-h-0 flex-col">
      <header className="border-b border-line px-6 py-4">
        <h1 className="text-lg font-semibold text-ink">Settings</h1>
        <p className="mt-0.5 text-sm text-muted">How this Storix server looks, who may reach it and what it can see.</p>
      </header>

      <div className="flex min-h-0 flex-1 flex-col md:flex-row">
        <nav
          aria-label="Settings sections"
          className="flex shrink-0 gap-1 overflow-x-auto border-b border-line p-3 md:w-60 md:flex-col md:overflow-x-visible md:border-b-0 md:border-r"
        >
          {TABS.map((item) => (
            <button
              key={item.id}
              type="button"
              className="sx-nav-item shrink-0"
              data-active={tab === item.id ? 'true' : undefined}
              aria-current={tab === item.id ? 'page' : undefined}
              onClick={() => setTab(item.id)}
            >
              <Icon name={item.icon} size={16} className="shrink-0" />
              <span className="truncate">{item.label}</span>
            </button>
          ))}
        </nav>

        <div className="sx-scroll min-h-0 flex-1">
          <div className="mx-auto max-w-3xl px-6 py-6">
            {settings.isPending && tab !== 'audit' && tab !== 'about' && tab !== 'folders' && tab !== 'tokens' ? (
              <div className="space-y-4">
                <Skeleton className="h-6 w-40" />
                <Skeleton className="h-24 w-full" />
                <Skeleton className="h-24 w-full" />
              </div>
            ) : settings.isError && tab !== 'audit' && tab !== 'about' && tab !== 'folders' && tab !== 'tokens' ? (
              <EmptyState
                icon="alert"
                title="Settings could not be loaded"
                message={explain(settings.error)}
                action={
                  <Button icon="refresh" onClick={() => void settings.refetch()}>
                    Try again
                  </Button>
                }
              />
            ) : (
              <div className="space-y-6">
                {restart && (
                  <Notice tone="warning" icon="refresh">
                    <p className="font-medium">A restart is needed before this takes effect.</p>
                    <div className="mt-2">
                      <CommandLine command={restart} />
                    </div>
                  </Notice>
                )}
                {dropped.length > 0 && (
                  <Notice tone="warning" icon="info">
                    The server kept its own value for {dropped.join(', ')}. The fields above now show what is
                    actually stored.
                  </Notice>
                )}
                {saveError && (
                  <Notice tone="danger" icon="alert">
                    {saveError}
                  </Notice>
                )}

                {tab === 'general' && draft && <GeneralTab draft={draft} patch={patchDraft} />}
                {tab === 'folders' && <FoldersTab />}
                {tab === 'access' && draft && <AccessTab draft={draft} patch={patchDraft} />}
                {tab === 'domain' && draft && <DomainTab draft={draft} />}
                {tab === 'tokens' && <TokensTab />}
                {tab === 'updates' && draft && (
                  <UpdatesTab draft={draft} patch={patchDraft} fallbackVersion={version} />
                )}
                {tab === 'audit' && <AuditTab />}
                {tab === 'about' && <AboutTab />}
              </div>
            )}

            {showSaveBar && (
              <div className="sticky bottom-0 z-10 -mx-6 mt-8 flex flex-wrap items-center gap-3 border-t border-line bg-surface/95 px-6 py-3 backdrop-blur">
                <span className="flex-1 text-sm text-muted">You have unsaved changes.</span>
                <Button
                  onClick={() => {
                    if (settings.data) setDraft(cloneSettings(settings.data))
                    setSaveError('')
                    setDropped([])
                  }}
                  disabled={save.isPending}
                >
                  Discard
                </Button>
                <Button variant="primary" icon="check" loading={save.isPending} onClick={submit}>
                  Save changes
                </Button>
              </div>
            )}
          </div>
        </div>
      </div>
    </div>
  )
}

// ---- general -----------------------------------------------------------------

function GeneralTab({
  draft,
  patch,
}: {
  draft: Settings
  patch: (update: (current: Settings) => Settings) => void
}) {
  const theme = useApp((state) => state.theme)
  const setTheme = useApp((state) => state.setTheme)
  const view = useApp((state) => state.view)
  const setView = useApp((state) => state.setView)
  const sort = useApp((state) => state.sort)
  const order = useApp((state) => state.order)
  const setSort = useApp((state) => state.setSort)

  return (
    <div className="space-y-8">
      <section>
        <SectionTitle>Branding</SectionTitle>
        <div className="sx-panel space-y-4 p-4">
          <Field
            label="Product name"
            value={draft.branding.name}
            placeholder="Storix"
            hint="Shown in the sidebar, the sign in screen and the browser tab."
            onChange={(event) =>
              patch((current) => ({
                ...current,
                branding: { ...current.branding, name: event.target.value },
              }))
            }
          />
          <Field
            label="Tagline"
            value={draft.branding.tagline}
            placeholder="Your server, your files"
            hint="One short line under the name on the sign in screen."
            onChange={(event) =>
              patch((current) => ({
                ...current,
                branding: { ...current.branding, tagline: event.target.value },
              }))
            }
          />
        </div>
      </section>

      <section>
        <SectionTitle>Interface</SectionTitle>
        <div className="sx-panel space-y-4 p-4">
          <p className="text-xs text-faint">These choices apply to your account in this browser.</p>
          <div className="grid gap-4 sm:grid-cols-3">
            <Select
              label="Theme"
              value={theme}
              options={[
                { value: 'dark', label: 'Dark' },
                { value: 'light', label: 'Light' },
              ]}
              onChange={(value) => setTheme(value as Theme)}
            />
            <Select
              label="Default view"
              value={view}
              options={[
                { value: 'list', label: 'List' },
                { value: 'grid', label: 'Grid' },
                { value: 'gallery', label: 'Gallery' },
              ]}
              onChange={(value) => setView(value as ViewMode)}
            />
            <Select
              label="Default sort"
              value={`${sort}:${order}`}
              options={[
                { value: 'name:asc', label: 'Name, A to Z' },
                { value: 'name:desc', label: 'Name, Z to A' },
                { value: 'modified:desc', label: 'Newest first' },
                { value: 'modified:asc', label: 'Oldest first' },
                { value: 'size:desc', label: 'Largest first' },
                { value: 'size:asc', label: 'Smallest first' },
                { value: 'kind:asc', label: 'Kind' },
                { value: 'ext:asc', label: 'Extension' },
              ]}
              onChange={(value) => {
                const [field, direction] = value.split(':')
                setSort(field as SortField, direction === 'desc' ? 'desc' : 'asc')
              }}
            />
          </div>
        </div>
      </section>
    </div>
  )
}

// ---- folders -----------------------------------------------------------------

function FoldersTab() {
  const client = useQueryClient()
  const toast = useToast()
  const [adding, setAdding] = useState(false)
  const [removing, setRemoving] = useState<RootFolder | null>(null)
  const [removeError, setRemoveError] = useState('')

  const roots = useQuery({ queryKey: ['roots'], queryFn: api.roots })

  const remove = useMutation({
    mutationFn: (id: number) => api.deleteRoot(id),
    onSuccess: async () => {
      await client.invalidateQueries({ queryKey: ['roots'] })
      toast.success('Folder removed', 'Nothing on disk was deleted.')
      setRemoving(null)
    },
    onError: (failure: unknown) => setRemoveError(explain(failure)),
  })

  const list = roots.data?.roots ?? []

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-3">
        <div className="min-w-0 flex-1">
          <SectionTitle>Storage locations</SectionTitle>
          <p className="-mt-1 text-sm text-muted">
            The folders on this server that Storix is allowed to open. Everything a person can reach lives
            inside one of them.
          </p>
        </div>
        <Button variant="primary" icon="folder-plus" onClick={() => setAdding(true)}>
          Add folder
        </Button>
      </div>

      {roots.isPending ? (
        <div className="space-y-4">
          {[0, 1].map((index) => (
            <Skeleton key={index} className="h-40 w-full rounded-2xl" />
          ))}
        </div>
      ) : roots.isError ? (
        <EmptyState
          icon="alert"
          title="The folder list could not be loaded"
          message={explain(roots.error)}
          action={
            <Button icon="refresh" onClick={() => void roots.refetch()}>
              Try again
            </Button>
          }
        />
      ) : list.length === 0 ? (
        <EmptyState
          icon="drive"
          title="No folders yet"
          message="Add the first folder so Storix has something to show."
          action={
            <Button variant="primary" icon="folder-plus" onClick={() => setAdding(true)}>
              Add folder
            </Button>
          }
        />
      ) : (
        <div className="space-y-4">
          {list.map((root) => (
            <RootCard
              key={root.id}
              root={root}
              onRemove={() => {
                setRemoveError('')
                setRemoving(root)
              }}
            />
          ))}
        </div>
      )}

      {adding && <ServerFolderDialog onClose={() => setAdding(false)} />}

      <ConfirmDialog
        open={removing !== null}
        danger
        title="Remove this folder from Storix"
        confirmLabel="Remove folder"
        busy={remove.isPending}
        message={
          <div className="space-y-3">
            <p>
              {removing ? removing.label : 'This folder'} disappears from Storix and nobody can browse it here
              any more.
            </p>
            <p className="text-ink">Nothing on disk is deleted. Every file stays exactly where it is.</p>
            {removing && (
              <p className="font-mono text-xs text-faint">{truncateMiddle(removing.path, 60)}</p>
            )}
            {removeError && (
              <Notice tone="danger" icon="alert">
                {removeError}
              </Notice>
            )}
          </div>
        }
        onCancel={() => {
          setRemoving(null)
          setRemoveError('')
        }}
        onConfirm={() => {
          if (removing) remove.mutate(removing.id)
        }}
      />
    </div>
  )
}

// ---- access ------------------------------------------------------------------

function AccessTab({
  draft,
  patch,
}: {
  draft: Settings
  patch: (update: (current: Settings) => Settings) => void
}) {
  const [entry, setEntry] = useState('')

  function addAddress() {
    const value = entry.trim()
    if (!value) return
    if (draft.security.ipAllowlist.includes(value)) {
      setEntry('')
      return
    }
    patch((current) => ({
      ...current,
      security: { ...current.security, ipAllowlist: [...current.security.ipAllowlist, value] },
    }))
    setEntry('')
  }

  function removeAddress(value: string) {
    patch((current) => ({
      ...current,
      security: {
        ...current.security,
        ipAllowlist: current.security.ipAllowlist.filter((item) => item !== value),
      },
    }))
  }

  return (
    <div className="space-y-8">
      <section>
        <SectionTitle>Sessions</SectionTitle>
        <div className="sx-panel grid gap-4 p-4 sm:grid-cols-2">
          <Field
            label="Stay signed in for"
            type="number"
            min={1}
            max={8760}
            value={String(draft.security.sessionTtlHours)}
            hint="Hours before a signed in browser has to sign in again."
            onChange={(event) =>
              patch((current) => ({
                ...current,
                security: { ...current.security, sessionTtlHours: Number(event.target.value) || 0 },
              }))
            }
          />
          <Field
            label="Sign in attempts allowed"
            type="number"
            min={1}
            max={100}
            value={String(draft.security.loginRateBurst)}
            hint="How many tries one address gets before Storix starts slowing it down."
            onChange={(event) =>
              patch((current) => ({
                ...current,
                security: { ...current.security, loginRateBurst: Number(event.target.value) || 0 },
              }))
            }
          />
        </div>
      </section>

      <section>
        <SectionTitle>Addresses allowed to sign in</SectionTitle>
        <div className="sx-panel space-y-3 p-4">
          <p className="text-sm text-muted">
            Leave this empty and anyone who knows the address can reach the sign in screen. Add an address or a
            range and everything else is turned away before the password is even checked.
          </p>
          <div className="flex items-end gap-2">
            <Field
              label="Address or range"
              className="flex-1"
              placeholder="203.0.113.4 or 10.0.0.0/8"
              spellCheck={false}
              value={entry}
              onChange={(event) => setEntry(event.target.value)}
              onKeyDown={(event) => {
                if (event.key !== 'Enter') return
                event.preventDefault()
                addAddress()
              }}
            />
            <Button icon="plus" onClick={addAddress}>
              Add
            </Button>
          </div>
          {draft.security.ipAllowlist.length === 0 ? (
            <p className="text-xs text-faint">Every address is allowed right now.</p>
          ) : (
            <div className="flex flex-wrap gap-2">
              {draft.security.ipAllowlist.map((address) => (
                <span key={address} className="sx-chip pr-1 font-mono">
                  {address}
                  <IconButton
                    icon="close"
                    label={`Remove ${address}`}
                    size={12}
                    className="h-5 w-5"
                    onClick={() => removeAddress(address)}
                  />
                </span>
              ))}
            </div>
          )}
        </div>
      </section>

      <section>
        <SectionTitle>Advanced controls</SectionTitle>
        <div className="sx-panel space-y-3 p-4">
          <Toggle
            checked={draft.security.allowAdvanced}
            onChange={(value) =>
              patch((current) => ({ ...current, security: { ...current.security, allowAdvanced: value } }))
            }
            label="Allow advanced Unix controls"
            hint="Off by default, and safe to leave off."
          />
          <p className="text-sm text-muted">
            Turning this on adds the owner, group and permission editors to the file details panel, so a
            trusted person can change the Unix mode of a file or a whole tree from the browser. Only accounts
            that also hold the advanced permission ever see it. Everyone else keeps the plain view.
          </p>
        </div>
      </section>
    </div>
  )
}

// ---- domain and https ---------------------------------------------------------

function DomainTab({ draft }: { draft: Settings }) {
  const client = useQueryClient()
  const [domain, setDomain] = useState(draft.server.domain)
  const [email, setEmail] = useState('')
  const [error, setError] = useState('')
  const [result, setResult] = useState<{ url: string; message: string } | null>(null)

  const apply = useMutation({
    mutationFn: (enable: boolean) => api.setDomain({ domain: domain.trim(), email: email.trim(), enable }),
    onSuccess: async (response) => {
      setError('')
      setResult({ url: response.url, message: response.message ?? 'The domain was saved.' })
      await client.invalidateQueries({ queryKey: ['settings'] })
    },
    onError: (failure: unknown) => {
      setResult(null)
      setError(explain(failure))
    },
  })

  const secure = draft.server.tlsMode !== 'off'

  return (
    <div className="space-y-8">
      <section>
        <SectionTitle>Public address</SectionTitle>
        <div className="sx-panel space-y-3 p-4">
          <Row label="People reach this server at">
            <a
              href={draft.server.publicUrl}
              target="_blank"
              rel="noreferrer"
              className="inline-flex items-center gap-1.5 font-mono text-primary hover:underline"
            >
              {draft.server.publicUrl}
              <Icon name="external" size={13} />
            </a>
          </Row>
          <Row label="Certificates">{secure ? `On, mode ${draft.server.tlsMode}` : 'Off, plain HTTP'}</Row>
          <Row label="Port">{draft.server.port}</Row>
        </div>
      </section>

      <section>
        <SectionTitle>Use your own domain</SectionTitle>
        <div className="sx-panel space-y-4 p-4">
          <Field
            label="Domain"
            placeholder="files.example.com"
            spellCheck={false}
            value={domain}
            hint="The name on its own, with no https and no path."
            onChange={(event) => setDomain(event.target.value)}
          />
          <Field
            label="Email for the certificate account"
            type="email"
            placeholder="you@example.com"
            value={email}
            hint="The certificate authority sends expiry notices here. It is never shown to anyone else."
            onChange={(event) => setEmail(event.target.value)}
          />

          <div className="rounded-xl border border-line bg-elevated p-3">
            <p className="text-sm font-medium text-ink">Before you turn HTTPS on</p>
            <ul className="mt-2 space-y-2">
              <li className="flex items-start gap-2.5 text-sm text-muted">
                <Icon name="check" size={15} className="mt-0.5 shrink-0 text-primary" />
                <span>
                  The DNS record for that domain already points at this server. Nothing here creates it for
                  you.
                </span>
              </li>
              <li className="flex items-start gap-2.5 text-sm text-muted">
                <Icon name="check" size={15} className="mt-0.5 shrink-0 text-primary" />
                <span>
                  Ports 80 and 443 can be reached from the internet. Port 80 is what proves the domain is
                  yours, port 443 is what serves the site.
                </span>
              </li>
            </ul>
          </div>

          {error && (
            <Notice tone="danger" icon="alert">
              {error}
            </Notice>
          )}

          {result && (
            <Notice tone="success" icon="check-circle">
              <p>{result.message}</p>
              <p className="mt-1.5">
                New address: <span className="font-mono">{result.url}</span>
              </p>
              <p className="mt-2 font-medium">Restart Storix so the change takes effect.</p>
              <div className="mt-2">
                <CommandLine command={RESTART_COMMAND} />
              </div>
            </Notice>
          )}

          <div className="flex flex-wrap items-center gap-2">
            <Button
              variant="primary"
              icon="lock"
              loading={apply.isPending && apply.variables === true}
              disabled={!domain.trim()}
              onClick={() => apply.mutate(true)}
            >
              Enable HTTPS
            </Button>
            <Button
              loading={apply.isPending && apply.variables === false}
              disabled={!domain.trim()}
              onClick={() => apply.mutate(false)}
            >
              Save the domain without HTTPS
            </Button>
          </div>
        </div>
      </section>
    </div>
  )
}

// ---- network drive and tokens -------------------------------------------------

// A token this close to running out is worth pointing at.
const TOKEN_WARNING_WINDOW = 7 * 24 * 60 * 60 * 1000

function DriveCommand({ platform, command }: { platform: string; command: string }) {
  const toast = useToast()
  return (
    <div className="flex items-start gap-3 rounded-xl border border-line bg-elevated px-3 py-2">
      <span className="mt-1 w-16 shrink-0 text-xs font-medium text-faint">{platform}</span>
      <code className="min-w-0 flex-1 break-words py-0.5 font-mono text-xs leading-5 text-ink">{command}</code>
      <IconButton
        icon="copy"
        label={`Copy the ${platform} line`}
        size={15}
        className="h-7 w-7 shrink-0"
        onClick={() => {
          void copyText(command).then((ok) =>
            ok ? toast.success('Command copied') : toast.error('The browser blocked the clipboard'),
          )
        }}
      />
    </div>
  )
}

/** TokenExpiry says when a token runs out, and only raises its voice near the end. */
function TokenExpiry({ token }: { token: ApiToken }) {
  if (!token.expiresAt) return <span className="text-sm text-muted">Never</span>
  const at = new Date(token.expiresAt)
  if (Number.isNaN(at.getTime())) return <span className="text-sm text-muted">Never</span>
  const left = at.getTime() - Date.now()
  if (token.expired || left <= 0) return <span className="sx-chip text-faint">Expired</span>
  if (left <= TOKEN_WARNING_WINDOW) {
    return <span className="sx-chip border-warning/35 bg-warning/12 text-warning">{dateShort(at)}</span>
  }
  return <span className="text-sm text-muted">{dateShort(at)}</span>
}

function TokensTab() {
  const client = useQueryClient()
  const toast = useToast()
  const [creating, setCreating] = useState(false)
  const [revoking, setRevoking] = useState<ApiToken | null>(null)
  const [revokeError, setRevokeError] = useState('')

  const tokens = useQuery({ queryKey: ['tokens'], queryFn: api.tokens })

  const revoke = useMutation({
    mutationFn: (id: number) => api.deleteToken(id),
    onSuccess: async () => {
      await client.invalidateQueries({ queryKey: ['tokens'] })
      toast.success('Token revoked', 'Anything that was using it has stopped working.')
      setRevoking(null)
    },
    onError: (failure: unknown) => setRevokeError(explain(failure)),
  })

  const webdav = tokens.data?.webdav
  const list = tokens.data?.tokens ?? []

  return (
    <div className="space-y-8">
      <section>
        <SectionTitle>Network drive</SectionTitle>
        <div className="sx-panel space-y-3 p-4">
          <p className="text-sm text-muted">
            Storix can be mounted as a drive on your computer, so files are dragged in and out with Explorer or
            the Finder without a browser open.
          </p>
          <p className="text-sm text-muted">
            Every folder you can reach here is inside it, and nothing is copied to the machine until you open a
            file.
          </p>

          {tokens.isPending ? (
            <div className="space-y-2">
              {[0, 1, 2].map((index) => (
                <Skeleton key={index} className="h-10 w-full rounded-xl" />
              ))}
            </div>
          ) : webdav && webdav.enabled ? (
            <>
              <div className="space-y-2">
                <DriveCommand platform="Windows" command={webdav.windows} />
                <DriveCommand platform="macOS" command={webdav.macos} />
                <DriveCommand platform="Linux" command={webdav.linux} />
              </div>
              <Notice tone="info" icon="key">
                Sign in with your username and an access token as the password. A token can be revoked on its
                own, so a lost laptop or a retired script never means changing the account password.
              </Notice>
            </>
          ) : (
            <Notice tone="info" icon="info">
              The network drive is switched off on this server, so there is nothing to mount yet.
            </Notice>
          )}
        </div>
      </section>

      <section>
        <div className="flex flex-wrap items-center gap-3">
          <div className="min-w-0 flex-1">
            <SectionTitle>Access tokens</SectionTitle>
            <p className="-mt-1 text-sm text-muted">
              A token lets a program reach this server without your account password and without a browser.
              Give each one its own name so it can be revoked on its own.
            </p>
          </div>
          <Button variant="primary" icon="plus" onClick={() => setCreating(true)}>
            Create token
          </Button>
        </div>

        <div className="sx-panel mt-4 overflow-hidden">
          <div className="overflow-x-auto">
            <table className="w-full min-w-[46rem] border-collapse text-left">
              <thead>
                <tr className="text-[11px] uppercase tracking-[0.12em] text-faint">
                  <th className="px-4 py-3 font-semibold">Name</th>
                  <th className="px-4 py-3 font-semibold">Scope</th>
                  <th className="px-4 py-3 font-semibold">Created</th>
                  <th className="px-4 py-3 font-semibold">Last used</th>
                  <th className="px-4 py-3 font-semibold">Expires</th>
                  <th className="px-4 py-3 text-right font-semibold">Action</th>
                </tr>
              </thead>
              {tokens.isPending ? (
                <tbody>
                  {[0, 1, 2].map((index) => (
                    <tr key={index} className="border-t border-line">
                      {[0, 1, 2, 3, 4, 5].map((cell) => (
                        <td key={cell} className="px-4 py-3">
                          <Skeleton className="h-4 w-full" />
                        </td>
                      ))}
                    </tr>
                  ))}
                </tbody>
              ) : tokens.isError ? (
                <tbody>
                  <tr>
                    <td colSpan={6}>
                      <EmptyState
                        icon="alert"
                        title="The token list could not be loaded"
                        message={explain(tokens.error)}
                        action={
                          <Button icon="refresh" onClick={() => void tokens.refetch()}>
                            Try again
                          </Button>
                        }
                      />
                    </td>
                  </tr>
                </tbody>
              ) : list.length === 0 ? (
                <tbody>
                  <tr>
                    <td colSpan={6}>
                      <EmptyState
                        icon="key"
                        title="No tokens yet"
                        message="A token is what a script, a backup job, rclone or a mounted drive signs in with. Make one for each of them, give it only the access it needs, and revoke it on its own when it is done."
                        action={
                          <Button variant="primary" icon="plus" onClick={() => setCreating(true)}>
                            Create token
                          </Button>
                        }
                      />
                    </td>
                  </tr>
                </tbody>
              ) : (
                <tbody>
                  {list.map((token) => (
                    <tr key={token.id} className="border-t border-line transition-colors hover:bg-elevated">
                      <td className="px-4 py-3">
                        <div className="text-sm text-ink">{token.name}</div>
                        <div className="mt-0.5 font-mono text-xs text-faint">{token.prefix}</div>
                      </td>
                      <td className="px-4 py-3">
                        {token.scope === 'write' ? (
                          <span className="sx-chip border-primary/35 bg-primary/12 text-primary">
                            Read and write
                          </span>
                        ) : (
                          <span className="sx-chip">Read only</span>
                        )}
                      </td>
                      <td className="whitespace-nowrap px-4 py-3 text-sm text-muted">
                        {dateTime(token.createdAt)}
                      </td>
                      <td className="px-4 py-3">
                        {token.lastUsedAt ? (
                          <>
                            <div className="whitespace-nowrap text-sm text-muted">
                              {dateTime(token.lastUsedAt)}
                            </div>
                            {token.lastUsedIp && (
                              <div className="mt-0.5 font-mono text-xs text-faint">{token.lastUsedIp}</div>
                            )}
                          </>
                        ) : (
                          <span className="text-sm text-faint">Never used</span>
                        )}
                      </td>
                      <td className="whitespace-nowrap px-4 py-3">
                        <TokenExpiry token={token} />
                      </td>
                      <td className="px-4 py-3 text-right">
                        <Button
                          variant="danger"
                          icon="trash"
                          onClick={() => {
                            setRevokeError('')
                            setRevoking(token)
                          }}
                        >
                          Revoke
                        </Button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              )}
            </table>
          </div>
        </div>
      </section>

      <TokenDialog open={creating} onClose={() => setCreating(false)} />

      <ConfirmDialog
        open={revoking !== null}
        danger
        title="Revoke this token"
        confirmLabel="Revoke token"
        busy={revoke.isPending}
        message={
          <div className="space-y-3">
            <p>
              {revoking ? revoking.name : 'This token'} stops working immediately. Any script, backup or
              mounted drive still using it is refused on its very next request.
            </p>
            <p className="text-ink">
              Your account and your password are untouched, and no file is deleted. If you still need the
              access, create a new token and put that one in its place.
            </p>
            {revokeError && (
              <Notice tone="danger" icon="alert">
                {revokeError}
              </Notice>
            )}
          </div>
        }
        onCancel={() => {
          setRevoking(null)
          setRevokeError('')
        }}
        onConfirm={() => {
          if (revoking) revoke.mutate(revoking.id)
        }}
      />
    </div>
  )
}

// ---- updates -----------------------------------------------------------------

function UpdatesTab({
  draft,
  patch,
  fallbackVersion,
}: {
  draft: Settings
  patch: (update: (current: Settings) => Settings) => void
  fallbackVersion: string
}) {
  const toast = useToast()
  const [release, setRelease] = useState<Release | null>(null)
  const [error, setError] = useState('')

  const info = useQuery({ queryKey: ['systemInfo'], queryFn: api.systemInfo })
  const current = info.data?.build.version || fallbackVersion || 'unknown'

  const check = useMutation({
    mutationFn: () => api.updateCheck(),
    onSuccess: (found) => {
      setError('')
      setRelease(found)
    },
    onError: (failure: unknown) => {
      setRelease(null)
      setError(explain(failure))
    },
  })

  const install = useMutation({
    mutationFn: () => api.applyUpdate(),
    onSuccess: () => toast.success('The update is running', 'You can follow it on the transfers screen.'),
    onError: (failure: unknown) => setError(explain(failure)),
  })

  return (
    <div className="space-y-8">
      <section>
        <SectionTitle>Version</SectionTitle>
        <div className="sx-panel space-y-4 p-4">
          <div className="flex flex-wrap items-center gap-3">
            <span className="flex h-10 w-10 items-center justify-center rounded-xl bg-primary/12 text-primary">
              <Icon name="zap" size={19} />
            </span>
            <div className="min-w-0 flex-1">
              <div className="text-sm text-ink">This server runs version {current}</div>
              <div className="mt-0.5 text-xs text-faint">
                Channel {draft.updates.channel}
                {release ? (release.available ? `, version ${release.version} is available` : ', up to date') : ''}
              </div>
            </div>
            <Button icon="refresh" loading={check.isPending} onClick={() => check.mutate()}>
              Check for updates
            </Button>
          </div>

          {error && (
            <Notice tone="danger" icon="alert">
              {error}
            </Notice>
          )}

          {release && !release.available && (
            <Notice tone="success" icon="check-circle">
              Storix is already the newest version on the {draft.updates.channel} channel.
            </Notice>
          )}

          {release && release.available && (
            <div className="space-y-4 rounded-xl border border-line p-4">
              <div className="flex flex-wrap items-center gap-3">
                <div className="min-w-0 flex-1">
                  <div className="text-sm font-medium text-ink">Version {release.version}</div>
                  <div className="mt-0.5 text-xs text-faint">
                    {release.publishedAt ? dateTime(release.publishedAt) : 'Release date unknown'}
                    {release.size > 0 ? `, ${bytes(release.size)} download` : ''}
                  </div>
                </div>
                {release.url && (
                  <a
                    href={release.url}
                    target="_blank"
                    rel="noreferrer"
                    className="inline-flex items-center gap-1.5 text-xs font-medium text-primary hover:underline"
                  >
                    Release page
                    <Icon name="external" size={13} />
                  </a>
                )}
              </div>

              {release.notes && (
                <div>
                  <div className="sx-label">What changed</div>
                  <pre className="sx-scroll max-h-64 whitespace-pre-wrap break-words rounded-xl border border-line bg-elevated p-3 font-sans text-sm text-muted">
                    {release.notes}
                  </pre>
                </div>
              )}

              {release.writable ? (
                <div className="flex flex-wrap items-center gap-3">
                  <Button
                    variant="primary"
                    icon="download"
                    loading={install.isPending}
                    onClick={() => install.mutate()}
                  >
                    Install version {release.version}
                  </Button>
                  <span className="text-xs text-faint">
                    Storix downloads it in the background, then asks you to restart.
                  </span>
                </div>
              ) : (
                <div className="space-y-2">
                  <p className="text-sm text-muted">
                    This copy of Storix cannot replace its own program file, so the update has to be installed
                    from the server itself.
                  </p>
                  <CommandLine command={UPDATE_COMMAND} />
                </div>
              )}

              {release.message && <p className="text-xs text-faint">{release.message}</p>}
            </div>
          )}
        </div>
      </section>

      <section>
        <SectionTitle>How Storix checks</SectionTitle>
        <div className="sx-panel space-y-4 p-4">
          <Select
            label="Release channel"
            value={draft.updates.channel}
            options={[
              { value: 'stable', label: 'Stable, tested releases only' },
              { value: 'beta', label: 'Beta, early releases' },
            ]}
            onChange={(value) =>
              patch((currentDraft) => ({ ...currentDraft, updates: { ...currentDraft.updates, channel: value } }))
            }
          />
          <Toggle
            checked={draft.updates.autoCheck}
            onChange={(value) =>
              patch((currentDraft) => ({
                ...currentDraft,
                updates: { ...currentDraft.updates, autoCheck: value },
              }))
            }
            label="Look for new versions on its own"
            hint="Storix asks the release service now and then and tells you when something is waiting. It never installs anything without you."
          />
        </div>
      </section>
    </div>
  )
}

// ---- security log ------------------------------------------------------------

function AuditTab() {
  const [action, setAction] = useState('')
  const [search, setSearch] = useState('')
  const [applied, setApplied] = useState('')
  const [limit, setLimit] = useState(100)

  useEffect(() => {
    const timer = window.setTimeout(() => setApplied(search.trim()), 300)
    return () => window.clearTimeout(timer)
  }, [search])

  const audit = useQuery({
    queryKey: ['audit', action, applied, limit],
    queryFn: () => api.audit({ limit, action: action || undefined, q: applied || undefined }),
  })

  const entries = audit.data?.entries ?? []
  const total = audit.data?.total ?? 0

  const actionOptions = useMemo(() => {
    const seen = new Set(AUDIT_ACTIONS)
    for (const entry of entries) seen.add(entry.action)
    return [
      { value: '', label: 'Every action' },
      ...Array.from(seen)
        .sort()
        .map((value) => ({ value, label: value })),
    ]
  }, [entries])

  return (
    <div className="space-y-4">
      <div>
        <SectionTitle>Security log</SectionTitle>
        <p className="-mt-1 text-sm text-muted">
          Every sign in, account change and settings change, with the address it came from.
        </p>
      </div>

      <div className="flex flex-wrap items-end gap-3">
        <Select
          label="Action"
          className="w-56"
          value={action}
          options={actionOptions}
          onChange={(value) => {
            setAction(value)
            setLimit(100)
          }}
        />
        <Field
          label="Search"
          className="min-w-[12rem] flex-1"
          icon="search"
          placeholder="Person, target or address"
          value={search}
          onChange={(event) => setSearch(event.target.value)}
        />
        <Button icon="refresh" onClick={() => void audit.refetch()}>
          Refresh
        </Button>
      </div>

      <div className="sx-panel overflow-hidden">
        <div className="overflow-x-auto">
          <table className="w-full min-w-[52rem] border-collapse text-left">
            <thead>
              <tr className="text-[11px] uppercase tracking-[0.12em] text-faint">
                <th className="px-4 py-3 font-semibold">When</th>
                <th className="px-4 py-3 font-semibold">Person</th>
                <th className="px-4 py-3 font-semibold">Action</th>
                <th className="px-4 py-3 font-semibold">Target</th>
                <th className="px-4 py-3 font-semibold">Address</th>
                <th className="px-4 py-3 font-semibold">Result</th>
              </tr>
            </thead>
            {audit.isPending ? (
              <tbody>
                {[0, 1, 2, 3, 4, 5].map((index) => (
                  <tr key={index} className="border-t border-line">
                    {[0, 1, 2, 3, 4, 5].map((cell) => (
                      <td key={cell} className="px-4 py-3">
                        <Skeleton className="h-4 w-full" />
                      </td>
                    ))}
                  </tr>
                ))}
              </tbody>
            ) : audit.isError ? (
              <tbody>
                <tr>
                  <td colSpan={6}>
                    <EmptyState
                      icon="alert"
                      title="The log could not be loaded"
                      message={explain(audit.error)}
                      action={
                        <Button icon="refresh" onClick={() => void audit.refetch()}>
                          Try again
                        </Button>
                      }
                    />
                  </td>
                </tr>
              </tbody>
            ) : entries.length === 0 ? (
              <tbody>
                <tr>
                  <td colSpan={6}>
                    <EmptyState
                      icon="activity"
                      title="Nothing recorded yet"
                      message={
                        action || applied
                          ? 'No entry matches this filter. Try a wider one.'
                          : 'Sign ins and administrative changes will show up here as they happen.'
                      }
                      action={
                        action || applied ? (
                          <Button
                            onClick={() => {
                              setAction('')
                              setSearch('')
                            }}
                          >
                            Clear the filter
                          </Button>
                        ) : undefined
                      }
                    />
                  </td>
                </tr>
              </tbody>
            ) : (
              <tbody>
                {entries.map((entry) => (
                  <tr key={entry.id} className="border-t border-line transition-colors hover:bg-elevated">
                    <td className="whitespace-nowrap px-4 py-3 text-sm text-muted">{dateTime(entry.at)}</td>
                    <td className="px-4 py-3 text-sm text-ink">{entry.username || 'System'}</td>
                    <td className="px-4 py-3">
                      <span className="font-mono text-xs text-muted">{entry.action}</span>
                    </td>
                    <td className="max-w-[18rem] px-4 py-3">
                      <span className="block truncate text-sm text-muted" title={entry.target}>
                        {entry.target || '-'}
                      </span>
                      {entry.detail && (
                        <span className="mt-0.5 block truncate text-xs text-faint" title={entry.detail}>
                          {entry.detail}
                        </span>
                      )}
                    </td>
                    <td className="whitespace-nowrap px-4 py-3 font-mono text-xs text-faint">{entry.ip}</td>
                    <td className="px-4 py-3">
                      {entry.ok ? (
                        <span className="sx-chip border-success/35 bg-success/12 text-success">Ok</span>
                      ) : (
                        <span className="sx-chip border-danger/35 bg-danger/12 text-danger">Failed</span>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            )}
          </table>
        </div>
      </div>

      {!audit.isPending && !audit.isError && entries.length > 0 && (
        <div className="flex flex-wrap items-center gap-3">
          <span className="flex-1 text-xs text-faint">
            Showing {entries.length} of {total.toLocaleString()} entries.
          </span>
          {entries.length < total && limit < 500 && (
            <Button icon="chevron-down" onClick={() => setLimit((value) => Math.min(500, value + 100))}>
              Show more
            </Button>
          )}
        </div>
      )}
    </div>
  )
}

// ---- about -------------------------------------------------------------------

function AboutTab() {
  const info = useQuery({ queryKey: ['systemInfo'], queryFn: api.systemInfo })

  if (info.isPending) {
    return (
      <div className="space-y-4">
        <Skeleton className="h-6 w-40" />
        <Skeleton className="h-56 w-full rounded-2xl" />
      </div>
    )
  }
  if (info.isError || !info.data) {
    return (
      <EmptyState
        icon="alert"
        title="Build information could not be loaded"
        message={explain(info.error)}
        action={
          <Button icon="refresh" onClick={() => void info.refetch()}>
            Try again
          </Button>
        }
      />
    )
  }

  const data = info.data
  const build = data.build

  return (
    <div className="space-y-8">
      <section>
        <SectionTitle>This build</SectionTitle>
        <div className="sx-panel p-4">
          <Row label="Product">{build.product}</Row>
          <Row label="Version">{build.version}</Row>
          <Row label="Channel">{build.channel || 'stable'}</Row>
          <Row label="Built on">{build.date ? dateTime(build.date) : 'unknown'}</Row>
          <Row label="Commit">
            <span className="font-mono">{build.commit || 'unknown'}</span>
          </Row>
          <Row label="Platform">{build.platform}</Row>
          <Row label="Go">{build.goVersion}</Row>
          <Row label="Running for">{duration(data.uptime)}</Row>
          <Row label="Public address">
            <span className="font-mono">{data.publicUrl}</span>
          </Row>
        </div>
      </section>

      {data.host && (
        <section>
          <SectionTitle>Host</SectionTitle>
          <div className="sx-panel p-4">
            <Row label="Hostname">{data.host.hostname}</Row>
            <Row label="System">
              {data.host.os} on {data.host.arch}
            </Row>
            <Row label="Processors">{data.host.cpus}</Row>
            <Row label="Active tasks">{data.host.goroutines}</Row>
            {data.memory && (
              <Row label="Memory in use">
                {bytes(data.memory.alloc)} of {bytes(data.memory.sys)} reserved
              </Row>
            )}
          </div>
        </section>
      )}

      {(data.database || data.counts) && (
        <section>
          <SectionTitle>Database</SectionTitle>
          <div className="sx-panel p-4">
            {data.database && (
              <>
                <Row label="File">
                  <span className="font-mono">{truncateMiddle(data.database.path, 48)}</span>
                </Row>
                <Row label="Size">{bytes(data.database.bytes)}</Row>
              </>
            )}
            {data.counts && (
              <>
                <Row label="Accounts">{data.counts.users}</Row>
                <Row label="Share links">{data.counts.shares}</Row>
                <Row label="Recorded operations">{data.counts.jobs}</Row>
              </>
            )}
          </div>
        </section>
      )}

      <section>
        <div className="sx-panel flex flex-wrap items-center gap-4 p-4">
          <div className="min-w-0 flex-1">
            <div className="text-sm font-medium text-ink">Storix</div>
            <p className="mt-0.5 text-sm text-muted">
              A file manager for your own Linux server. Source, releases and issue tracker live on GitHub.
            </p>
            <p className="mt-2 text-xs text-faint">Developed by X Project</p>
          </div>
          <a
            href={GITHUB_URL}
            target="_blank"
            rel="noreferrer"
            className="sx-btn-secondary"
          >
            <Icon name="external" size={16} />
            Open on GitHub
          </a>
        </div>
      </section>
    </div>
  )
}
