// Line icon set for Storix. Every icon is inline SVG on a 24 by 24 grid so it
// inherits the current colour and stays crisp at any size.
// Developed by X Project.

import type { SVGProps } from 'react'

export type IconName =
  | 'activity'
  | 'alert'
  | 'archive'
  | 'arrow-left'
  | 'arrow-right'
  | 'arrow-up'
  | 'bell'
  | 'calendar'
  | 'check'
  | 'check-circle'
  | 'chevron-down'
  | 'chevron-left'
  | 'chevron-right'
  | 'chevron-up'
  | 'clipboard'
  | 'clock'
  | 'close'
  | 'cloud-upload'
  | 'code'
  | 'copy'
  | 'cpu'
  | 'database'
  | 'download'
  | 'drive'
  | 'edit'
  | 'external'
  | 'eye'
  | 'eye-off'
  | 'file'
  | 'file-plus'
  | 'filter'
  | 'folder'
  | 'folder-open'
  | 'folder-plus'
  | 'gallery'
  | 'globe'
  | 'grid'
  | 'help'
  | 'home'
  | 'image'
  | 'info'
  | 'key'
  | 'link'
  | 'list'
  | 'lock'
  | 'logout'
  | 'menu'
  | 'monitor'
  | 'moon'
  | 'more'
  | 'move'
  | 'music'
  | 'pause'
  | 'pdf'
  | 'pin'
  | 'play'
  | 'plus'
  | 'refresh'
  | 'restore'
  | 'scissors'
  | 'search'
  | 'server'
  | 'settings'
  | 'share'
  | 'shield'
  | 'sort'
  | 'star'
  | 'star-filled'
  | 'sun'
  | 'terminal'
  | 'trash'
  | 'upload'
  | 'user'
  | 'users'
  | 'video'
  | 'zap'

