// First run wizard. Four questions, one per step, then Storix is ready.
// Developed by X Project.

import clsx from 'clsx'
import { useCallback, useEffect, useState, type FormEvent, type ReactNode } from 'react'
import { useNavigate, useSearchParams } from 'react-router-dom'
import { ApiError, api } from '../lib/api'
import { useSession } from '../lib/session'
import { Icon, type IconName } from '../components/Icon'
import { Logo } from '../components/Logo'
import { Button, Field, IconButton } from '../components/ui'

const STEPS = [
  { id: 'welcome', label: 'Welcome' },
  { id: 'admin', label: 'Administrator' },
  { id: 'folders', label: 'Folders' },
  { id: 'domain', label: 'Domain' },
]

const SUGGESTIONS: Array<{ path: string; hint: string; icon: IconName }> = [
  { path: '/home', hint: 'User folders', icon: 'home' },
  { path: '/var/www', hint: 'Websites', icon: 'globe' },
  { path: '/mnt', hint: 'Mounted disks', icon: 'drive' },
  { path: '/srv', hint: 'Served data', icon: 'server' },
  { path: '/opt', hint: 'Installed software', icon: 'cpu' },
  { path: '/backups', hint: 'Backups', icon: 'archive' },
]

const EMAIL = /^[^\s@]+@[^\s@]+\.[^\s@]+$/
const USERNAME = /^[a-zA-Z][a-zA-Z0-9._-]{1,31}$/
const DOMAIN = /^[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?)+$/

interface Problem {
  field: string
  message: string
}

interface Strength {
  score: number
  label: string
  tone: string
  text: string
}

/** strengthOf scores a password so the meter can show progress, not rules. */
function strengthOf(value: string): Strength {
  if (!value) return { score: 0, label: '', tone: 'bg-line', text: 'text-faint' }
  if (value.length < 8) return { score: 1, label: 'Too short', tone: 'bg-danger', text: 'text-danger' }
  let points = 0
  if (value.length >= 12) points += 1
  if (value.length >= 16) points += 1
  if (/[a-z]/.test(value) && /[A-Z]/.test(value)) points += 1
  if (/\d/.test(value)) points += 1
  if (/[^A-Za-z0-9]/.test(value)) points += 1
  if (points <= 1) return { score: 2, label: 'Weak', tone: 'bg-warning', text: 'text-warning' }
  if (points <= 2) return { score: 3, label: 'Good', tone: 'bg-primary', text: 'text-primary' }
  return { score: 4, label: 'Strong', tone: 'bg-success', text: 'text-success' }
}

/** tidyPath normalises a typed folder into a clean absolute path. */
function tidyPath(value: string): string {
  const trimmed = value.trim().replace(/\s+$/, '')
  if (!trimmed) return ''
  const collapsed = trimmed.replace(/\/{2,}/g, '/')
  if (collapsed.length > 1 && collapsed.endsWith('/')) return collapsed.slice(0, -1)
  return collapsed
}

/** failureOf maps a setup failure onto the step that can fix it. */
function failureOf(error: unknown): Problem & { step: number } {
  if (error instanceof ApiError) {
    switch (error.code) {
      case 'invalid_token':
      case 'unauthorized':
      case 'forbidden':
        return {
          step: 0,
          field: 'token',
          message: 'That setup token is not right. Copy it from the installer output, or read it again on the server.',
        }
      case 'setup_complete':
      case 'already_setup':
        return {
          step: 0,
          field: 'form',
          message: 'Storix has already been set up on this server. Sign in instead.',
        }
      case 'invalid_username':
      case 'user_exists':
        return { step: 1, field: 'username', message: error.message || 'Choose a different username.' }
      case 'weak_password':
        return { step: 1, field: 'password', message: error.message || 'Choose a longer password.' }
      case 'invalid_path':
      case 'not_found':
        return {
          step: 2,
          field: 'folders',
          message: error.message || 'One of these folders could not be opened. Check the paths, then try again.',
        }
      default:
        return { step: 3, field: 'form', message: error.message || 'Setup did not finish. Try again.' }
    }
  }
  return {
    step: 3,
    field: 'form',
    message: 'Storix could not be reached. Check your connection, then try again.',
  }
}

