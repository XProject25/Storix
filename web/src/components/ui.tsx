// Shared interface primitives. Every screen builds from these so the product
// looks like one product.
// Developed by X Project.

import clsx from 'clsx'
import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type ButtonHTMLAttributes,
  type InputHTMLAttributes,
  type ReactNode,
} from 'react'
import { createPortal } from 'react-dom'
import { Icon, type IconName } from './Icon'

// ---- button -----------------------------------------------------------------

export type ButtonVariant = 'primary' | 'secondary' | 'ghost' | 'danger'

export interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: ButtonVariant
  icon?: IconName
  iconRight?: IconName
  loading?: boolean
  block?: boolean
}

export function Button({
  variant = 'secondary',
  icon,
  iconRight,
  loading,
  block,
  className,
  children,
  disabled,
  ...rest
}: ButtonProps) {
  const base =
    variant === 'primary'
      ? 'sx-btn-primary'
      : variant === 'ghost'
        ? 'sx-btn-ghost'
        : variant === 'danger'
          ? 'sx-btn-danger'
          : 'sx-btn-secondary'
  return (
    <button
      type="button"
      className={clsx(base, block && 'w-full', className)}
      disabled={disabled || loading}
      {...rest}
    >
      {loading ? <Spinner size={15} /> : icon ? <Icon name={icon} size={16} /> : null}
      {children}
      {iconRight && <Icon name={iconRight} size={15} />}
    </button>
  )
}

export interface IconButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  icon: IconName
  label: string
  active?: boolean
  size?: number
  tone?: 'default' | 'danger'
}

export function IconButton({ icon, label, active, size = 17, tone = 'default', className, ...rest }: IconButtonProps) {
  return (
    <button
      type="button"
      title={label}
      aria-label={label}
      data-active={active ? 'true' : undefined}
      className={clsx(
        'inline-flex h-9 w-9 items-center justify-center rounded-xl transition-colors',
        tone === 'danger' ? 'text-danger hover:bg-danger/10' : 'text-muted hover:text-ink hover:bg-elevated',
        active && 'bg-elevated text-ink',
        className,
      )}
      {...rest}
    >
      <Icon name={icon} size={size} />
    </button>
  )
}

// ---- spinner ----------------------------------------------------------------

export function Spinner({ size = 16, className }: { size?: number; className?: string }) {
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 24 24"
      className={clsx('animate-spin', className)}
      aria-hidden="true"
      focusable="false"
    >
      <circle cx="12" cy="12" r="9" fill="none" stroke="currentColor" strokeOpacity="0.22" strokeWidth="2.5" />
      <path d="M21 12a9 9 0 0 0-9-9" fill="none" stroke="currentColor" strokeWidth="2.5" strokeLinecap="round" />
    </svg>
  )
}

// ---- inputs -----------------------------------------------------------------

export interface FieldProps extends InputHTMLAttributes<HTMLInputElement> {
  label?: string
  hint?: string
  error?: string
  icon?: IconName
}

export function Field({ label, hint, error, icon, className, id, ...rest }: FieldProps) {
  const generated = useId()
  const inputId = id ?? generated
  return (
    <div className={clsx('w-full', className)}>
      {label && (
        <label className="sx-label" htmlFor={inputId}>
          {label}
        </label>
      )}
      <div className="relative">
        {icon && (
          <span className="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-faint">
            <Icon name={icon} size={16} />
          </span>
        )}
        <input
          id={inputId}
          className={clsx('sx-input', icon && 'pl-9', error && 'border-danger/60')}
          aria-invalid={error ? true : undefined}
          {...rest}
        />
      </div>
      {error ? (
        <p className="mt-1.5 text-xs text-danger">{error}</p>
      ) : hint ? (
        <p className="mt-1.5 text-xs text-faint">{hint}</p>
      ) : null}
    </div>
  )
}

let idCounter = 0
function useId(): string {
  const ref = useRef<string>()
  if (!ref.current) {
    idCounter += 1
    ref.current = `sx-${idCounter}`
  }
  return ref.current
}

