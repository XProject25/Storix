// The transfer queue. Uploads use the tus resumable protocol, so a broken
// connection continues from the byte it stopped at instead of starting over.
// Developed by X Project.

import * as tus from 'tus-js-client'
import { create } from 'zustand'
import { API_BASE, readCookie } from '../lib/api'

export type TransferStatus = 'queued' | 'uploading' | 'paused' | 'done' | 'error' | 'canceled'

export interface Transfer {
  id: string
  name: string
  dir: string
  relativePath: string
  size: number
  uploaded: number
  status: TransferStatus
  error?: string
  speed: number
  eta: number
  startedAt: number
  finishedAt?: number
  finalPath?: string
}

const MAX_PARALLEL = 3
const CHUNK_SIZE = 8 * 1024 * 1024

interface Entry {
  transfer: Transfer
  file: File
  upload?: tus.Upload
  lastLoaded: number
  lastTime: number
}

const entries = new Map<string, Entry>()

interface TransferState {
  items: Transfer[]
  totalBytes: number
  doneBytes: number
  active: number
  enqueue: (files: Array<{ file: File; relativePath?: string }>, dir: string) => void
  pause: (id: string) => void
  resume: (id: string) => void
  cancel: (id: string) => void
  retry: (id: string) => void
  clearFinished: () => void
  cancelAll: () => void
  onComplete?: (dir: string) => void
  setOnComplete: (fn: (dir: string) => void) => void
}

let counter = 0

function nextID(): string {
  counter += 1
  return `t${Date.now().toString(36)}${counter}`
}

function snapshot(): Transfer[] {
  return Array.from(entries.values()).map((entry) => ({ ...entry.transfer }))
}

/** uploadHeaders carries the CSRF token on every tus request. */
function uploadHeaders(): Record<string, string> {
  const headers: Record<string, string> = {}
  const token = readCookie('storix_csrf')
  if (token) headers['X-Storix-CSRF'] = token
  return headers
}

export const useTransfers = create<TransferState>((set, get) => {
  const sync = () => {
    const items = snapshot()
    const live = items.filter((item) => item.status !== 'done' && item.status !== 'canceled')
    set({
      items,
      totalBytes: live.reduce((sum, item) => sum + item.size, 0),
      doneBytes: live.reduce((sum, item) => sum + item.uploaded, 0),
      active: items.filter((item) => item.status === 'uploading').length,
    })
  }

  /** pump starts queued transfers up to the parallel limit. */
  const pump = () => {
    const running = Array.from(entries.values()).filter((entry) => entry.transfer.status === 'uploading').length
    if (running >= MAX_PARALLEL) return
    for (const entry of entries.values()) {
      if (entry.transfer.status !== 'queued') continue
      start(entry)
      if (Array.from(entries.values()).filter((e) => e.transfer.status === 'uploading').length >= MAX_PARALLEL) break
    }
  }

  const start = (entry: Entry) => {
    entry.transfer.status = 'uploading'
    entry.transfer.startedAt = entry.transfer.startedAt || Date.now()
    entry.lastTime = Date.now()
    entry.lastLoaded = entry.transfer.uploaded

    const upload = new tus.Upload(entry.file, {
      endpoint: API_BASE + '/tus',
      chunkSize: CHUNK_SIZE,
      retryDelays: [0, 1000, 3000, 6000, 12000, 25000],
      parallelUploads: 1,
      storeFingerprintForResuming: true,
      removeFingerprintOnSuccess: true,
      metadata: {
        filename: entry.transfer.name,
        relativePath: entry.transfer.relativePath,
        dir: entry.transfer.dir,
      },
      headers: uploadHeaders(),
      onProgress: (uploaded, total) => {
        const now = Date.now()
        const elapsed = (now - entry.lastTime) / 1000
        if (elapsed > 0.35) {
          const instant = (uploaded - entry.lastLoaded) / elapsed
          // Exponential smoothing keeps the readout steady on a bumpy link.
          entry.transfer.speed = entry.transfer.speed > 0 ? entry.transfer.speed * 0.7 + instant * 0.3 : instant
          entry.lastTime = now
          entry.lastLoaded = uploaded
        }
        entry.transfer.uploaded = uploaded
        entry.transfer.size = total || entry.transfer.size
        entry.transfer.eta =
          entry.transfer.speed > 0 ? Math.max(0, (entry.transfer.size - uploaded) / entry.transfer.speed) : 0
        sync()
      },
      onSuccess: () => {
        entry.transfer.status = 'done'
        entry.transfer.uploaded = entry.transfer.size
        entry.transfer.finishedAt = Date.now()
        entry.transfer.speed = 0
        entry.transfer.eta = 0
        sync()
        get().onComplete?.(entry.transfer.dir)
        pump()
      },
      onError: (error) => {
        entry.transfer.status = 'error'
        entry.transfer.error = friendlyError(error)
        entry.transfer.speed = 0
        sync()
        pump()
      },
    })

    entry.upload = upload
    // Resume where a previous attempt for the same file stopped.
    upload.findPreviousUploads().then((previous) => {
      if (previous.length > 0 && previous[0]) upload.resumeFromPreviousUpload(previous[0])
      upload.start()
    })
    sync()
  }

  return {
    items: [],
    totalBytes: 0,
    doneBytes: 0,
    active: 0,
    onComplete: undefined,

    setOnComplete: (fn) => set({ onComplete: fn }),

    enqueue: (files, dir) => {
      for (const item of files) {
        const id = nextID()
        const relative = item.relativePath ?? ''
        entries.set(id, {
          file: item.file,
          lastLoaded: 0,
          lastTime: Date.now(),
          transfer: {
            id,
            name: item.file.name,
            dir,
            relativePath: relative,
            size: item.file.size,
            uploaded: 0,
            status: 'queued',
            speed: 0,
            eta: 0,
            startedAt: 0,
          },
        })
      }
      sync()
      pump()
    },

    pause: (id) => {
      const entry = entries.get(id)
      if (!entry || entry.transfer.status !== 'uploading') return
      entry.upload?.abort()
      entry.transfer.status = 'paused'
      entry.transfer.speed = 0
      sync()
      pump()
    },

    resume: (id) => {
      const entry = entries.get(id)
      if (!entry) return
      if (entry.transfer.status === 'paused' && entry.upload) {
        entry.transfer.status = 'uploading'
        entry.lastTime = Date.now()
        entry.lastLoaded = entry.transfer.uploaded
        void entry.upload.start()
        sync()
        return
      }
      entry.transfer.status = 'queued'
      sync()
      pump()
    },

    retry: (id) => {
      const entry = entries.get(id)
      if (!entry) return
      entry.transfer.status = 'queued'
      entry.transfer.error = undefined
      entry.transfer.uploaded = 0
      entry.upload = undefined
      sync()
      pump()
    },

    cancel: (id) => {
      const entry = entries.get(id)
      if (!entry) return
      // Terminating removes the partial file on the server as well.
      void entry.upload?.abort(true)
      entry.transfer.status = 'canceled'
      entry.transfer.speed = 0
      entries.delete(id)
      sync()
      pump()
    },

    cancelAll: () => {
      for (const entry of Array.from(entries.values())) {
        if (entry.transfer.status === 'uploading' || entry.transfer.status === 'queued') {
          void entry.upload?.abort(true)
          entries.delete(entry.transfer.id)
        }
      }
      sync()
    },

    clearFinished: () => {
      for (const [id, entry] of Array.from(entries.entries())) {
        if (entry.transfer.status === 'done' || entry.transfer.status === 'canceled') entries.delete(id)
      }
      sync()
    },
  }
})

