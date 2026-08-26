// Formatting helpers shared by every screen.
// Developed by X Project.

const UNITS = ['B', 'KB', 'MB', 'GB', 'TB', 'PB']

/** bytes renders a size the way a file manager should: short and readable. */
export function bytes(value: number, digits?: number): string {
  if (!Number.isFinite(value) || value <= 0) return '0 B'
  let index = 0
  let size = value
  while (size >= 1024 && index < UNITS.length - 1) {
    size /= 1024
    index++
  }
  const precision = digits ?? (index === 0 ? 0 : size >= 100 ? 0 : size >= 10 ? 1 : 2)
  return `${size.toFixed(precision)} ${UNITS[index]}`
}

/** rate renders a transfer speed. */
export function rate(bytesPerSecond: number): string {
  if (!Number.isFinite(bytesPerSecond) || bytesPerSecond <= 0) return '0 B/s'
  return `${bytes(bytesPerSecond, 1)}/s`
}

/** duration renders seconds as a compact clock, for example 01:48. */
export function duration(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds < 0) return '--:--'
  if (seconds >= 86400) return `${Math.round(seconds / 86400)}d`
  const total = Math.round(seconds)
  const h = Math.floor(total / 3600)
  const m = Math.floor((total % 3600) / 60)
  const s = total % 60
  const pad = (n: number) => String(n).padStart(2, '0')
  return h > 0 ? `${h}:${pad(m)}:${pad(s)}` : `${pad(m)}:${pad(s)}`
}

/** percent clamps and rounds a ratio for display. */
export function percent(value: number, digits = 0): string {
  if (!Number.isFinite(value)) return '0%'
  const clamped = Math.min(100, Math.max(0, value))
  return `${clamped.toFixed(digits)}%`
}

const RELATIVE = new Intl.RelativeTimeFormat(undefined, { numeric: 'auto' })

/** ago renders a timestamp relative to now, falling back to a date. */
export function ago(input: string | number | Date | undefined | null): string {
  if (!input) return ''
  const date = new Date(input)
  if (Number.isNaN(date.getTime())) return ''
  const diff = (date.getTime() - Date.now()) / 1000
  const abs = Math.abs(diff)
  if (abs < 45) return 'just now'
  if (abs < 3600) return RELATIVE.format(Math.round(diff / 60), 'minute')
  if (abs < 86400) return RELATIVE.format(Math.round(diff / 3600), 'hour')
  if (abs < 7 * 86400) return RELATIVE.format(Math.round(diff / 86400), 'day')
  return dateShort(date)
}

/** dateShort renders a calendar date without the time. */
export function dateShort(input: string | number | Date | undefined | null): string {
  if (!input) return ''
  const date = new Date(input)
  if (Number.isNaN(date.getTime())) return ''
  const sameYear = date.getFullYear() === new Date().getFullYear()
  return date.toLocaleDateString(undefined, {
    month: 'short',
    day: 'numeric',
    year: sameYear ? undefined : 'numeric',
  })
}

/** dateTime renders a full timestamp for detail panels and logs. */
export function dateTime(input: string | number | Date | undefined | null): string {
  if (!input) return ''
  const date = new Date(input)
  if (Number.isNaN(date.getTime())) return ''
  return date.toLocaleString(undefined, {
    year: 'numeric',
    month: 'short',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
  })
}

/** smartDate is what the file list shows: a time today, a date otherwise. */
export function smartDate(input: string | number | Date | undefined | null): string {
  if (!input) return ''
  const date = new Date(input)
  if (Number.isNaN(date.getTime())) return ''
  const now = new Date()
  const sameDay =
    date.getDate() === now.getDate() && date.getMonth() === now.getMonth() && date.getFullYear() === now.getFullYear()
  if (sameDay) return `Today ${date.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' })}`
  const yesterday = new Date(now)
  yesterday.setDate(now.getDate() - 1)
  const isYesterday =
    date.getDate() === yesterday.getDate() &&
    date.getMonth() === yesterday.getMonth() &&
    date.getFullYear() === yesterday.getFullYear()
  if (isYesterday) return `Yesterday ${date.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' })}`
  return dateTime(date)
}

/** parentPath returns the containing folder of an absolute path. */
export function parentPath(path: string): string {
  if (!path || path === '/') return ''
  const trimmed = path.replace(/\/+$/, '')
  const index = trimmed.lastIndexOf('/')
  if (index <= 0) return '/'
  return trimmed.slice(0, index)
}

/** baseName returns the last element of a path. */
export function baseName(path: string): string {
  if (!path) return ''
  const trimmed = path.replace(/\/+$/, '')
  const index = trimmed.lastIndexOf('/')
  return index < 0 ? trimmed : trimmed.slice(index + 1)
}

/** joinPath appends a child to an absolute path. */
export function joinPath(base: string, name: string): string {
  if (!base || base === '/') return '/' + name.replace(/^\/+/, '')
  return base.replace(/\/+$/, '') + '/' + name.replace(/^\/+/, '')
}

/** extensionOf returns a lowercase extension without the dot. */
export function extensionOf(name: string): string {
  const index = name.lastIndexOf('.')
  if (index <= 0) return ''
  return name.slice(index + 1).toLowerCase()
}

/** initials builds an avatar label from a display name. */
export function initials(name: string): string {
  const parts = name.trim().split(/\s+/).filter(Boolean)
  if (parts.length === 0) return '?'
  if (parts.length === 1) return parts[0].slice(0, 2).toUpperCase()
  return (parts[0][0] + parts[parts.length - 1][0]).toUpperCase()
}

/** plural picks the right noun form without a leading count. */
export function plural(count: number, singular: string, pluralForm?: string): string {
  return count === 1 ? singular : pluralForm ?? singular + 's'
}

/** counted renders "3 items" style labels. */
export function counted(count: number, singular: string, pluralForm?: string): string {
  return `${count.toLocaleString()} ${plural(count, singular, pluralForm)}`
}

/** eta estimates the seconds left in a transfer. */
export function eta(done: number, total: number, bytesPerSecond: number): number {
  if (bytesPerSecond <= 0 || total <= done) return 0
  return (total - done) / bytesPerSecond
}

/** truncateMiddle shortens a long path but keeps both ends readable. */
export function truncateMiddle(text: string, max = 48): string {
  if (text.length <= max) return text
  const half = Math.floor((max - 3) / 2)
  return `${text.slice(0, half)}...${text.slice(text.length - half)}`
}

/** modeToText turns 0644 into a plain description for the simple view. */
export function modeToText(octal: string): string {
  const digits = octal.replace(/^0/, '').padStart(3, '0')
  const who = ['Owner', 'Group', 'Everyone']
  const parts: string[] = []
  for (let i = 0; i < 3; i++) {
    const value = Number(digits[i] ?? '0')
    const bits: string[] = []
    if (value & 4) bits.push('read')
    if (value & 2) bits.push('write')
    if (value & 1) bits.push('run')
    parts.push(`${who[i]}: ${bits.length ? bits.join(', ') : 'no access'}`)
  }
  return parts.join('  |  ')
}
