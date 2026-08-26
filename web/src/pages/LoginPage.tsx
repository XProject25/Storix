// Sign in screen. Credentials first, then a second step when the account has
// two step verification switched on.
// Developed by X Project.

import { useCallback, useEffect, useRef, useState, type FormEvent, type ReactNode } from 'react'
import { useLocation, useNavigate } from 'react-router-dom'
import { ApiError, api } from '../lib/api'
import { useSession } from '../lib/session'
import { Icon } from '../components/Icon'
import { Logo } from '../components/Logo'
import { Button, Checkbox, Field, IconButton } from '../components/ui'

type Step = 'credentials' | 'code'

/**
 * describe turns an API failure into one calm sentence. It never says whether
 * the username exists, only that the pair did not match.
 */
function describe(error: unknown, duringCode: boolean): string {
  if (error instanceof ApiError) {
    switch (error.code) {
      case 'invalid_credentials':
        return duringCode
          ? 'That code is not correct. Check your authenticator app, then try again.'
          : 'That username and password do not match an account.'
      case 'invalid_totp':
      case 'totp_invalid':
        return 'That code is not correct. Check your authenticator app, then try again.'
      case 'disabled':
        return 'This account is turned off. Ask an administrator to switch it back on.'
      case 'locked':
        return 'This account is locked after too many failed attempts. Wait a few minutes, or ask an administrator to unlock it.'
      case 'rate_limited':
        return 'Too many attempts from this device. Wait a minute, then try again.'
      default:
        return error.message || 'Sign in did not work. Try again.'
    }
  }
  return 'Storix could not be reached. Check your connection, then try again.'
}

/** Notice is the calm red panel that carries an error above the button. */
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

/** Shell centres a card on the dark background with the product glow behind it. */
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

export default function LoginPage() {
  const navigate = useNavigate()
  const location = useLocation()
  const { signIn, refresh } = useSession()

  const [step, setStep] = useState<Step>('credentials')
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [remember, setRemember] = useState(false)
  const [reveal, setReveal] = useState(false)
  const [code, setCode] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  const codeRef = useRef<HTMLInputElement>(null)
  const busyRef = useRef(false)

  const state = location.state as { from?: string } | null
  const requested = state?.from ?? ''
  const target = requested && !requested.startsWith('/login') && !requested.startsWith('/setup') ? requested : '/'

  useEffect(() => {
    if (step === 'code') codeRef.current?.focus()
  }, [step])

  const submit = useCallback(
    async (totp?: string) => {
      if (busyRef.current) return
      busyRef.current = true
      setBusy(true)
      setError('')
      try {
        const result = await api.login({
          username: username.trim(),
          password,
          remember,
          totp: totp || undefined,
        })
        const session = await api.me().catch(() => null)
        if (session) signIn({ ...session, user: result.user, csrf: result.csrf })
        else await refresh()
        navigate(target, { replace: true })
      } catch (failure) {
        if (failure instanceof ApiError && failure.is('totp_required')) {
          setStep('code')
          setCode('')
          return
        }
        if (failure instanceof ApiError && failure.is('setup_required')) {
          navigate('/setup', { replace: true })
          return
        }
        setError(describe(failure, totp !== undefined))
        if (totp !== undefined) setCode('')
        codeRef.current?.focus()
      } finally {
        busyRef.current = false
        setBusy(false)
      }
    },
    [username, password, remember, signIn, refresh, navigate, target],
  )

  const onCredentials = (event: FormEvent) => {
    event.preventDefault()
    if (!username.trim()) {
      setError('Enter your username to continue.')
      return
    }
    if (!password) {
      setError('Enter your password to continue.')
      return
    }
    void submit()
  }

  const onCode = (event: FormEvent) => {
    event.preventDefault()
    if (code.length !== 6) {
      setError('Enter all six digits of the code.')
      return
    }
    void submit(code)
  }

  const onCodeChange = (value: string) => {
    const digits = value.replace(/\D/g, '').slice(0, 6)
    setCode(digits)
    if (error) setError('')
    if (digits.length === 6) void submit(digits)
  }

  const back = () => {
    setStep('credentials')
    setCode('')
    setError('')
  }

  return (
    <Shell>
      <div className="mb-7 flex flex-col items-center gap-3">
        <Logo size={40} />
        <p className="text-sm text-muted">Fast. Secure. Powerful.</p>
      </div>

      <div className="sx-panel w-full max-w-[26rem] animate-slide-up p-6 sm:p-7">
        {step === 'credentials' ? (
          <form onSubmit={onCredentials} noValidate>
            <h1 className="text-[17px] font-semibold text-ink">Sign in</h1>
            <p className="mt-1 text-sm text-muted">Use your Storix account to reach your files.</p>

            <div className="mt-6 space-y-4">
              <Field
                label="Username"
                icon="user"
                name="username"
                autoComplete="username"
                autoCapitalize="none"
                autoCorrect="off"
                spellCheck={false}
                autoFocus
                value={username}
                placeholder="Your username"
                onChange={(event) => {
                  setUsername(event.target.value)
                  if (error) setError('')
                }}
              />

              <div className="w-full">
                <label className="sx-label" htmlFor="storix-password">
                  Password
                </label>
                <div className="relative">
                  <span className="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-faint">
                    <Icon name="lock" size={16} />
                  </span>
                  <input
                    id="storix-password"
                    name="password"
                    type={reveal ? 'text' : 'password'}
                    autoComplete="current-password"
                    className="sx-input pl-9 pr-11"
                    placeholder="Your password"
                    value={password}
                    onChange={(event) => {
                      setPassword(event.target.value)
                      if (error) setError('')
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
              </div>

              <Checkbox checked={remember} onChange={setRemember} label="Keep me signed in" />

              {error && <Notice>{error}</Notice>}

              <Button type="submit" variant="primary" block loading={busy}>
                Sign in
              </Button>
            </div>
          </form>
        ) : (
          <form onSubmit={onCode} noValidate>
            <span className="flex h-10 w-10 items-center justify-center rounded-xl bg-primary/12 text-primary">
              <Icon name="shield" size={19} />
            </span>
            <h1 className="mt-4 text-[17px] font-semibold text-ink">Two step verification</h1>
            <p className="mt-1 text-sm text-muted">
              Enter the six digit code from your authenticator app to finish signing in.
            </p>

            <div className="mt-6 space-y-4">
              <input
                ref={codeRef}
                type="text"
                inputMode="numeric"
                pattern="[0-9]*"
                maxLength={6}
                autoComplete="one-time-code"
                aria-label="Six digit code"
                placeholder="000000"
                className="sx-input h-12 pl-[0.45em] text-center font-mono text-lg tracking-[0.45em]"
                value={code}
                onChange={(event) => onCodeChange(event.target.value)}
                onKeyDown={(event) => {
                  if (event.key === 'Escape') {
                    event.preventDefault()
                    back()
                  }
                }}
              />

              {error && <Notice>{error}</Notice>}

              <Button type="submit" variant="primary" block loading={busy} disabled={code.length !== 6}>
                Verify
              </Button>

              <div className="flex justify-center">
                <Button variant="ghost" icon="arrow-left" onClick={back} disabled={busy}>
                  Back to sign in
                </Button>
              </div>
            </div>
          </form>
        )}
      </div>
    </Shell>
  )
}