export function Toggle({
  checked,
  onChange,
  label,
  hint,
  disabled,
}: {
  checked: boolean
  onChange: (value: boolean) => void
  label?: ReactNode
  hint?: string
  disabled?: boolean
}) {
  return (
    <label className={clsx('flex items-start gap-3', disabled ? 'opacity-50' : 'cursor-pointer')}>
      <button
        type="button"
        role="switch"
        aria-checked={checked}
        disabled={disabled}
        onClick={() => onChange(!checked)}
        className={clsx(
          'relative mt-0.5 h-5 w-9 shrink-0 rounded-full transition-colors',
          checked ? 'bg-primary' : 'bg-line',
        )}
      >
        <span
          className={clsx(
            'absolute top-0.5 h-4 w-4 rounded-full bg-white transition-transform',
            checked ? 'translate-x-[18px]' : 'translate-x-0.5',
          )}
        />
      </button>
      {(label || hint) && (
        <span className="min-w-0">
          {label && <span className="block text-sm text-ink">{label}</span>}
          {hint && <span className="mt-0.5 block text-xs text-faint">{hint}</span>}
        </span>
      )}
    </label>
  )
}

export function Checkbox({
  checked,
  indeterminate,
  onChange,
  label,
  className,
}: {
  checked: boolean
  indeterminate?: boolean
  onChange: (value: boolean) => void
  label?: ReactNode
  className?: string
}) {
  return (
    <label className={clsx('inline-flex items-center gap-2.5 cursor-pointer select-none', className)}>
      <span
        onClick={(event) => {
          event.preventDefault()
          event.stopPropagation()
          onChange(!checked)
        }}
        className={clsx(
          'flex h-[18px] w-[18px] items-center justify-center rounded-[6px] border transition-colors',
          checked || indeterminate ? 'border-primary bg-primary text-white' : 'border-line bg-elevated',
        )}
      >
        {indeterminate ? (
          <span className="h-0.5 w-2.5 rounded-full bg-current" />
        ) : checked ? (
          <Icon name="check" size={12} strokeWidth={3} />
        ) : null}
      </span>
      {label && <span className="text-sm">{label}</span>}
    </label>
  )
}

export function Select({
  value,
  onChange,
  options,
  label,
  className,
}: {
  value: string
  onChange: (value: string) => void
  options: Array<{ value: string; label: string }>
  label?: string
  className?: string
}) {
  return (
    <div className={className}>
      {label && <label className="sx-label">{label}</label>}
      <div className="relative">
        <select
          value={value}
          onChange={(event) => onChange(event.target.value)}
          className="sx-input appearance-none pr-9"
        >
          {options.map((option) => (
            <option key={option.value} value={option.value}>
              {option.label}
            </option>
          ))}
        </select>
        <span className="pointer-events-none absolute right-3 top-1/2 -translate-y-1/2 text-faint">
          <Icon name="chevron-down" size={15} />
        </span>
      </div>
    </div>
  )
}

// ---- modal ------------------------------------------------------------------

export interface ModalProps {
  open: boolean
  onClose: () => void
  title?: ReactNode
  description?: ReactNode
  children?: ReactNode
  footer?: ReactNode
  width?: number
  icon?: IconName
}

export function Modal({ open, onClose, title, description, children, footer, width = 480, icon }: ModalProps) {
  useEffect(() => {
    if (!open) return
    const onKey = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        event.stopPropagation()
        onClose()
      }
    }
    document.addEventListener('keydown', onKey, true)
    return () => document.removeEventListener('keydown', onKey, true)
  }, [open, onClose])

  if (!open) return null
  return createPortal(
    <div className="fixed inset-0 z-50 flex items-center justify-center p-4">
      <div className="absolute inset-0 bg-black/55 backdrop-blur-[2px] animate-fade-in" onClick={onClose} />
      <div
        role="dialog"
        aria-modal="true"
        className="sx-panel relative z-10 w-full animate-slide-up p-5"
        style={{ maxWidth: width }}
        onClick={(event) => event.stopPropagation()}
      >
        {(title || icon) && (
          <div className="mb-4 flex items-start gap-3">
            {icon && (
              <span className="mt-0.5 flex h-9 w-9 items-center justify-center rounded-xl bg-primary/12 text-primary">
                <Icon name={icon} size={18} />
              </span>
            )}
            <div className="min-w-0 flex-1">
              {title && <h2 className="text-[15px] font-semibold text-ink">{title}</h2>}
              {description && <p className="mt-1 text-sm text-muted">{description}</p>}
            </div>
            <IconButton icon="close" label="Close" onClick={onClose} className="-mr-1 -mt-1" />
          </div>
        )}
        {children}
        {footer && <div className="mt-5 flex items-center justify-end gap-2">{footer}</div>}
      </div>
    </div>,
    document.body,
  )
}

