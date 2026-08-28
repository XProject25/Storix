// Shapes returned by the Storix API. They mirror the Go structs exactly.
// Developed by X Project.

export type Kind =
  | 'folder'
  | 'image'
  | 'video'
  | 'audio'
  | 'pdf'
  | 'archive'
  | 'code'
  | 'text'
  | 'document'
  | 'disk'
  | 'font'
  | 'binary'
  | 'other'

export type Role = 'admin' | 'manager' | 'user' | 'readonly' | 'custom'

export type Permission =
  | 'view'
  | 'download'
  | 'upload'
  | 'create'
  | 'rename'
  | 'move'
  | 'copy'
  | 'delete'
  | 'share'
  | 'archive'
  | 'edit'
  | 'advanced'
  | 'users'
  | 'settings'

export interface Entry {
  name: string
  path: string
  isDir: boolean
  size: number
  modified: string
  mode: string
  modeOctal: string
  owner: string
  group: string
  uid: number
  gid: number
  kind: Kind
  mime: string
  ext: string
  hidden: boolean
  symlink: boolean
  linkTarget?: string
  broken?: boolean
  readOnly: boolean
  previewable: boolean
  editable: boolean
  thumbnail: boolean
}

export interface Mount {
  path: string
  label: string
  icon: string
  readOnly: boolean
}

export interface Crumb {
  name: string
  path: string
}

export interface Listing {
  path: string
  parent: string
  mount: Mount
  entries: Entry[]
  total: number
  truncated: boolean
  files: number
  folders: number
  size: number
  readOnly: boolean
  hiddenCount: number
  favorite?: boolean
  canWrite?: boolean
  breadcrumbs?: Crumb[]
  mounts?: Mount[]
  isRoot?: boolean
}

export interface DiskUsage {
  path: string
  filesystem: string
  total: number
  free: number
  used: number
  available: number
  inodesTotal: number
  inodesFree: number
  inodesUsed: number
  percent: number
  inodePercent: number
}

export interface SearchResult {
  entries: Entry[]
  truncated: boolean
  scanned: number
  elapsedMs: number
  query: string
}

export interface TextFile {
  path: string
  name: string
  content: string
  size: number
  truncated: boolean
  binary: boolean
  language: string
  readOnly: boolean
  modified: string
}

export interface TreeNode {
  name: string
  path: string
  hasChildren: boolean
}

export interface Branding {
  name: string
  tagline: string
  logoUrl: string
  accentFrom: string
  accentTo: string
  footer: string
}

export interface User {
  id: number
  username: string
  displayName: string
  email: string
  role: Role
  permissions: Permission[]
  totpEnabled: boolean
  active: boolean
  mustChangePassword: boolean
  theme: string
  locale: string
  quota: number
  lastLoginAt?: string
  lastLoginIp?: string
  createdAt: string
  updatedAt: string
  mounts: StoredMount[]
  sessions?: number
}

export interface StoredMount {
  id: number
  userId: number
  path: string
  label: string
  icon: string
  readOnly: boolean
  sortOrder: number
  createdAt: string
}

export interface Session {
  id: string
  userId: number
  ip: string
  userAgent: string
  createdAt: string
  lastSeenAt: string
  expiresAt: string
  current?: boolean
}

export interface Me {
  user: User
  permissions: Permission[]
  mounts: Mount[]
  csrf: string
  branding: Branding
  limits: { maxUploadSize: number; textEditMaxBytes: number }
  features: { advanced: boolean; shares: boolean; totp: boolean }
}

export interface SystemStatus {
  product: string
  version: string
  setupRequired: boolean
  branding: Branding
  platform: string
  developer: string
}

export type ShareKind = 'download' | 'upload'

export interface Share {
  id: number
  token: string
  ownerId: number
  ownerName?: string
  path: string
  name: string
  kind: ShareKind
  isDir: boolean
  hasPassword: boolean
  allowDownload: boolean
  allowUpload: boolean
  allowList: boolean
  maxDownloads: number
  downloads: number
  note: string
  expiresAt?: string
  lastAccessAt?: string
  createdAt: string
  url?: string
}

export interface PublicShare {
  name: string
  kind: ShareKind
  isDir: boolean
  hasPassword?: boolean
  allowDownload: boolean
  allowUpload: boolean
  allowList: boolean
  note: string
  expiresAt?: string
  owner: string
  entries: Entry[]
  path: string
  breadcrumbs: Crumb[]
}

export interface TrashItem {
  id: number
  userId: number
  name: string
  originalPath: string
  isDir: boolean
  size: number
  deletedAt: string
  expiresAt: string
}

export interface Favorite {
  id: number
  userId: number
  path: string
  name: string
  isDir: boolean
  createdAt: string
}

export interface Recent {
  id: number
  userId: number
  path: string
  name: string
  isDir: boolean
  size: number
  action: string
  at: string
}

export type JobStatus = 'queued' | 'running' | 'done' | 'failed' | 'canceled'

