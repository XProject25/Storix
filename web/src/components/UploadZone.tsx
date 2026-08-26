// The drop target. It accepts dragged files and folders and opens the device
// file picker, then hands everything to the transfer queue.
// Developed by X Project.

import clsx from 'clsx'
import { useCallback, useEffect, useRef, useState, type DragEvent } from 'react'
import { Icon } from './Icon'
import { useApp } from '../state/app'
import { collectFiles, fromInput, useTransfers } from '../state/transfers'

export interface UploadZoneProps {
  /** Absolute folder the files land in. */
  path: string
  className?: string
  /** compact renders a single line version for tight spaces. */
  compact?: boolean
}

type Picked = Array<{ file: File; relativePath: string }>

/** carriesFiles reports whether a drag actually holds files rather than text. */
function carriesFiles(event: DragEvent<HTMLElement>): boolean {
  const types = event.dataTransfer?.types
  if (!types) return false
  return Array.from(types).includes('Files')
}

export default function UploadZone({ path, className, compact }: UploadZoneProps) {
  const enqueue = useTransfers((state) => state.enqueue)
  const setTransfersOpen = useApp((state) => state.setTransfersOpen)
  const fileInput = useRef<HTMLInputElement>(null)
  const folderInput = useRef<HTMLInputElement>(null)
  // A counter, so moving over a child element does not flicker the state.
  const depth = useRef(0)
  const [dragging, setDragging] = useState(false)

  // These attributes are not in the React typings, so they are set directly.
  useEffect(() => {
    const element = folderInput.current
    if (!element) return
    element.setAttribute('webkitdirectory', '')
    element.setAttribute('directory', '')
  }, [])

  const add = useCallback(
    (picked: Picked) => {
      if (picked.length === 0) return
      enqueue(picked, path)
      setTransfersOpen(true)
    },
    [enqueue, path, setTransfersOpen],
  )

  const onDragEnter = useCallback((event: DragEvent<HTMLDivElement>) => {
    if (!carriesFiles(event)) return
    event.preventDefault()
    depth.current += 1
    setDragging(true)
  }, [])

  const onDragOver = useCallback((event: DragEvent<HTMLDivElement>) => {
    if (!carriesFiles(event)) return
    event.preventDefault()
    event.dataTransfer.dropEffect = 'copy'
  }, [])

  const onDragLeave = useCallback((event: DragEvent<HTMLDivElement>) => {
    if (depth.current === 0) return
    event.preventDefault()
    depth.current -= 1
    if (depth.current <= 0) {
      depth.current = 0
      setDragging(false)
    }
  }, [])

  const onDrop = useCallback(
    (event: DragEvent<HTMLDivElement>) => {
      event.preventDefault()
      depth.current = 0
      setDragging(false)
      if (!event.dataTransfer) return
      void collectFiles(event.dataTransfer).then(add)
    },
    [add],
  )

  const inputs = (
    <>
      <input
        ref={fileInput}
        type="file"
        multiple
        className="hidden"
        onChange={(event) => {
          add(fromInput(event.target.files))
          event.target.value = ''
        }}
      />
      <input
        ref={folderInput}
        type="file"
        multiple
        className="hidden"
        onChange={(event) => {
          add(fromInput(event.target.files))
          event.target.value = ''
        }}
      />
    </>
  )

  const dragProps = {
    onDragEnter,
    onDragOver,
    onDragLeave,
    onDrop,
  }

  if (compact) {
    return (
      <div
        {...dragProps}
        className={clsx(
          'flex items-center gap-2.5 rounded-xl border border-dashed px-3 py-2 transition-colors',
          dragging ? 'border-primary bg-primary/10' : 'border-line',
          className,
        )}
      >
        <Icon name="cloud-upload" size={16} className={dragging ? 'text-primary' : 'text-faint'} />
        <span className="min-w-0 flex-1 truncate text-xs text-muted">
          Drag and drop files here to upload, or click to{' '}
          <button
            type="button"
            className="font-medium text-primary underline-offset-2 hover:underline"
            onClick={() => fileInput.current?.click()}
          >
            browse your device
          </button>
        </span>
        {inputs}
      </div>
    )
  }

  return (
    <div
      {...dragProps}
      onClick={() => fileInput.current?.click()}
      className={clsx(
        'flex flex-col items-center justify-center rounded-2xl border border-dashed px-6 py-10 text-center transition-colors',
        dragging ? 'border-primary bg-primary/10' : 'border-line hover:border-faint',
        className,
      )}
    >
      <span
        className={clsx(
          'mb-4 flex h-14 w-14 items-center justify-center rounded-2xl transition-colors',
          dragging ? 'bg-primary/20 text-primary' : 'bg-elevated text-faint',
        )}
      >
        <Icon name="cloud-upload" size={26} />
      </span>
      <p className="text-[15px] font-medium text-ink">Drag and drop files here to upload</p>
      <p className="mt-1.5 text-sm text-muted">
        or click to{' '}
        <button
          type="button"
          className="font-medium text-primary underline-offset-2 hover:underline"
          onClick={(event) => {
            event.stopPropagation()
            fileInput.current?.click()
          }}
        >
          browse your device
        </button>
      </p>
      <button
        type="button"
        className="mt-3 inline-flex items-center gap-1.5 text-xs text-faint transition-colors hover:text-ink"
        onClick={(event) => {
          event.stopPropagation()
          folderInput.current?.click()
        }}
      >
        <Icon name="folder-plus" size={14} />
        Upload a whole folder
      </button>
      {inputs}
    </div>
  )
}
