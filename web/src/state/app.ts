// Interface preferences that survive a reload.
// Developed by X Project.

import { create } from 'zustand'
import { persist } from 'zustand/middleware'
import type { SortField, SortOrder, ViewMode } from '../lib/types'

export type Theme = 'dark' | 'light'

export interface ClipboardState {
  paths: string[]
  mode: 'copy' | 'cut'
}

interface AppState {
  theme: Theme
  view: ViewMode
  sort: SortField
  order: SortOrder
  showHidden: boolean
  foldersFirst: boolean
  sidebarOpen: boolean
  detailsOpen: boolean
  detailsWidth: number
  transfersOpen: boolean
  clipboard: ClipboardState | null
  lastPath: string

  setTheme: (theme: Theme) => void
  toggleTheme: () => void
  setView: (view: ViewMode) => void
  setSort: (sort: SortField, order?: SortOrder) => void
  toggleHidden: () => void
  setFoldersFirst: (value: boolean) => void
  toggleSidebar: () => void
  setDetailsOpen: (open: boolean) => void
  setDetailsWidth: (width: number) => void
  setTransfersOpen: (open: boolean) => void
  setClipboard: (clipboard: ClipboardState | null) => void
  setLastPath: (path: string) => void
}

/** applyTheme writes the theme onto the document so CSS variables switch. */
export function applyTheme(theme: Theme): void {
  const root = document.documentElement
  root.classList.toggle('dark', theme === 'dark')
  root.classList.toggle('light', theme === 'light')
  const meta = document.querySelector('meta[name="theme-color"]')
  if (meta) meta.setAttribute('content', theme === 'dark' ? '#0B0F17' : '#F4F7FC')
}

export const useApp = create<AppState>()(
  persist(
    (set, get) => ({
      theme: 'dark',
      view: 'list',
      sort: 'name',
      order: 'asc',
      showHidden: false,
      foldersFirst: true,
      sidebarOpen: true,
      detailsOpen: true,
      detailsWidth: 340,
      transfersOpen: true,
      clipboard: null,
      lastPath: '',

      setTheme: (theme) => {
        applyTheme(theme)
        set({ theme })
      },
      toggleTheme: () => {
        const next: Theme = get().theme === 'dark' ? 'light' : 'dark'
        applyTheme(next)
        set({ theme: next })
      },
      setView: (view) => set({ view }),
      setSort: (sort, order) =>
        set((state) => ({
          sort,
          order: order ?? (state.sort === sort && state.order === 'asc' ? 'desc' : 'asc'),
        })),
      toggleHidden: () => set((state) => ({ showHidden: !state.showHidden })),
      setFoldersFirst: (foldersFirst) => set({ foldersFirst }),
      toggleSidebar: () => set((state) => ({ sidebarOpen: !state.sidebarOpen })),
      setDetailsOpen: (detailsOpen) => set({ detailsOpen }),
      setDetailsWidth: (detailsWidth) => set({ detailsWidth: Math.min(560, Math.max(280, detailsWidth)) }),
      setTransfersOpen: (transfersOpen) => set({ transfersOpen }),
      setClipboard: (clipboard) => set({ clipboard }),
      setLastPath: (lastPath) => set({ lastPath }),
    }),
    {
      name: 'storix.ui',
      partialize: (state) => ({
        theme: state.theme,
        view: state.view,
        sort: state.sort,
        order: state.order,
        showHidden: state.showHidden,
        foldersFirst: state.foldersFirst,
        sidebarOpen: state.sidebarOpen,
        detailsOpen: state.detailsOpen,
        detailsWidth: state.detailsWidth,
        transfersOpen: state.transfersOpen,
        lastPath: state.lastPath,
      }),
    },
  ),
)