const PATHS: Record<IconName, string> = {
  activity: 'M3 12h4l3 8 4-16 3 8h4',
  alert: 'M12 9v4m0 4h.01M10.3 4.3 2.6 17.6A1.9 1.9 0 0 0 4.3 20.5h15.4a1.9 1.9 0 0 0 1.7-2.9L13.7 4.3a1.9 1.9 0 0 0-3.4 0Z',
  archive: 'M3 7h18M5 7v12a1 1 0 0 0 1 1h12a1 1 0 0 0 1-1V7M3 7l1.5-3h15L21 7M10 12h4',
  'arrow-left': 'M19 12H5m0 0 6-6m-6 6 6 6',
  'arrow-right': 'M5 12h14m0 0-6-6m6 6-6 6',
  'arrow-up': 'M12 19V5m0 0-6 6m6-6 6 6',
  bell: 'M18 8a6 6 0 1 0-12 0c0 6-3 7-3 7h18s-3-1-3-7M13.7 20a2 2 0 0 1-3.4 0',
  calendar: 'M7 3v4M17 3v4M4 9h16M5 5h14a1 1 0 0 1 1 1v13a1 1 0 0 1-1 1H5a1 1 0 0 1-1-1V6a1 1 0 0 1 1-1Z',
  check: 'M4 12.5 9.5 18 20 6.5',
  'check-circle': 'M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18Zm-3.5-9.2L11 14.5l5-5.5',
  'chevron-down': 'm6 9.5 6 6 6-6',
  'chevron-left': 'm14.5 6-6 6 6 6',
  'chevron-right': 'm9.5 6 6 6-6 6',
  'chevron-up': 'm6 14.5 6-6 6 6',
  clipboard: 'M9 4h6a1 1 0 0 1 1 1v1H8V5a1 1 0 0 1 1-1ZM8 6H6a1 1 0 0 0-1 1v12a1 1 0 0 0 1 1h12a1 1 0 0 0 1-1V7a1 1 0 0 0-1-1h-2',
  clock: 'M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18Zm0-13v5l3 2',
  close: 'M6 6l12 12M18 6 6 18',
  'cloud-upload': 'M7 17.5A4.5 4.5 0 0 1 7.2 8.6a5.5 5.5 0 0 1 10.5 1.6A3.9 3.9 0 0 1 17 17.5M12 21v-9m0 0-3 3m3-3 3 3',
  code: 'm9 8-5 4 5 4m6-8 5 4-5 4M13.5 5l-3 14',
  copy: 'M9 9h9a1 1 0 0 1 1 1v9a1 1 0 0 1-1 1H9a1 1 0 0 1-1-1v-9a1 1 0 0 1 1-1Zm-3 6H5a1 1 0 0 1-1-1V5a1 1 0 0 1 1-1h9a1 1 0 0 1 1 1v1',
  cpu: 'M8 8h8v8H8zM6 4h12a2 2 0 0 1 2 2v12a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2V6a2 2 0 0 1 2-2ZM9 2v2m6-2v2M9 20v2m6-2v2M2 9h2m-2 6h2m16-6h2m-2 6h2',
  database: 'M12 7.5c4.4 0 8-1.1 8-2.5S16.4 2.5 12 2.5 4 3.6 4 5s3.6 2.5 8 2.5ZM4 5v14c0 1.4 3.6 2.5 8 2.5s8-1.1 8-2.5V5M4 12c0 1.4 3.6 2.5 8 2.5s8-1.1 8-2.5',
  download: 'M12 3v12m0 0-4.5-4.5M12 15l4.5-4.5M4 17v2a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2v-2',
  drive: 'M4 5h16a1 1 0 0 1 1 1v5H3V6a1 1 0 0 1 1-1ZM3 11h18v7a1 1 0 0 1-1 1H4a1 1 0 0 1-1-1v-7Zm4 4h.01M10 15h4',
  edit: 'M4 20h4l10.5-10.5a2.1 2.1 0 0 0-3-3L5 17v3Zm10-13 3 3',
  external: 'M14 4h6v6m0-6L10 14M18 14v5a1 1 0 0 1-1 1H5a1 1 0 0 1-1-1V7a1 1 0 0 1 1-1h5',
  eye: 'M2.5 12S6 5.5 12 5.5 21.5 12 21.5 12 18 18.5 12 18.5 2.5 12 2.5 12Zm9.5 2.6a2.6 2.6 0 1 0 0-5.2 2.6 2.6 0 0 0 0 5.2Z',
  'eye-off': 'M4 4l16 16M9.9 5.9A8.4 8.4 0 0 1 12 5.5c6 0 9.5 6.5 9.5 6.5a17 17 0 0 1-3.3 4M6.3 8.2A17 17 0 0 0 2.5 12S6 18.5 12 18.5a8.6 8.6 0 0 0 3.4-.7M10.2 10.3a2.6 2.6 0 0 0 3.5 3.6',
  file: 'M14 3H7a1 1 0 0 0-1 1v16a1 1 0 0 0 1 1h10a1 1 0 0 0 1-1V7l-4-4Zm0 0v4h4',
  'file-plus': 'M14 3H7a1 1 0 0 0-1 1v16a1 1 0 0 0 1 1h10a1 1 0 0 0 1-1V7l-4-4Zm0 0v4h4m-6 3v6m-3-3h6',
  filter: 'M3 5h18l-7 8v6l-4 2v-8L3 5Z',
  folder: 'M3 7.5A1.5 1.5 0 0 1 4.5 6h4.2a1.5 1.5 0 0 1 1.1.5l1.3 1.4h8.4A1.5 1.5 0 0 1 21 9.4v9.1a1.5 1.5 0 0 1-1.5 1.5h-15A1.5 1.5 0 0 1 3 18.5v-11Z',
  'folder-open': 'M3 8V6.5A1.5 1.5 0 0 1 4.5 5h4.2a1.5 1.5 0 0 1 1.1.5L11.1 7h7.4A1.5 1.5 0 0 1 20 8.5V10M3.4 10h17.2a1 1 0 0 1 1 1.2l-1.4 7A1.5 1.5 0 0 1 18.7 19.5H5.3a1.5 1.5 0 0 1-1.5-1.3l-1.4-7a1 1 0 0 1 1-1.2Z',
  'folder-plus': 'M3 7.5A1.5 1.5 0 0 1 4.5 6h4.2a1.5 1.5 0 0 1 1.1.5l1.3 1.4h8.4A1.5 1.5 0 0 1 21 9.4v9.1a1.5 1.5 0 0 1-1.5 1.5h-15A1.5 1.5 0 0 1 3 18.5v-11ZM12 11v6m-3-3h6',
  gallery: 'M4 5h16a1 1 0 0 1 1 1v12a1 1 0 0 1-1 1H4a1 1 0 0 1-1-1V6a1 1 0 0 1 1-1Zm0 11 5-5 4 4 3-2 5 4M8.5 10a1.2 1.2 0 1 0 0-2.4 1.2 1.2 0 0 0 0 2.4Z',
  globe: 'M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18Zm-9-9h18M12 3c2.3 2.4 3.5 5.5 3.5 9S14.3 18.6 12 21c-2.3-2.4-3.5-5.5-3.5-9S9.7 5.4 12 3Z',
  grid: 'M4 4h6v6H4zm10 0h6v6h-6zM4 14h6v6H4zm10 0h6v6h-6z',
  help: 'M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18Zm-2.2-11.3a2.3 2.3 0 1 1 3.2 2.1c-.6.3-1 .9-1 1.6v.4m0 3h.01',
  home: 'M4 10.5 12 4l8 6.5V19a1 1 0 0 1-1 1h-4v-6H9v6H5a1 1 0 0 1-1-1v-8.5Z',
  image: 'M4 5h16a1 1 0 0 1 1 1v12a1 1 0 0 1-1 1H4a1 1 0 0 1-1-1V6a1 1 0 0 1 1-1Zm0 12 5.5-5.5 3.5 3.5 2.5-2 5.5 4.5M9 10.5a1.5 1.5 0 1 0 0-3 1.5 1.5 0 0 0 0 3Z',
  info: 'M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18Zm0-9v5m0-8h.01',
  key: 'M15.5 3a5.5 5.5 0 0 0-5.2 7.3L3 17.6V21h3.4l1-1v-2h2v-2h2l1.3-1.3A5.5 5.5 0 1 0 15.5 3Zm1.5 4.5h.01',
  link: 'M10.5 13.5a4 4 0 0 0 5.7 0l2.8-2.8a4 4 0 1 0-5.7-5.7l-1.4 1.4m-1.4 4.1a4 4 0 0 0-5.7 0l-2.8 2.8a4 4 0 1 0 5.7 5.7l1.4-1.4',
  list: 'M8 6h13M8 12h13M8 18h13M3.5 6h.01M3.5 12h.01M3.5 18h.01',
  lock: 'M7 11V8a5 5 0 0 1 10 0v3M6 11h12a1 1 0 0 1 1 1v8a1 1 0 0 1-1 1H6a1 1 0 0 1-1-1v-8a1 1 0 0 1 1-1Zm6 4v3',
  logout: 'M15 5V4a1 1 0 0 0-1-1H5a1 1 0 0 0-1 1v16a1 1 0 0 0 1 1h9a1 1 0 0 0 1-1v-1m3-9-3 3m3-3-3-3m3 3H9',
  menu: 'M4 7h16M4 12h16M4 17h16',
  monitor: 'M4 5h16a1 1 0 0 1 1 1v9a1 1 0 0 1-1 1H4a1 1 0 0 1-1-1V6a1 1 0 0 1 1-1Zm5 15h6m-3-4v4',
  moon: 'M20 14.5A8.5 8.5 0 0 1 9.5 4 8.5 8.5 0 1 0 20 14.5Z',
  more: 'M12 6.5h.01M12 12h.01M12 17.5h.01',
  move: 'M12 3v18M3 12h18m0 0-3-3m3 3-3 3M3 12l3-3m-3 3 3 3M12 3 9 6m3-3 3 3m-3 15-3-3m3 3 3-3',
  music: 'M9 18V6l11-2v12M9 18a3 3 0 1 1-6 0 3 3 0 0 1 6 0Zm11-2a3 3 0 1 1-6 0 3 3 0 0 1 6 0Z',
  pause: 'M9 5v14M15 5v14',
  pdf: 'M14 3H7a1 1 0 0 0-1 1v16a1 1 0 0 0 1 1h10a1 1 0 0 0 1-1V7l-4-4Zm0 0v4h4M8.5 17v-4h1.2a1.2 1.2 0 0 1 0 2.4H8.5m5-2.4h2m-2 4v-4',
  pin: 'M9 4h6l-1 5 3.5 3.5H15V20l-3 1-3-1v-7.5H6.5L10 9 9 4Z',
  play: 'M8 5.5v13l11-6.5-11-6.5Z',
  plus: 'M12 5v14M5 12h14',
  refresh: 'M20 12a8 8 0 1 1-2.3-5.6M20 4v5h-5',
  restore: 'M4 12a8 8 0 1 0 2.3-5.6M4 4v5h5M12 8v4.5l3 2',
  scissors: 'm7 7 10 10M17 7 7 17M8 8a2.5 2.5 0 1 1-3.5-3.5A2.5 2.5 0 0 1 8 8Zm-3.5 8a2.5 2.5 0 1 0 3.5 3.5A2.5 2.5 0 0 0 4.5 16Z',
  search: 'M11 18a7 7 0 1 0 0-14 7 7 0 0 0 0 14Zm5.5-1.5L21 21',
  server: 'M4 4h16a1 1 0 0 1 1 1v5H3V5a1 1 0 0 1 1-1Zm-1 6h18v5H3v-5Zm0 5h18v4a1 1 0 0 1-1 1H4a1 1 0 0 1-1-1v-4Zm4-8h.01M7 12.5h.01M7 18h.01',
  settings: 'M12 15.2a3.2 3.2 0 1 0 0-6.4 3.2 3.2 0 0 0 0 6.4Zm7.4-2.1a7.6 7.6 0 0 0 0-2.2l2-1.5-2-3.4-2.4 1a7.5 7.5 0 0 0-1.9-1.1L14.7 3H9.3l-.4 2.9c-.7.3-1.3.6-1.9 1.1l-2.4-1-2 3.4 2 1.5a7.6 7.6 0 0 0 0 2.2l-2 1.5 2 3.4 2.4-1c.6.5 1.2.8 1.9 1.1l.4 2.9h5.4l.4-2.9c.7-.3 1.3-.6 1.9-1.1l2.4 1 2-3.4-2-1.5Z',
  share: 'M12 3v12m0-12 4 4m-4-4-4 4M5 14v5a1 1 0 0 0 1 1h12a1 1 0 0 0 1-1v-5',
  shield: 'M12 3 5 6v6c0 4.2 2.9 7.7 7 9 4.1-1.3 7-4.8 7-9V6l-7-3Zm-2.5 9 2 2 4-4',
  sort: 'M7 5v14m0 0-3-3m3 3 3-3M17 19V5m0 0-3 3m3-3 3 3',
  star: 'm12 4 2.4 5 5.6.8-4 3.9.9 5.5-4.9-2.6-4.9 2.6.9-5.5-4-3.9L9.6 9 12 4Z',
  'star-filled': 'm12 4 2.4 5 5.6.8-4 3.9.9 5.5-4.9-2.6-4.9 2.6.9-5.5-4-3.9L9.6 9 12 4Z',
  sun: 'M12 16.5a4.5 4.5 0 1 0 0-9 4.5 4.5 0 0 0 0 9ZM12 2v2m0 16v2M4.2 4.2l1.5 1.5m12.6 12.6 1.5 1.5M2 12h2m16 0h2M4.2 19.8l1.5-1.5M18.3 5.7l1.5-1.5',
  terminal: 'm5 7 5 5-5 5m8 1h6',
  trash: 'M4 7h16M9 7V5a1 1 0 0 1 1-1h4a1 1 0 0 1 1 1v2m3 0v12a1 1 0 0 1-1 1H7a1 1 0 0 1-1-1V7m4 4v6m4-6v6',
  upload: 'M12 15V3m0 0-4.5 4.5M12 3l4.5 4.5M4 17v2a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2v-2',
  user: 'M12 12a4 4 0 1 0 0-8 4 4 0 0 0 0 8Zm-8 8a8 8 0 0 1 16 0',
  users: 'M9 12a4 4 0 1 0 0-8 4 4 0 0 0 0 8Zm-7 8a7 7 0 0 1 14 0m2-15.7a4 4 0 0 1 0 7.4M18 20a7 7 0 0 0-2-4.9',
  video: 'M4 6h11a1 1 0 0 1 1 1v10a1 1 0 0 1-1 1H4a1 1 0 0 1-1-1V7a1 1 0 0 1 1-1Zm12 5 5-3v8l-5-3v-2Z',
  zap: 'M13 3 5 13.5h6L11 21l8-10.5h-6L13 3Z',
}