function friendlyError(error: unknown): string {
  const text = error instanceof Error ? error.message : String(error)
  if (text.includes('403')) return 'You do not have permission to upload here'
  if (text.includes('413')) return 'The file is larger than this server allows'
  if (text.includes('409')) return 'A file with that name is already here'
  if (text.includes('Failed to fetch') || text.includes('network')) return 'Connection lost, the transfer will resume'
  return text.replace(/^tus:\s*/, '')
}

/**
 * collectFiles turns a drop event into a flat list, walking directories so a
 * dropped folder keeps its structure.
 */
export async function collectFiles(dataTransfer: DataTransfer): Promise<Array<{ file: File; relativePath: string }>> {
  const out: Array<{ file: File; relativePath: string }> = []
  const items = Array.from(dataTransfer.items ?? [])
  const roots = items
    .map((item) => (typeof item.webkitGetAsEntry === 'function' ? item.webkitGetAsEntry() : null))
    .filter((entry): entry is FileSystemEntry => Boolean(entry))

  if (roots.length === 0) {
    for (const file of Array.from(dataTransfer.files ?? [])) out.push({ file, relativePath: '' })
    return out
  }

  const walk = async (entry: FileSystemEntry, prefix: string): Promise<void> => {
    if (entry.isFile) {
      const file = await new Promise<File | null>((resolve) => {
        ;(entry as FileSystemFileEntry).file(
          (value) => resolve(value),
          () => resolve(null),
        )
      })
      if (file) out.push({ file, relativePath: prefix ? `${prefix}/${file.name}` : '' })
      return
    }
    if (!entry.isDirectory) return
    const reader = (entry as FileSystemDirectoryEntry).createReader()
    const children: FileSystemEntry[] = []
    for (;;) {
      const batch = await new Promise<FileSystemEntry[]>((resolve) => {
        reader.readEntries(
          (value) => resolve(value),
          () => resolve([]),
        )
      })
      if (batch.length === 0) break
      children.push(...batch)
    }
    for (const child of children) {
      await walk(child, prefix ? `${prefix}/${entry.name}` : entry.name)
    }
  }

  for (const root of roots) await walk(root, '')
  return out
}

/** fromInput reads files chosen through a file or folder input. */
export function fromInput(list: FileList | null): Array<{ file: File; relativePath: string }> {
  if (!list) return []
  return Array.from(list).map((file) => ({
    file,
    relativePath: (file as File & { webkitRelativePath?: string }).webkitRelativePath ?? '',
  }))
}