/** Notice is the calm red panel used for anything that blocks the step. */
function Notice({ children }: { children: ReactNode }) {
  return (
    <div
      role="alert"
      className="flex items-start gap-2.5 rounded-xl border border-danger/35 bg-danger/10 px-3 py-2.5 text-sm text-danger"
    >
      <Icon name="alert" size={16} className="mt-0.5 shrink-0" />
      <span className="min-w-0">{children}</span>
    </div>
  )
}

/** Shell centres the wizard on the dark background with the product glow behind it. */
function Shell({ children }: { children: ReactNode }) {
  const { version } = useSession()
  return (
    <div className="sx-scroll relative h-full w-full bg-bg">
      <div aria-hidden="true" className="pointer-events-none fixed inset-0 overflow-hidden">
        <div className="absolute -top-40 left-1/2 h-[32rem] w-[32rem] -translate-x-1/2 rounded-full bg-primary/15 blur-[120px]" />
        <div className="absolute -bottom-48 left-[12%] h-[28rem] w-[28rem] rounded-full bg-accent/18 blur-[120px]" />
        <div className="absolute -right-32 top-1/4 h-[24rem] w-[24rem] rounded-full bg-secondary/12 blur-[120px]" />
      </div>
      <div className="relative flex min-h-full flex-col items-center justify-center px-4 py-10">
        {children}
        <footer className="mt-8 text-center">
          <p className="text-xs text-faint">Developed by X Project</p>
          {version && <p className="mt-1 text-[11px] text-faint/70">Version {version}</p>}
        </footer>
      </div>
    </div>
  )
}

