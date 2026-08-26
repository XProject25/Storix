// Typed client for the Storix API.
// Developed by X Project.

import type {
  AuditEntry,
  ConflictPolicy,
  Dashboard,
  DiskUsage,
  Entry,
  Favorite,
  Job,
  Listing,
  Me,
  PermissionInfo,
  PublicShare,
  Recent,
  Release,
  RoleInfo,
  RootFolder,
  SearchResult,
  Session,
  Settings,
  Share,
  ShareKind,
  SystemInfo,
  SystemStatus,
  TextFile,
  TrashItem,
  TreeNode,
  Quota,
  RenamePreview,
  RenameRule,
  UploadRecord,
  UsageReport,
  User,
} from './types'

export const API_BASE = '/api/v1'

/** ApiError carries the machine readable code the server sent. */
export class ApiError extends Error {
  readonly code: string
  readonly status: number
  readonly detail: string

  constructor(status: number, code: string, message: string, detail = '') {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
    this.detail = detail
  }

  /** is reports whether the error carries a given code. */
  is(code: string): boolean {
    return this.code === code
  }
}

/** readCookie returns a browser cookie value. */
export function readCookie(name: string): string {
  const target = name + '='
  for (const part of document.cookie.split(';')) {
    const trimmed = part.trim()
    if (trimmed.startsWith(target)) return decodeURIComponent(trimmed.slice(target.length))
  }
  return ''
}

let csrfToken = ''

/** setCSRF stores the token returned by login, setup or the session probe. */
export function setCSRF(token: string): void {
  csrfToken = token
}

function csrf(): string {
  return csrfToken || readCookie('storix_csrf')
}

interface RequestOptions {
  method?: string
  body?: unknown
  signal?: AbortSignal
  raw?: boolean
}

async function request<T>(path: string, options: RequestOptions = {}): Promise<T> {
  const method = options.method ?? 'GET'
  const headers: Record<string, string> = { Accept: 'application/json' }
  let body: BodyInit | undefined

  if (options.body !== undefined) {
    headers['Content-Type'] = 'application/json'
    body = JSON.stringify(options.body)
  }
  if (method !== 'GET' && method !== 'HEAD') {
    const token = csrf()
    if (token) headers['X-Storix-CSRF'] = token
  }

  const response = await fetch(API_BASE + path, {
    method,
    headers,
    body,
    credentials: 'same-origin',
    signal: options.signal,
  })

  if (response.status === 204) return undefined as T

  const text = await response.text()
  let payload: unknown = null
  if (text) {
    try {
      payload = JSON.parse(text)
    } catch {
      payload = null
    }
  }

  if (!response.ok) {
    const envelope = payload as { error?: { code?: string; message?: string; detail?: string } } | null
    throw new ApiError(
      response.status,
      envelope?.error?.code ?? 'error',
      envelope?.error?.message ?? response.statusText ?? 'Request failed',
      envelope?.error?.detail ?? '',
    )
  }
  return payload as T
}

function query(params: Record<string, string | number | boolean | undefined | null>): string {
  const search = new URLSearchParams()
  for (const [key, value] of Object.entries(params)) {
    if (value === undefined || value === null || value === '') continue
    search.set(key, String(value))
  }
  const out = search.toString()
  return out ? `?${out}` : ''
}

/** fileURL builds a direct link the browser can fetch, for example a download. */
export function fileURL(endpoint: string, params: Record<string, string | number | boolean | undefined>): string {
  return API_BASE + endpoint + query(params)
}