const FILLED: Partial<Record<IconName, boolean>> = {
  'star-filled': true,
  play: true,
}

export interface IconProps extends Omit<SVGProps<SVGSVGElement>, 'name'> {
  name: IconName
  size?: number
  strokeWidth?: number
}

/** Icon renders one line icon. */
export function Icon({ name, size = 18, strokeWidth = 1.7, ...rest }: IconProps) {
  const filled = FILLED[name] === true
  return (
    <svg
      viewBox="0 0 24 24"
      width={size}
      height={size}
      fill={filled ? 'currentColor' : 'none'}
      stroke="currentColor"
      strokeWidth={filled ? 0 : strokeWidth}
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
      focusable="false"
      {...rest}
    >
      <path d={PATHS[name]} />
    </svg>
  )
}

/** iconForKind maps a file family onto its icon. */
export function iconForKind(kind: string, isDir: boolean): IconName {
  if (isDir) return 'folder'
  switch (kind) {
    case 'image':
      return 'image'
    case 'video':
      return 'video'
    case 'audio':
      return 'music'
    case 'pdf':
      return 'pdf'
    case 'archive':
      return 'archive'
    case 'code':
      return 'code'
    case 'text':
      return 'file'
    case 'disk':
      return 'drive'
    case 'document':
      return 'file'
    default:
      return 'file'
  }
}

/** colourForKind gives each family a distinct accent in the browser. */
export function colourForKind(kind: string, isDir: boolean): string {
  if (isDir) return 'text-primary'
  switch (kind) {
    case 'image':
      return 'text-violet'
    case 'video':
      return 'text-accent'
    case 'audio':
      return 'text-success'
    case 'pdf':
      return 'text-danger'
    case 'archive':
      return 'text-warning'
    case 'code':
      return 'text-secondary'
    default:
      return 'text-muted'
  }
}
