// The path trail above the file browser. It collapses the middle of a deep
// path and accepts files dropped onto any step.
// Developed by X Project.

import { useState, type DragEvent } from 'react'
import clsx from 'clsx'
import { Icon } from './Icon'
import { Menu, type MenuItem } from './ui'
import type { Crumb } from '../lib/types'
import { STORIX_DRAG_TYPE } from './FileList'

export interface BreadcrumbsProps {
  /** The trail from the mount root down to the current folder. */
  crumbs: Crumb[]
  /** Called with the folder path when a step is chosen. */
  onNavigate: (path: string) => void
  /** Called when a selection is dropped onto a step. */
  onDropOnCrumb?: (path: string, event: DragEvent) => void
  className?: string
}

function carriesPaths(event: DragEvent): boolean {
  return Array.from(event.dataTransfer.types).includes(STORIX_DRAG_TYPE)
}

/** Breadcrumbs renders the current location as a row of steps. */
export function Breadcrumbs({ crumbs, onNavigate, onDropOnCrumb, className }: BreadcrumbsProps) {
  const [menu, setMenu] = useState<{ x: number; y: number } | null>(null)
  const [over, setOver] = useState<string | null>(null)

  if (crumbs.length === 0) return null

  const collapsed = crumbs.length > 4
  const first = crumbs[0]
  const middle = collapsed ? crumbs.slice(1, crumbs.length - 2) : []
  const rest = collapsed ? crumbs.slice(crumbs.length - 2) : crumbs.slice(1)
  const currentPath = crumbs[crumbs.length - 1]?.path ?? ''

  const dropHandlers = (path: string) =>
    onDropOnCrumb
      ? {
          onDragOver: (event: DragEvent) => {
            if (!carriesPaths(event)) return
            event.preventDefault()
            event.dataTransfer.dropEffect = event.ctrlKey || event.metaKey ? 'copy' : 'move'
            setOver(path)
          },
          onDragLeave: () => setOver((value) => (value === path ? null : value)),
          onDrop: (event: DragEvent) => {
            if (!carriesPaths(event)) return
            event.preventDefault()
            setOver(null)
            onDropOnCrumb(path, event)
          },
        }
      : {}

  const step = (crumb: Crumb, index: number, isLast: boolean) => (
    <button
      key={`${crumb.path}-${index}`}
      type="button"
      onClick={() => onNavigate(crumb.path)}
      aria-current={isLast ? 'page' : undefined}
      className={clsx(
        'max-w-[16rem] truncate rounded-lg px-2 py-1 text-sm transition-colors',
        isLast ? 'font-medium text-ink' : 'text-muted hover:bg-elevated hover:text-ink',
        over === crumb.path && 'bg-primary/15 text-ink ring-1 ring-primary/50',
      )}
      {...dropHandlers(crumb.path)}
    >
      {crumb.name}
    </button>
  )

  const separator = (key: string) => (
    <Icon key={key} name="chevron-right" size={14} className="shrink-0 text-faint" />
  )

  const hiddenItems: MenuItem[] = middle.map((crumb) => ({
    id: crumb.path,
    label: crumb.name,
    icon: 'folder',
    onSelect: () => onNavigate(crumb.path),
  }))

  return (
    <nav aria-label="Folder path" className={clsx('flex min-w-0 items-center gap-0.5', className)}>
      {step(first, 0, crumbs.length === 1)}
      {collapsed && (
        <>
          {separator('sep-collapse')}
          <button
            type="button"
            aria-label="Show the folders in between"
            title="Show the folders in between"
            onClick={(event) => {
              const rect = event.currentTarget.getBoundingClientRect()
              setMenu({ x: rect.left, y: rect.bottom + 6 })
            }}
            className="rounded-lg px-1.5 py-1 text-muted transition-colors hover:bg-elevated hover:text-ink"
          >
            <Icon name="more" size={16} />
          </button>
        </>
      )}
      {rest.map((crumb, index) => (
        <span key={`${crumb.path}-${index}`} className="flex min-w-0 items-center gap-0.5">
          {separator(`sep-${crumb.path}-${index}`)}
          {step(crumb, index, crumb.path === currentPath)}
        </span>
      ))}
      {menu && <Menu items={hiddenItems} x={menu.x} y={menu.y} onClose={() => setMenu(null)} />}
    </nav>
  )
}