export interface Job {
  id: string
  userId: number
  type: string
  status: JobStatus
  title: string
  message: string
  error?: string
  total: number
  done: number
  totalItems: number
  doneItems: number
  result?: string
  createdAt: string
  updatedAt: string
  startedAt?: string
  finishedAt?: string
  cancellable: boolean
}

export interface AuditEntry {
  id: number
  userId: number
  username: string
  action: string
  target: string
  detail: string
  ip: string
  userAgent: string
  ok: boolean
  at: string
}

export interface Release {
  version: string
  current: string
  available: boolean
  notes: string
  url: string
  publishedAt: string
  asset: string
  size: number
  writable: boolean
  message?: string
}

export interface UploadRecord {
  id: string
  name: string
  dir: string
  offset: number
  size: number
  createdAt: string
  expiresAt: string
}

export interface Dashboard {
  greeting: string
  user: User
  storage: { total: number; used: number; free: number; percent: number; path: string }
  recent: Recent[]
  favorites: Favorite[]
  transfers: { active: number; bytes: number }
  shares: { active: number }
  jobs: Job[]
  trash: { count: number; bytes: number }
  mounts: Array<Mount & { usage?: DiskUsage }>
  version: string
  updateAvailable: boolean
}

export interface SystemInfo {
  build: {
    product: string
    version: string
    commit: string
    date: string
    channel: string
    platform: string
    goVersion: string
    developer: string
  }
  uptime: number
  publicUrl: string
  host?: { hostname: string; os: string; arch: string; cpus: number; goroutines: number }
  memory?: { alloc: number; sys: number }
  database?: { path: string; bytes: number }
  counts?: { users: number; shares: number; jobs: number }
}

export interface Settings {
  branding: Branding
  security: {
    allowAdvanced: boolean
    sessionTtlHours: number
    loginRateBurst: number
    ipAllowlist: string[]
  }
  limits: Record<string, number>
  updates: { channel: string; check: boolean; endpoint: string; interval: string }
  server: { domain: string; tlsMode: string; port: number; publicUrl: string }
  trash: { retentionDays: number }
  restartRequired?: boolean
  changed?: string[]
}

export interface RootFolder {
  id: number
  path: string
  label: string
  icon: string
  readOnly: boolean
  sortOrder: number
  createdAt: string
  exists?: boolean
  usage?: DiskUsage
}

export interface RoleInfo {
  id: Role
  label: string
  permissions: Permission[]
}

export interface PermissionInfo {
  id: Permission
  label: string
  description: string
}

export interface StorixEvent {
  type:
    | 'hello'
    | 'job.created'
    | 'job.progress'
    | 'job.done'
    | 'job.failed'
    | 'fs.changed'
    | 'upload.progress'
    | 'upload.done'
    | 'share.changed'
    | 'system.notice'
  data: unknown
  at: string
}

export type ConflictPolicy = 'rename' | 'overwrite' | 'skip'
export type ViewMode = 'list' | 'grid' | 'gallery'
export type SortField = 'name' | 'size' | 'modified' | 'kind' | 'ext'
export type SortOrder = 'asc' | 'desc'

// ---- 1.1 ------------------------------------------------------------------

export interface Quota {
  limit: number
  used: number
  files: number
  percent: number
  remaining: number
  computedAt: string
  stale: boolean
}

export interface UsageNode {
  name: string
  path: string
  bytes: number
  files: number
  isDir: boolean
  percent: number
  kind: Kind
}

export interface UsageReport {
  path: string
  bytes: number
  files: number
  folders: number
  children: UsageNode[]
  largest: UsageNode[]
  byKind: Array<{ kind: Kind; bytes: number; files: number; percent: number }>
  scanned: number
  truncated: boolean
  elapsedMs: number
}

export type RenameMode = 'replace' | 'prefix' | 'suffix' | 'number' | 'case'

export interface RenameRule {
  mode: RenameMode
  find?: string
  replace?: string
  regex?: boolean
  caseSensitive?: boolean
  text?: string
  start?: number
  padding?: number
  pattern?: string
  casing?: 'lower' | 'upper' | 'title'
  keepExtension?: boolean
}

export interface RenameChange {
  path: string
  from: string
  to: string
  conflict: boolean
  unchanged: boolean
  reason?: string
}

export interface RenamePreview {
  changes: RenameChange[]
  conflicts: number
  unchanged: number
  valid: number
}

// ---- 1.2 ------------------------------------------------------------------

export type TokenScope = 'read' | 'write'

export interface ApiToken {
  id: number
  name: string
  prefix: string
  scope: TokenScope
  expiresAt?: string
  lastUsedAt?: string
  lastUsedIp?: string
  createdAt: string
  expired: boolean
}

export interface DuplicateFile {
  path: string
  name: string
  size: number
  modified: string
}

export interface DuplicateGroup {
  hash: string
  size: number
  count: number
  wasted: number
  files: DuplicateFile[]
}

export interface DuplicateReport {
  path: string
  groups: DuplicateGroup[]
  wasted: number
  scanned: number
  hashed: number
  truncated: boolean
  elapsedMs: number
}

export interface WebDAVInfo {
  enabled: boolean
  url: string
  windows: string
  macos: string
  linux: string
}