export function ConfirmDialog({
  open,
  title,
  message,
  confirmLabel = 'Confirm',
  cancelLabel = 'Cancel',
  danger,
  busy,
  onConfirm,
  onCancel,
}: {
  open: boolean
  title: string
  message: ReactNode
  confirmLabel?: string
  cancelLabel?: string
  danger?: boolean
  busy?: boolean
  onConfirm: () => void
  onCancel: () => void
}) {
  return (
    <Modal
      open={open}
      onClose={onCancel}
      title={title}
      icon={danger ? 'alert' : 'help'}
      width={430}
      footer={
        <>
          <Button onClick={onCancel} disabled={busy}>
            {cancelLabel}
          </Button>
          <Button variant={danger ? 'danger' : 'primary'} onClick={onConfirm} loading={busy}>
            {confirmLabel}
          </Button>
        </>
      }
    >
      <div className="text-sm text-muted">{message}</div>
    </Modal>
  )
}

// ---- dropdown menu ----------------------------------------------------------

export interface MenuItem {
  id: string
  label: string
  icon?: IconName
  shortcut?: string
  danger?: boolean
  disabled?: boolean
  divider?: boolean
  onSelect?: () => void
}

export function Menu({
  items,
  x,
  y,
  onClose,
  anchorRight,
}: {
  items: MenuItem[]
  x: number
  y: number
  onClose: () => void
  anchorRight?: boolean
}) {
  const ref = useRef<HTMLDivElement>(null)
  const [pos, setPos] = useState({ x, y })

  useLayoutEffect(() => {
    const element = ref.current
    if (!element) return
    const rect = element.getBoundingClientRect()
    let nextX = anchorRight ? x - rect.width : x
    let nextY = y
    if (nextX + rect.width > window.innerWidth - 8) nextX = window.innerWidth - rect.width - 8
    if (nextY + rect.height > window.innerHeight - 8) nextY = Math.max(8, y - rect.height)
    if (nextX < 8) nextX = 8
    setPos({ x: nextX, y: nextY })
  }, [x, y, anchorRight])

  useEffect(() => {
    const onDown = (event: MouseEvent) => {
      if (!ref.current?.contains(event.target as Node)) onClose()
    }
    const onKey = (event: KeyboardEvent) => {
      if (event.key === 'Escape') onClose()
    }
    document.addEventListener('mousedown', onDown)
    document.addEventListener('keydown', onKey)
    window.addEventListener('resize', onClose)
    window.addEventListener('blur', onClose)
    return () => {
      document.removeEventListener('mousedown', onDown)
      document.removeEventListener('keydown', onKey)
      window.removeEventListener('resize', onClose)
      window.removeEventListener('blur', onClose)
    }
  }, [onClose])

  return createPortal(
    <div ref={ref} className="sx-menu fixed z-[60]" style={{ left: pos.x, top: pos.y }} role="menu">
      {items.map((item, index) =>
        item.divider ? (
          <div key={`divider-${index}`} className="sx-divider" />
        ) : (
          <button
            key={item.id}
            type="button"
            role="menuitem"
            disabled={item.disabled}
            data-danger={item.danger ? 'true' : undefined}
            className="sx-menu-item w-full text-left disabled:opacity-40 disabled:pointer-events-none"
            onClick={() => {
              onClose()
              item.onSelect?.()
            }}
          >
            {item.icon && <Icon name={item.icon} size={16} className="shrink-0 opacity-80" />}
            <span className="flex-1 truncate">{item.label}</span>
            {item.shortcut && <span className="font-mono text-[11px] text-faint">{item.shortcut}</span>}
          </button>
        ),
      )}
    </div>,
    document.body,
  )
}

/** useContextMenu wires a right click menu to any element. */
export function useContextMenu() {
  const [state, setState] = useState<{ x: number; y: number } | null>(null)
  const open = useCallback((event: { clientX: number; clientY: number; preventDefault: () => void }) => {
    event.preventDefault()
    setState({ x: event.clientX, y: event.clientY })
  }, [])
  const close = useCallback(() => setState(null), [])
  return { position: state, open, close }
}

// ---- toasts -----------------------------------------------------------------

export interface Toast {
  id: number
  tone: 'info' | 'success' | 'error'
  title: string
  message?: string
  action?: { label: string; run: () => void }
}

interface ToastContextValue {
  toasts: Toast[]
  push: (toast: Omit<Toast, 'id'>) => number
  dismiss: (id: number) => void
  success: (title: string, message?: string) => void
  error: (title: string, message?: string) => void
  info: (title: string, message?: string) => void
}

const ToastContext = createContext<ToastContextValue | null>(null)