export default function SetupPage() {
  const navigate = useNavigate()
  const { refresh } = useSession()
  const [params] = useSearchParams()

  const linkedToken = params.get('token') ?? ''
  const [token, setToken] = useState(linkedToken)
  const [showToken, setShowToken] = useState(linkedToken === '')

  const [step, setStep] = useState(0)
  const [username, setUsername] = useState('admin')
  const [displayName, setDisplayName] = useState('Administrator')
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [confirm, setConfirm] = useState('')
  const [reveal, setReveal] = useState(false)
  const [folders, setFolders] = useState<string[]>(['/home'])
  const [custom, setCustom] = useState('')
  const [domain, setDomain] = useState('')

  const [problem, setProblem] = useState<Problem | null>(null)
  const [busy, setBusy] = useState(false)
  const [done, setDone] = useState(false)
  const [warning, setWarning] = useState('')

  const strength = strengthOf(password)
  const errorFor = (field: string) => (problem?.field === field ? problem.message : undefined)

  const continueToApp = useCallback(async () => {
    await refresh()
    navigate('/', { replace: true })
  }, [refresh, navigate])

  useEffect(() => {
    if (!done || warning) return
    const timer = window.setTimeout(() => void continueToApp(), 1600)
    return () => window.clearTimeout(timer)
  }, [done, warning, continueToApp])

  const validate = (index: number): Problem | null => {
    if (index === 0) {
      if (!token.trim()) return { field: 'token', message: 'Enter the setup token to continue.' }
      return null
    }
    if (index === 1) {
      if (!USERNAME.test(username.trim())) {
        return {
          field: 'username',
          message: 'Use two or more characters, starting with a letter. Letters, numbers, dots, dashes and underscores are allowed.',
        }
      }
      if (!displayName.trim()) return { field: 'displayName', message: 'Enter the name to show in the interface.' }
      if (email.trim() && !EMAIL.test(email.trim())) {
        return { field: 'email', message: 'That email address does not look right.' }
      }
      if (password.length < 8) return { field: 'password', message: 'Use a password of at least eight characters.' }
      if (confirm !== password) return { field: 'confirm', message: 'The two passwords do not match.' }
      return null
    }
    if (index === 2) {
      if (folders.length === 0) return { field: 'folders', message: 'Choose at least one folder for Storix to manage.' }
      return null
    }
    if (domain.trim() && !DOMAIN.test(domain.trim())) {
      return { field: 'domain', message: 'Enter a domain such as files.example.com, or skip this step.' }
    }
    return null
  }

  const toggleFolder = (path: string) => {
    setProblem(null)
    setFolders((current) => (current.includes(path) ? current.filter((item) => item !== path) : [...current, path]))
  }

  const addCustom = () => {
    const path = tidyPath(custom)
    if (!path.startsWith('/')) {
      setProblem({ field: 'custom', message: 'Enter a full path that starts with a slash, for example /var/data.' })
      return
    }
    if (folders.includes(path)) {
      setProblem({ field: 'custom', message: 'That folder is already on the list.' })
      setCustom('')
      return
    }
    setProblem(null)
    setFolders((current) => [...current, path])
    setCustom('')
  }

  const finish = useCallback(
    async (withDomain: string) => {
      setBusy(true)
      setProblem(null)
      try {
        const result = await api.setup({
          token: token.trim(),
          username: username.trim(),
          password,
          displayName: displayName.trim(),
          email: email.trim(),
          folders,
          domain: withDomain.trim(),
        })
        setWarning(result.warning ?? '')
        setDone(true)
      } catch (failure) {
        const mapped = failureOf(failure)
        setStep(mapped.step)
        setProblem({ field: mapped.field, message: mapped.message })
      } finally {
        setBusy(false)
      }
    },
    [token, username, password, displayName, email, folders],
  )

  const onSubmit = (event: FormEvent) => {
    event.preventDefault()
    const found = validate(step)
    if (found) {
      setProblem(found)
      return
    }
    setProblem(null)
    if (step < STEPS.length - 1) {
      setStep(step + 1)
      return
    }
    void finish(domain)
  }

  const goBack = () => {
    setProblem(null)
    setStep((current) => Math.max(0, current - 1))
  }

  if (done) {
    return (
      <Shell>
        <div className="mb-7 flex flex-col items-center">
          <Logo size={40} />
        </div>
        <div className="sx-panel w-full max-w-[34rem] animate-slide-up p-7">
          <div className="flex flex-col items-center text-center">
            <span className="flex h-14 w-14 items-center justify-center rounded-2xl bg-success/15 text-success">
              <Icon name="check" size={28} strokeWidth={2.4} />
            </span>
            <h1 className="mt-4 text-lg font-semibold text-ink">Storix is ready</h1>
            <p className="mt-1.5 max-w-sm text-sm text-muted">
              Your administrator account is created and the folders you chose are connected.
            </p>
          </div>

          {warning && (
            <div className="mt-5 flex items-start gap-2.5 rounded-xl border border-warning/35 bg-warning/10 px-3 py-2.5 text-sm text-warning">
              <Icon name="info" size={16} className="mt-0.5 shrink-0" />
              <span className="min-w-0">
                <span className="block font-medium">One thing to note</span>
                <span className="mt-0.5 block">{warning}</span>
              </span>
            </div>
          )}

          <div className="mt-6 flex justify-center">
            <Button variant="primary" iconRight="arrow-right" onClick={() => void continueToApp()}>
              Open Storix
            </Button>
          </div>
        </div>
      </Shell>
    )
  }

  return (
    <Shell>
      <div className="mb-7 flex flex-col items-center">
        <Logo size={40} />
      </div>

      <div className="sx-panel w-full max-w-[34rem] animate-slide-up p-6 sm:p-7">
        <div className="mb-6">
          <div className="flex items-center justify-between">
            <span className="text-[11px] font-semibold uppercase tracking-[0.14em] text-faint">
              Step {step + 1} of {STEPS.length}
            </span>
            <span className="text-[11px] text-faint">{STEPS[step].label}</span>
          </div>
          <div className="mt-2.5 flex gap-1.5" aria-hidden="true">
            {STEPS.map((item, index) => (
              <span
                key={item.id}
                className={clsx(
                  'h-1 flex-1 rounded-full transition-colors',
                  index < step ? 'bg-primary/60' : index === step ? 'bg-primary' : 'bg-line',
                )}
              />
            ))}
          </div>
        </div>

        <form onSubmit={onSubmit} noValidate>
          {step === 0 && (
            <div>
              <h1 className="text-[17px] font-semibold text-ink">Welcome to Storix</h1>
              <p className="mt-1.5 text-sm text-muted">
                Storix gives you a fast, private way to browse, upload and share the files on this server from any
                browser.
              </p>

              <div className="mt-6 space-y-4">
                {showToken ? (
                  <Field
                    label="Setup token"
                    icon="key"
                    autoFocus
                    autoComplete="off"
                    autoCapitalize="none"
                    autoCorrect="off"
                    spellCheck={false}
                    placeholder="Paste the token here"
                    value={token}
                    error={errorFor('token')}
                    onChange={(event) => {
                      setToken(event.target.value)
                      setProblem(null)
                    }}
                  />
                ) : (
                  <div className="flex items-center gap-3 rounded-xl border border-success/30 bg-success/10 px-3 py-2.5">
                    <Icon name="check-circle" size={16} className="shrink-0 text-success" />
                    <span className="min-w-0 flex-1 text-sm text-ink">Setup token taken from your link</span>
                    <Button
                      variant="ghost"
                      onClick={() => {
                        setToken('')
                        setShowToken(true)
                      }}
                    >
                      Change
                    </Button>
                  </div>
                )}

                <div className="rounded-xl border border-line bg-elevated px-3.5 py-3">
                  <p className="text-xs font-medium text-ink">Where to find the token</p>
                  <p className="mt-1.5 text-xs leading-relaxed text-muted">
                    The installer prints it when it finishes. To see it again, sign in to the server and run
                    <code className="mx-1 rounded-md bg-surface px-1.5 py-0.5 font-mono text-[11px] text-ink">
                      storix setup-token
                    </code>
                    .
                  </p>
                </div>

                {errorFor('form') && <Notice>{errorFor('form')}</Notice>}
              </div>
            </div>
          )}

          {step === 1 && (
            <div>
              <h1 className="text-[17px] font-semibold text-ink">Create your administrator</h1>
              <p className="mt-1.5 text-sm text-muted">
                This account can reach every folder Storix manages, and it is the account that adds everyone else.
              </p>

              <div className="mt-6 space-y-4">
                <div className="grid gap-4 sm:grid-cols-2">
                  <Field
                    label="Username"
                    icon="user"
                    autoFocus
                    autoComplete="username"
                    autoCapitalize="none"
                    autoCorrect="off"
                    spellCheck={false}
                    value={username}
                    error={errorFor('username')}
                    onChange={(event) => {
                      setUsername(event.target.value)
                      setProblem(null)
                    }}
                  />
                  <Field
                    label="Display name"
                    autoComplete="name"
                    value={displayName}
                    error={errorFor('displayName')}
                    onChange={(event) => {
                      setDisplayName(event.target.value)
                      setProblem(null)
                    }}
                  />
                </div>

                <Field
                  label="Email"
                  type="email"
                  autoComplete="email"
                  autoCapitalize="none"
                  autoCorrect="off"
                  spellCheck={false}
                  placeholder="Optional"
                  hint="Used for password resets and certificate notices. You can add it later."
                  value={email}
                  error={errorFor('email')}
                  onChange={(event) => {
                    setEmail(event.target.value)
                    setProblem(null)
                  }}
                />

                <div className="w-full">
                  <label className="sx-label" htmlFor="storix-setup-password">
                    Password
                  </label>
                  <div className="relative">
                    <span className="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-faint">
                      <Icon name="lock" size={16} />
                    </span>
                    <input
                      id="storix-setup-password"
                      type={reveal ? 'text' : 'password'}
                      autoComplete="new-password"
                      className={clsx('sx-input pl-9 pr-11', errorFor('password') && 'border-danger/60')}
                      value={password}
                      onChange={(event) => {
                        setPassword(event.target.value)
                        setProblem(null)
                      }}
                    />
                    <IconButton
                      icon={reveal ? 'eye-off' : 'eye'}
                      label={reveal ? 'Hide password' : 'Show password'}
                      size={16}
                      tabIndex={-1}
                      className="absolute right-0 top-0"
                      onClick={() => setReveal((current) => !current)}
                    />
                  </div>

                  <div className="mt-2 flex gap-1.5" aria-hidden="true">
                    {[1, 2, 3, 4].map((mark) => (
                      <span
                        key={mark}
                        className={clsx(
                          'h-1 flex-1 rounded-full transition-colors',
                          mark <= strength.score ? strength.tone : 'bg-line',
                        )}
                      />
                    ))}
                  </div>
                  {errorFor('password') ? (
                    <p className="mt-1.5 text-xs text-danger">{errorFor('password')}</p>
                  ) : strength.label ? (
                    <p className={clsx('mt-1.5 text-xs', strength.text)}>Password strength: {strength.label}</p>
                  ) : (
                    <p className="mt-1.5 text-xs text-faint">
                      At least eight characters. A short phrase works well and is easy to remember.
                    </p>
                  )}
                </div>

                <Field
                  label="Confirm password"
                  type={reveal ? 'text' : 'password'}
                  icon="lock"
                  autoComplete="new-password"
                  value={confirm}
                  error={errorFor('confirm')}
                  onChange={(event) => {
                    setConfirm(event.target.value)
                    setProblem(null)
                  }}
                />

                {errorFor('form') && <Notice>{errorFor('form')}</Notice>}
              </div>
            </div>
          )}

          {step === 2 && (
            <div>
              <h1 className="text-[17px] font-semibold text-ink">Choose the folders to manage</h1>
              <p className="mt-1.5 text-sm text-muted">
                Storix only ever reads and writes inside the folders listed here. Everything else on the server stays
                out of reach.
              </p>

              <div className="mt-6 space-y-4">
                <div className="grid gap-2 sm:grid-cols-2">
                  {SUGGESTIONS.map((item) => {
                    const selected = folders.includes(item.path)
                    return (
                      <button
                        key={item.path}
                        type="button"
                        aria-pressed={selected}
                        onClick={() => toggleFolder(item.path)}
                        className={clsx(
                          'flex items-center gap-3 rounded-xl border p-3 text-left transition-colors',
                          selected
                            ? 'border-primary/55 bg-primary/10'
                            : 'border-line bg-elevated hover:border-primary/30',
                        )}
                      >
                        <span
                          className={clsx(
                            'flex h-8 w-8 shrink-0 items-center justify-center rounded-lg',
                            selected ? 'bg-primary/20 text-primary' : 'bg-surface text-muted',
                          )}
                        >
                          <Icon name={item.icon} size={16} />
                        </span>
                        <span className="min-w-0 flex-1">
                          <span className="block truncate font-mono text-[13px] text-ink">{item.path}</span>
                          <span className="mt-0.5 block truncate text-xs text-faint">{item.hint}</span>
                        </span>
                        {selected && <Icon name="check" size={15} className="shrink-0 text-primary" />}
                      </button>
                    )
                  })}
                </div>

                <div className="w-full">
                  <label className="sx-label" htmlFor="storix-setup-path">
                    Add another folder
                  </label>
                  <div className="flex items-start gap-2">
                    <div className="relative flex-1">
                      <span className="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-faint">
                        <Icon name="folder" size={16} />
                      </span>
                      <input
                        id="storix-setup-path"
                        className={clsx('sx-input pl-9 font-mono', errorFor('custom') && 'border-danger/60')}
                        placeholder="/data/projects"
                        autoComplete="off"
                        autoCapitalize="none"
                        autoCorrect="off"
                        spellCheck={false}
                        value={custom}
                        onChange={(event) => {
                          setCustom(event.target.value)
                          setProblem(null)
                        }}
                        onKeyDown={(event) => {
                          // Enter adds the folder, unless the field is empty,
                          // where it means "carry on to the next step".
                          if (event.key !== 'Enter' || !custom.trim()) return
                          event.preventDefault()
                          addCustom()
                        }}
                      />
                    </div>
                    <Button icon="plus" onClick={addCustom} disabled={!custom.trim()}>
                      Add
                    </Button>
                  </div>
                  {errorFor('custom') ? (
                    <p className="mt-1.5 text-xs text-danger">{errorFor('custom')}</p>
                  ) : (
                    <p className="mt-1.5 text-xs text-faint">Type the full path, starting with a slash.</p>
                  )}
                </div>

                <div>
                  <p className="mb-2 text-[11px] font-semibold uppercase tracking-[0.14em] text-faint">
                    Folders Storix will manage
                  </p>
                  {folders.length === 0 ? (
                    <div className="rounded-xl border border-dashed border-line px-3 py-6 text-center text-sm text-faint">
                      Nothing chosen yet. Pick a suggestion above or type a path.
                    </div>
                  ) : (
                    <ul className="space-y-1.5">
                      {folders.map((path) => (
                        <li
                          key={path}
                          className="flex items-center gap-3 rounded-xl border border-line bg-elevated px-3 py-2"
                        >
                          <Icon name="folder" size={16} className="shrink-0 text-primary" />
                          <span className="min-w-0 flex-1 truncate font-mono text-[13px] text-ink">{path}</span>
                          <IconButton
                            icon="close"
                            label={`Remove ${path}`}
                            size={15}
                            onClick={() => toggleFolder(path)}
                          />
                        </li>
                      ))}
                    </ul>
                  )}
                </div>

                {errorFor('folders') && <Notice>{errorFor('folders')}</Notice>}
                {errorFor('form') && <Notice>{errorFor('form')}</Notice>}
              </div>
            </div>
          )}

          {step === 3 && (
            <div>
              <h1 className="text-[17px] font-semibold text-ink">Add a domain</h1>
              <p className="mt-1.5 text-sm text-muted">
                Reach Storix by name instead of by address. This is optional and can be set up at any time.
              </p>

              <div className="mt-6 space-y-4">
                <Field
                  label="Domain"
                  icon="globe"
                  autoFocus
                  autoComplete="off"
                  autoCapitalize="none"
                  autoCorrect="off"
                  spellCheck={false}
                  placeholder="files.example.com"
                  hint="A secure certificate is issued automatically once the domain points to this server."
                  value={domain}
                  error={errorFor('domain')}
                  onChange={(event) => {
                    setDomain(event.target.value)
                    setProblem(null)
                  }}
                />

                {errorFor('form') && <Notice>{errorFor('form')}</Notice>}
              </div>
            </div>
          )}

          <div className="mt-7 flex items-center justify-between gap-3">
            {step > 0 ? (
              <Button variant="ghost" icon="arrow-left" onClick={goBack} disabled={busy}>
                Back
              </Button>
            ) : (
              <span />
            )}
            <div className="flex items-center gap-2">
              {step === STEPS.length - 1 && (
                <Button variant="ghost" onClick={() => void finish('')} disabled={busy}>
                  Skip for now
                </Button>
              )}
              <Button
                type="submit"
                variant="primary"
                loading={busy}
                iconRight={step === STEPS.length - 1 ? undefined : 'arrow-right'}
              >
                {step === STEPS.length - 1 ? 'Finish setup' : 'Continue'}
              </Button>
            </div>
          </div>
        </form>
      </div>
    </Shell>
  )
}
