// The full text editor. Monaco is fetched only when a file is opened for
// editing, so the first paint of the file browser stays small.
// Developed by X Project.

import clsx from 'clsx'
import { useCallback, useEffect, useRef, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import type * as MonacoApi from 'monaco-editor'
import editorWorker from 'monaco-editor/esm/vs/editor/editor.worker?worker'
import { ApiError, api } from '../lib/api'
import { baseName, bytes, joinPath, parentPath, smartDate } from '../lib/format'
import type { TextFile } from '../lib/types'
import { useApp } from '../state/app'
import { Icon } from './Icon'
import { Button, ConfirmDialog, Field, IconButton, Modal, Skeleton, Spinner, useToast } from './ui'

type MonacoModule = typeof MonacoApi

export interface CodeEditorProps {
  path: string
  onClose: () => void
  onSaved?: () => void
}

// ---- worker wiring ----------------------------------------------------------

interface WorkerFactory {
  getWorker(): Worker
}

let workersConfigured = false

/**
 * configureWorkers points Monaco at the one worker Storix bundles.
 *
 * Only the base editor worker is shipped. The language service workers exist
 * to give TypeScript, JSON, CSS and HTML full IntelliSense, and the TypeScript
 * one alone is six megabytes of generated code. Storix is used to edit server
 * configuration, YAML, shell scripts and logs, where syntax highlighting is
 * what matters, and highlighting is done in the main worker for every language
 * Monaco knows. Dropping the rest keeps the download small and the build sane.
 */
function configureWorkers(): void {
  if (workersConfigured) return
  workersConfigured = true
  const host = self as unknown as { MonacoEnvironment: WorkerFactory }
  host.MonacoEnvironment = {
    getWorker() {
      return new editorWorker()
    },
  }
}

// ---- themes -----------------------------------------------------------------

// Monaco only accepts literal hex, so these values mirror the design tokens in
// styles.css rather than replacing them.
let themesDefined = false

function defineThemes(monaco: MonacoModule): void {
  if (themesDefined) return
  themesDefined = true

  monaco.editor.defineTheme('storix-dark', {
    base: 'vs-dark',
    inherit: true,
    rules: [
      { token: '', foreground: 'EDF2FA' },
      { token: 'comment', foreground: '6B7280', fontStyle: 'italic' },
      { token: 'keyword', foreground: '00D4FF' },
      { token: 'string', foreground: '34D399' },
      { token: 'number', foreground: 'FBBF24' },
      { token: 'type', foreground: 'D946EF' },
      { token: 'type.identifier', foreground: 'D946EF' },
      { token: 'tag', foreground: '00D4FF' },
      { token: 'attribute.name', foreground: '7C3AED' },
      { token: 'delimiter', foreground: '9CA3AF' },
    ],
    colors: {
      'editor.background': '#0B0F17',
      'editor.foreground': '#EDF2FA',
      'editorGutter.background': '#0B0F17',
      'editorLineNumber.foreground': '#4B5563',
      'editorLineNumber.activeForeground': '#9CA3AF',
      'editor.lineHighlightBackground': '#111827',
      'editor.selectionBackground': '#00D4FF33',
      'editor.inactiveSelectionBackground': '#00D4FF1F',
      'editorCursor.foreground': '#00D4FF',
      'editorIndentGuide.background1': '#232A3A',
      'editorWidget.background': '#111827',
      'editorWidget.border': '#232A3A',
      'editorSuggestWidget.background': '#111827',
      'editorSuggestWidget.border': '#232A3A',
      'editorHoverWidget.background': '#111827',
      'editorHoverWidget.border': '#232A3A',
      'scrollbarSlider.background': '#232A3A99',
      'scrollbarSlider.hoverBackground': '#232A3ACC',
      'scrollbarSlider.activeBackground': '#00D4FF66',
    },
  })

  monaco.editor.defineTheme('storix-light', {
    base: 'vs',
    inherit: true,
    rules: [
      { token: '', foreground: '0C1421' },
      { token: 'comment', foreground: '8C98AA', fontStyle: 'italic' },
      { token: 'keyword', foreground: '0060D6' },
      { token: 'string', foreground: '059669' },
      { token: 'number', foreground: 'CA8A04' },
      { token: 'type', foreground: 'C026D3' },
      { token: 'type.identifier', foreground: 'C026D3' },
      { token: 'tag', foreground: '0091BE' },
      { token: 'attribute.name', foreground: '6D28D9' },
      { token: 'delimiter', foreground: '5C697D' },
    ],
    colors: {
      'editor.background': '#FFFFFF',
      'editor.foreground': '#0C1421',
      'editorGutter.background': '#FFFFFF',
      'editorLineNumber.foreground': '#8C98AA',
      'editorLineNumber.activeForeground': '#5C697D',
      'editor.lineHighlightBackground': '#F4F7FC',
      'editor.selectionBackground': '#0091BE2E',
      'editorCursor.foreground': '#0091BE',
      'editorIndentGuide.background1': '#E0E7F0',
      'editorWidget.background': '#FFFFFF',
      'editorWidget.border': '#E0E7F0',
      'scrollbarSlider.background': '#E0E7F099',
    },
  })
}

function messageOf(error: unknown): string {
  if (error instanceof ApiError) return error.detail || error.message
  if (error instanceof Error) return error.message
  return 'Something went wrong.'
}

// ---- component --------------------------------------------------------------

export default function CodeEditor({ path, onClose, onSaved }: CodeEditorProps) {
  const theme = useApp((state) => state.theme)
  const toast = useToast()
  const client = useQueryClient()

  const hostRef = useRef<HTMLDivElement>(null)
  const editorRef = useRef<MonacoApi.editor.IStandaloneCodeEditor | null>(null)
  const monacoRef = useRef<MonacoModule | null>(null)
  const baselineRef = useRef('')
  const saveRef = useRef<() => void>(() => undefined)

  const [loaded, setLoaded] = useState(false)
  const [loadFailed, setLoadFailed] = useState(false)
  const [doc, setDoc] = useState<TextFile | null>(null)
  const [dirty, setDirty] = useState(false)
  const [wrap, setWrap] = useState(false)
  const [confirmClose, setConfirmClose] = useState(false)
  const [confirmReload, setConfirmReload] = useState(false)
  const [saveAsOpen, setSaveAsOpen] = useState(false)
  const [saveAsName, setSaveAsName] = useState('')
  const [saveAsError, setSaveAsError] = useState('')

  const text = useQuery({
    queryKey: ['text', path],
    queryFn: () => api.readText(path),
    staleTime: Infinity,
  })

  // A truncated payload only holds the first slice of the file, so saving it
  // back would throw the rest away. Treat it as read only.
  const truncated = doc?.truncated ?? false
  const readOnly = doc ? doc.readOnly || truncated : true
  const docReady = doc !== null

  // Seed the editor once. Later refreshes are explicit, so a background refetch
  // can never discard what someone is typing.
  useEffect(() => {
    if (text.data && !doc) {
      baselineRef.current = text.data.content
      setDoc(text.data)
    }
  }, [text.data, doc])

  // Load Monaco itself.
  useEffect(() => {
    let cancelled = false
    void import('monaco-editor')
      .then((module) => {
        if (cancelled) return
        configureWorkers()
        defineThemes(module)
        monacoRef.current = module
        setLoaded(true)
      })
      .catch(() => {
        if (!cancelled) setLoadFailed(true)
      })
    return () => {
      cancelled = true
    }
  }, [])

  // Create the editor once both the module and the file are in hand.
  useEffect(() => {
    const monaco = monacoRef.current
    const host = hostRef.current
    if (!loaded || !monaco || !host || !docReady || !doc) return

    const editor = monaco.editor.create(host, {
      value: doc.content,
      language: doc.language || 'plaintext',
      theme: useApp.getState().theme === 'light' ? 'storix-light' : 'storix-dark',
      readOnly: doc.readOnly,
      automaticLayout: false,
      minimap: { enabled: false },
      tabSize: 2,
      insertSpaces: true,
      wordWrap: 'off',
      fontSize: 13,
      lineHeight: 20,
      fontFamily: 'JetBrains Mono, ui-monospace, SFMono-Regular, Menlo, Consolas, monospace',
      fontLigatures: false,
      scrollBeyondLastLine: false,
      renderLineHighlight: 'gutter',
      smoothScrolling: true,
      cursorBlinking: 'smooth',
      roundedSelection: false,
      padding: { top: 12, bottom: 24 },
      scrollbar: { verticalScrollbarSize: 10, horizontalScrollbarSize: 10 },
    })
    editorRef.current = editor

    const changed = editor.onDidChangeModelContent(() => {
      setDirty(editor.getValue() !== baselineRef.current)
    })
    editor.addCommand(monaco.KeyMod.CtrlCmd | monaco.KeyCode.KeyS, () => saveRef.current())

    const observer = new ResizeObserver(() => editor.layout())
    observer.observe(host)
    editor.layout()

    return () => {
      observer.disconnect()
      changed.dispose()
      editor.getModel()?.dispose()
      editor.dispose()
      editorRef.current = null
    }
    // The file is read once at creation. Reload replaces the value in place,
    // so a changing doc reference must never tear the editor down.
  }, [loaded, docReady])

  useEffect(() => {
    monacoRef.current?.editor.setTheme(theme === 'light' ? 'storix-light' : 'storix-dark')
  }, [theme, loaded])

  useEffect(() => {
    editorRef.current?.updateOptions({ wordWrap: wrap ? 'on' : 'off' })
  }, [wrap])

  const save = useMutation({
    mutationFn: (content: string) => api.writeText(path, content),
    onSuccess: (_result, content) => {
      baselineRef.current = content
      setDirty(false)
      client.setQueryData<TextFile>(['text', path], (previous) =>
        previous ? { ...previous, content, truncated: false } : previous,
      )
      void client.invalidateQueries({ queryKey: ['list', parentPath(path)] })
      void client.invalidateQueries({ queryKey: ['recent'] })
      toast.success('Saved', baseName(path))
      onSaved?.()
    },
    onError: (error) => toast.error('Could not save', messageOf(error)),
  })

  const saveAs = useMutation({
    mutationFn: (target: string) => api.writeText(target, editorRef.current?.getValue() ?? ''),
    onSuccess: (_result, target) => {
      setSaveAsOpen(false)
      void client.invalidateQueries({ queryKey: ['list', parentPath(target)] })
      toast.success('Copy saved', baseName(target))
      onSaved?.()
    },
    onError: (error) => setSaveAsError(messageOf(error)),
  })

  saveRef.current = () => {
    const editor = editorRef.current
    if (!editor || readOnly || save.isPending) return
    save.mutate(editor.getValue())
  }

  const reload = useCallback(async () => {
    setConfirmReload(false)
    try {
      const fresh = await client.fetchQuery({
        queryKey: ['text', path],
        queryFn: () => api.readText(path),
        staleTime: 0,
      })
      baselineRef.current = fresh.content
      setDoc(fresh)
      const editor = editorRef.current
      if (editor) {
        const position = editor.getPosition()
        editor.setValue(fresh.content)
        if (position) editor.setPosition(position)
      }
      setDirty(false)
      toast.info('Reloaded', baseName(path))
    } catch (error) {
      toast.error('Could not reload', messageOf(error))
    }
  }, [client, path, toast])

  const requestClose = useCallback(() => {
    if (dirty) {
      setConfirmClose(true)
      return
    }
    onClose()
  }, [dirty, onClose])

  // Ctrl+S works even when the caret is outside the editing surface.
  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === 's') {
        event.preventDefault()
        saveRef.current()
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])

  useEffect(() => {
    if (!dirty) return
    const onLeave = (event: BeforeUnloadEvent) => {
      event.preventDefault()
      event.returnValue = ''
    }
    window.addEventListener('beforeunload', onLeave)
    return () => window.removeEventListener('beforeunload', onLeave)
  }, [dirty])

  const failed = loadFailed || text.isError
  const waiting = !failed && (!loaded || !docReady)

  return (
    <div className="flex h-full min-h-0 w-full flex-col overflow-hidden bg-surface">
      <header className="flex shrink-0 flex-wrap items-center gap-2 border-b border-line px-3 py-2">
        <span className="flex h-8 w-8 shrink-0 items-center justify-center rounded-xl bg-elevated text-secondary">
          <Icon name="code" size={16} />
        </span>
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2">
            <span className="truncate text-sm font-medium text-ink" title={path}>
              {baseName(path)}
            </span>
            {dirty && (
              <span
                className="h-1.5 w-1.5 shrink-0 rounded-full bg-primary"
                aria-hidden="true"
              />
            )}
          </div>
          <p className="truncate text-xs text-faint">
            {dirty ? 'Unsaved changes' : doc ? `${bytes(doc.size)}, saved ${smartDate(doc.modified)}` : 'Loading'}
          </p>
        </div>

        <div className="flex shrink-0 items-center gap-1">
          <IconButton
            icon="list"
            label="Word wrap"
            active={wrap}
            onClick={() => setWrap((value) => !value)}
            disabled={!docReady}
          />
          <IconButton
            icon="refresh"
            label="Reload from disk"
            onClick={() => (dirty ? setConfirmReload(true) : void reload())}
            disabled={!docReady}
          />
          {!readOnly && (
            <>
              <Button
                icon="copy"
                onClick={() => {
                  setSaveAsError('')
                  setSaveAsName(baseName(path))
                  setSaveAsOpen(true)
                }}
                disabled={!docReady}
              >
                Save as
              </Button>
              <Button
                variant="primary"
                icon="check"
                loading={save.isPending}
                disabled={!dirty || !docReady}
                onClick={() => saveRef.current()}
              >
                Save
              </Button>
            </>
          )}
          <IconButton icon="close" label="Close editor" onClick={requestClose} />
        </div>
      </header>

      {docReady && truncated && (
        <div className="flex shrink-0 items-center gap-2 border-b border-line bg-elevated px-4 py-2 text-xs text-muted">
          <Icon name="info" size={14} className="shrink-0 text-primary" />
          <span>Showing the first 8 MB. This file is too large to save back safely, so it opens read only.</span>
        </div>
      )}

      {docReady && readOnly && !truncated && (
        <div className="flex shrink-0 items-center gap-2 border-b border-line bg-elevated px-4 py-2 text-xs text-muted">
          <Icon name="lock" size={14} className="shrink-0 text-warning" />
          <span>This file is read only, so changes cannot be saved back to it.</span>
        </div>
      )}

      <div className="relative min-h-0 flex-1">
        <div ref={hostRef} className="absolute inset-0" />

        {waiting && (
          <div className="absolute inset-0 flex flex-col gap-2 bg-surface p-4">
            <div className="mb-2 flex items-center gap-2 text-xs text-faint">
              <Spinner size={14} className="text-primary" />
              Preparing the editor
            </div>
            {['w-3/4', 'w-1/2', 'w-5/6', 'w-2/3', 'w-1/3', 'w-4/5', 'w-1/2', 'w-3/5'].map((width, index) => (
              <Skeleton key={index} className={clsx('h-3.5', width)} />
            ))}
          </div>
        )}

        {failed && (
          <div className="absolute inset-0 flex flex-col items-center justify-center gap-3 bg-surface p-6 text-center">
            <span className="flex h-12 w-12 items-center justify-center rounded-2xl bg-danger/12 text-danger">
              <Icon name="alert" size={22} />
            </span>
            <div>
              <h3 className="text-sm font-medium text-ink">This file could not be opened</h3>
              <p className="mt-1 text-sm text-muted">
                {loadFailed ? 'The editor failed to load. Check the connection and try again.' : messageOf(text.error)}
              </p>
            </div>
            <div className="flex items-center gap-2">
              <Button
                icon="refresh"
                onClick={() => {
                  setLoadFailed(false)
                  void text.refetch()
                }}
              >
                Try again
              </Button>
              <Button variant="ghost" onClick={onClose}>
                Close
              </Button>
            </div>
          </div>
        )}
      </div>

      <ConfirmDialog
        open={confirmClose}
        title="Close without saving"
        message="This file has changes that have not been saved. Closing now discards them."
        confirmLabel="Discard changes"
        cancelLabel="Keep editing"
        danger
        onCancel={() => setConfirmClose(false)}
        onConfirm={() => {
          setConfirmClose(false)
          setDirty(false)
          onClose()
        }}
      />

      <ConfirmDialog
        open={confirmReload}
        title="Reload from disk"
        message="Reloading replaces what is on screen with the copy stored on the server. Unsaved changes are lost."
        confirmLabel="Reload"
        cancelLabel="Cancel"
        danger
        onCancel={() => setConfirmReload(false)}
        onConfirm={() => void reload()}
      />

      <Modal
        open={saveAsOpen}
        onClose={() => setSaveAsOpen(false)}
        title="Save as"
        description={`A new file is created in ${parentPath(path) || '/'}.`}
        icon="file-plus"
        footer={
          <>
            <Button onClick={() => setSaveAsOpen(false)} disabled={saveAs.isPending}>
              Cancel
            </Button>
            <Button
              variant="primary"
              loading={saveAs.isPending}
              onClick={() => {
                const name = saveAsName.trim()
                if (!name) {
                  setSaveAsError('Enter a name for the new file.')
                  return
                }
                if (name.includes('/')) {
                  setSaveAsError('A name cannot contain a slash.')
                  return
                }
                setSaveAsError('')
                saveAs.mutate(joinPath(parentPath(path), name))
              }}
            >
              Save copy
            </Button>
          </>
        }
      >
        <Field
          label="File name"
          value={saveAsName}
          autoFocus
          error={saveAsError || undefined}
          onChange={(event) => setSaveAsName(event.target.value)}
        />
      </Modal>
    </div>
  )
}