export function ToastProvider({ children }: { children: ReactNode }) {
  const [toasts, setToasts] = useState<Toast[]>([])
  const next = useRef(1)

  const dismiss = useCallback((id: number) => {
    setToasts((current) => current.filter((toast) => toast.id !== id))
  }, [])

  const push = useCallback(
    (toast: Omit<Toast, 'id'>) => {
      const id = next.current++
      setToasts((current) => [...current.slice(-4), { ...toast, id }])
      window.setTimeout(() => dismiss(id), toast.tone === 'error' ? 8000 : 4200)
      return id
    },
    [dismiss],
  )

  const value = useMemo<ToastContextValue>(
    () => ({
      toasts,
      push,
      dismiss,
      success: (title, message) => void push({ tone: 'success', title, message }),
      error: (title, message) => void push({ tone: 'error', title, message }),
      info: (title, message) => void push({ tone: 'info', title, message }),
    }),
    [toasts, push, dismiss],
  )

  return (
    <ToastContext.Provider value={value}>
      {children}
      {createPortal(
        <div className="pointer-events-none fixed bottom-5 left-1/2 z-[70] flex -translate-x-1/2 flex-col items-center gap-2">
          {toasts.map((toast) => (
            <div
              key={toast.id}
              className="sx-panel pointer-events-auto flex max-w-[min(92vw,30rem)] items-start gap-3 px-4 py-3 animate-slide-up"
            >
              <span
                className={clsx(
                  'mt-0.5 flex h-6 w-6 shrink-0 items-center justify-center rounded-lg',
                  toast.tone === 'success' && 'bg-success/15 text-success',
                  toast.tone === 'error' && 'bg-danger/15 text-danger',
                  toast.tone === 'info' && 'bg-primary/15 text-primary',
                )}
              >
                <Icon
                  name={toast.tone === 'success' ? 'check-circle' : toast.tone === 'error' ? 'alert' : 'info'}
                  size={15}
                />
              </span>
              <div className="min-w-0 flex-1">
                <div className="text-sm font-medium text-ink">{toast.title}</div>
                {toast.message && <div className="mt-0.5 text-xs text-muted">{toast.message}</div>}
              </div>
              {toast.action && (
                <button
                  type="button"
                  className="text-xs font-medium text-primary hover:underline"
                  onClick={() => {
                    toast.action?.run()
                    dismiss(toast.id)
                  }}
                >
                  {toast.action.label}
                </button>
              )}
              <IconButton icon="close" label="Dismiss" size={14} onClick={() => dismiss(toast.id)} className="h-6 w-6" />
            </div>
          ))}
        </div>,
        document.body,
      )}
    </ToastContext.Provider>
  )
}

export function useToast(): ToastContextValue {
  const context = useContext(ToastContext)
  if (!context) throw new Error('useToast must be used inside ToastProvider')
  return context
}

// ---- layout helpers ---------------------------------------------------------

export function EmptyState({
  icon = 'folder-open',
  title,
  message,
  action,
}: {
  icon?: IconName
  title: string
  message?: string
  action?: ReactNode
}) {
  return (
    <div className="flex flex-col items-center justify-center px-6 py-16 text-center">
      <span className="mb-4 flex h-14 w-14 items-center justify-center rounded-2xl bg-elevated text-faint">
        <Icon name={icon} size={26} />
      </span>
      <h3 className="text-[15px] font-medium text-ink">{title}</h3>
      {message && <p className="mt-1.5 max-w-sm text-sm text-muted">{message}</p>}
      {action && <div className="mt-5">{action}</div>}
    </div>
  )
}

export function Progress({ value, className }: { value: number; className?: string }) {
  const clamped = Math.min(100, Math.max(0, value))
  return (
    <div className={clsx('sx-progress', className)} role="progressbar" aria-valuenow={Math.round(clamped)}>
      <span style={{ width: `${clamped}%` }} />
    </div>
  )
}

export function Skeleton({ className }: { className?: string }) {
  return <div className={clsx('sx-skeleton', className)} />
}

export function SectionTitle({ children, action }: { children: ReactNode; action?: ReactNode }) {
  return (
    <div className="mb-3 flex items-center justify-between">
      <h2 className="text-[11px] font-semibold uppercase tracking-[0.14em] text-faint">{children}</h2>
      {action}
    </div>
  )
}

export function Tooltip({ label, children }: { label: string; children: ReactNode }) {
  return (
    <span className="group relative inline-flex">
      {children}
      <span className="pointer-events-none absolute bottom-full left-1/2 z-50 mb-1.5 -translate-x-1/2 whitespace-nowrap rounded-lg bg-elevated px-2 py-1 text-[11px] text-ink opacity-0 shadow-pop transition-opacity group-hover:opacity-100">
        {label}
      </span>
    </span>
  )
}