export const api = {
  // ---- system and session ------------------------------------------------
  status: () => request<SystemStatus>('/system/status'),
  setup: (body: {
    token: string
    username: string
    password: string
    displayName?: string
    email?: string
    folders: string[]
    domain?: string
  }) => request<{ ok: boolean; user: User; csrf: string; warning?: string }>('/setup', { method: 'POST', body }),

  login: (body: { username: string; password: string; totp?: string; remember?: boolean }) =>
    request<{ ok: boolean; user: User; csrf: string; mustChangePassword: boolean }>('/auth/login', {
      method: 'POST',
      body,
    }),
  logout: () => request<{ ok: boolean }>('/auth/logout', { method: 'POST' }),
  me: () => request<Me>('/auth/me'),
  changePassword: (body: { current: string; new: string }) =>
    request<{ ok: boolean }>('/auth/password', { method: 'POST', body }),
  totpSetup: () => request<{ secret: string; uri: string; recovery: string[] }>('/auth/totp/setup', { method: 'POST' }),
  totpEnable: (code: string) => request<{ ok: boolean }>('/auth/totp/enable', { method: 'POST', body: { code } }),
  totpDisable: (password: string) =>
    request<{ ok: boolean }>('/auth/totp/disable', { method: 'POST', body: { password } }),
  sessions: () => request<{ sessions: Session[] }>('/auth/sessions'),
  revokeSession: (id: string) => request<{ ok: boolean }>(`/auth/sessions/${encodeURIComponent(id)}`, { method: 'DELETE' }),
  savePreferences: (body: Record<string, unknown>) =>
    request<Record<string, unknown>>('/auth/preferences', { method: 'POST', body }),
  roles: () => request<{ roles: RoleInfo[]; permissions: PermissionInfo[] }>('/roles'),

  // ---- browsing ----------------------------------------------------------
  list: (params: {
    path: string
    hidden?: boolean
    sort?: string
    order?: string
    filter?: string
    limit?: number
  }) => request<Listing>('/fs/list' + query(params)),
  stat: (path: string) => request<Entry & { favorite: boolean }>('/fs/stat' + query({ path })),
  tree: (path: string, depth = 1) => request<{ path: string; children: TreeNode[] }>('/fs/tree' + query({ path, depth })),
  search: (params: { q: string; path?: string; content?: boolean; hidden?: boolean; limit?: number; kind?: string }) =>
    request<SearchResult>('/fs/search' + query(params)),
  du: (path: string) => request<{ path: string; bytes: number; items: number }>('/fs/du' + query({ path })),
  disk: (path: string) => request<DiskUsage>('/fs/disk' + query({ path })),

  // ---- writing -----------------------------------------------------------
  mkdir: (path: string, name: string) => request<Entry>('/fs/mkdir', { method: 'POST', body: { path, name } }),
  touch: (path: string, name: string, content = '') =>
    request<Entry>('/fs/touch', { method: 'POST', body: { path, name, content } }),
  rename: (path: string, name: string) => request<Entry>('/fs/rename', { method: 'POST', body: { path, name } }),
  move: (sources: string[], dest: string, conflict: ConflictPolicy = 'rename') =>
    request<Job>('/fs/move', { method: 'POST', body: { sources, dest, conflict } }),
  copy: (sources: string[], dest: string, conflict: ConflictPolicy = 'rename') =>
    request<Job>('/fs/copy', { method: 'POST', body: { sources, dest, conflict } }),
  remove: (paths: string[], permanent = false) =>
    request<{ ok?: boolean; job?: Job; removed?: number }>('/fs/delete', {
      method: 'POST',
      body: { paths, permanent },
    }),
  readText: (path: string) => request<TextFile>('/fs/text' + query({ path })),
  writeText: (path: string, content: string) => request<Entry>('/fs/text', { method: 'PUT', body: { path, content } }),
  chmod: (path: string, mode: string, recursive = false) =>
    request<{ ok: boolean }>('/fs/chmod', { method: 'POST', body: { path, mode, recursive } }),
  chown: (path: string, owner: string, group: string, recursive = false) =>
    request<{ ok: boolean }>('/fs/chown', { method: 'POST', body: { path, owner, group, recursive } }),
  compress: (body: { sources: string[]; dest: string; name: string; format: string }) =>
    request<Job>('/fs/compress', { method: 'POST', body }),
  extract: (path: string, dest: string) => request<Job>('/fs/extract', { method: 'POST', body: { path, dest } }),
  archivePreview: (path: string, limit = 200) =>
    request<{ format: string; items: Array<{ name: string; size: number; isDir: boolean; modified: string }>; truncated: boolean }>(
      '/fs/archive' + query({ path, limit }),
    ),

  // ---- direct links ------------------------------------------------------
  downloadURL: (path: string) => fileURL('/fs/download', { path }),
  rawURL: (path: string) => fileURL('/fs/raw', { path }),
  thumbURL: (path: string, size = 256) => fileURL('/fs/thumb', { path, size }),
  zipURL: (paths: string[], name?: string) => {
    // Repeated path parameters, so a file name holding a comma stays intact.
    const search = new URLSearchParams()
    for (const path of paths) search.append('path', path)
    if (name) search.set('name', name)
    return `${API_BASE}/fs/download-zip?${search.toString()}`
  },

  // ---- recycle bin and shortcuts ----------------------------------------
  trash: () => request<{ items: TrashItem[]; count: number; bytes: number; retentionDays: number }>('/trash'),
  trashRestore: (ids: number[]) =>
    request<{ restored: number; failed: Array<{ id: number; reason: string }> }>('/trash/restore', {
      method: 'POST',
      body: { ids },
    }),
  trashDelete: (ids: number[]) => request<{ ok: boolean }>('/trash/delete', { method: 'POST', body: { ids } }),
  trashEmpty: (allUsers = false) => request<{ ok: boolean }>('/trash/empty', { method: 'POST', body: { allUsers } }),
  favorites: () => request<{ favorites: Favorite[] }>('/favorites'),
  addFavorite: (path: string) => request<Favorite>('/favorites', { method: 'POST', body: { path } }),
  removeFavorite: (path: string) => request<{ ok: boolean }>('/favorites' + query({ path }), { method: 'DELETE' }),
  recent: (limit = 30) => request<{ recent: Recent[] }>('/recent' + query({ limit })),

  // ---- shares ------------------------------------------------------------
  shares: (all = false) => request<{ shares: Share[] }>('/shares' + query({ all })),
  createShare: (body: {
    path: string
    kind: ShareKind
    password?: string
    expiresIn?: string
    maxDownloads?: number
    allowDownload?: boolean
    allowUpload?: boolean
    allowList?: boolean
    note?: string
  }) => request<Share>('/shares', { method: 'POST', body }),
  updateShare: (id: number, body: Record<string, unknown>) =>
    request<Share>(`/shares/${id}`, { method: 'PATCH', body }),
  deleteShare: (id: number) => request<{ ok: boolean }>(`/shares/${id}`, { method: 'DELETE' }),

  publicMeta: (token: string, path?: string) =>
    request<PublicShare>(`/public/${encodeURIComponent(token)}` + query({ path })),
  publicAuth: (token: string, password: string) =>
    request<{ ok: boolean }>(`/public/${encodeURIComponent(token)}/auth`, { method: 'POST', body: { password } }),
  publicDownloadURL: (token: string, path?: string) =>
    fileURL(`/public/${encodeURIComponent(token)}/download`, { path }),
  publicRawURL: (token: string, path?: string) => fileURL(`/public/${encodeURIComponent(token)}/raw`, { path }),
  publicThumbURL: (token: string, path?: string, size = 256) =>
    fileURL(`/public/${encodeURIComponent(token)}/thumb`, { path, size }),
  publicZipURL: (token: string, path?: string) => fileURL(`/public/${encodeURIComponent(token)}/download-zip`, { path }),

  // ---- accounts ----------------------------------------------------------
  users: () => request<{ users: User[] }>('/users'),
  createUser: (body: Record<string, unknown>) => request<User>('/users', { method: 'POST', body }),
  updateUser: (id: number, body: Record<string, unknown>) => request<User>(`/users/${id}`, { method: 'PATCH', body }),
  deleteUser: (id: number) => request<{ ok: boolean }>(`/users/${id}`, { method: 'DELETE' }),

  // ---- operations --------------------------------------------------------
  jobs: (limit = 25, all = false) => request<{ jobs: Job[] }>('/jobs' + query({ limit, all })),
  job: (id: string) => request<Job>(`/jobs/${encodeURIComponent(id)}`),
  cancelJob: (id: string) => request<{ ok: boolean }>(`/jobs/${encodeURIComponent(id)}/cancel`, { method: 'POST' }),
  uploads: () => request<{ uploads: UploadRecord[]; active: number; bytes: number }>('/uploads'),

  // ---- administration ----------------------------------------------------
  dashboard: () => request<Dashboard>('/dashboard'),
  systemInfo: () => request<SystemInfo>('/system/info'),
  settings: () => request<Settings>('/system/settings'),
  saveSettings: (body: Partial<Settings>) => request<Settings>('/system/settings', { method: 'PUT', body }),
  roots: () => request<{ roots: RootFolder[] }>('/system/roots'),
  addRoot: (body: { path: string; label?: string; icon?: string; readOnly?: boolean }) =>
    request<RootFolder>('/system/roots', { method: 'POST', body }),
  updateRoot: (id: number, body: Record<string, unknown>) =>
    request<RootFolder>(`/system/roots/${id}`, { method: 'PATCH', body }),
  deleteRoot: (id: number) => request<{ ok: boolean; message?: string }>(`/system/roots/${id}`, { method: 'DELETE' }),
  browseServer: (path: string) =>
    request<{ path: string; parent: string; dirs: Array<{ name: string; path: string; readable: boolean }> }>(
      '/system/browse' + query({ path }),
    ),
  audit: (params: { limit?: number; action?: string; q?: string; user?: string }) =>
    request<{ entries: AuditEntry[]; total: number }>('/system/audit' + query(params)),
  updateCheck: () => request<Release>('/system/update/check'),
  applyUpdate: () => request<Job>('/system/update', { method: 'POST' }),
  setDomain: (body: { domain: string; email?: string; enable: boolean }) =>
    request<{ ok: boolean; restartRequired: boolean; url: string; message?: string }>('/system/domain', {
      method: 'POST',
      body,
    }),

  // ---- storage insight and bulk rename, added in 1.1 ---------------------
  usage: (path: string, limit = 40) => request<UsageReport>('/fs/usage' + query({ path, limit })),
  quota: () => request<Quota>('/auth/quota'),
  userQuota: (id: number) => request<Quota>(`/users/${id}/quota`),
  renamePreview: (paths: string[], rule: RenameRule) =>
    request<RenamePreview>('/fs/rename-bulk/preview', { method: 'POST', body: { paths, rule } }),
  renameBulk: (paths: string[], rule: RenameRule) =>
    request<{ renamed: number; failed: Array<{ path: string; reason: string }> }>('/fs/rename-bulk', {
      method: 'POST',
      body: { paths, rule },
    }),
}

/**
 * subscribe opens the server sent event stream. The returned function closes it.
 * The browser reconnects on its own, so only unexpected failures are surfaced.
 */
export function subscribe(onEvent: (type: string, data: unknown) => void, onError?: () => void): () => void {
  const source = new EventSource(API_BASE + '/events', { withCredentials: true })
  const types = [
    'hello',
    'job.created',
    'job.progress',
    'job.done',
    'job.failed',
    'fs.changed',
    'upload.progress',
    'upload.done',
    'share.changed',
    'system.notice',
  ]
  const handlers: Array<[string, EventListener]> = []
  for (const type of types) {
    const handler: EventListener = (event) => {
      const message = event as MessageEvent
      let data: unknown = null
      try {
        data = message.data ? JSON.parse(message.data) : null
      } catch {
        data = message.data
      }
      onEvent(type, data)
    }
    source.addEventListener(type, handler)
    handlers.push([type, handler])
  }
  source.onerror = () => onError?.()
  return () => {
    for (const [type, handler] of handlers) source.removeEventListener(type, handler)
    source.close()
  }
}
