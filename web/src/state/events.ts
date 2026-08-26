// One event stream for the whole interface.
//
// Several parts of the app care about server events: the layout invalidates
// queries, the notification list collects them, the transfer panel follows
// uploads. Each of them opening its own EventSource would cost a goroutine and
// a subscriber on the server per tab, and browsers only allow six connections
// per host over HTTP/1.1, so a handful of panels would starve the rest of the
// page. This keeps a single connection open and fans it out.
//
// Developed by X Project.

import { useEffect, useRef } from 'react'
import { subscribe } from '../lib/api'

export type EventHandler = (type: string, data: unknown) => void

const handlers = new Set<EventHandler>()
let close: (() => void) | null = null

function ensureOpen(): void {
  if (close) return
  close = subscribe(
    (type, data) => {
      // A handler that throws must not take the others down with it.
      for (const handler of Array.from(handlers)) {
        try {
          handler(type, data)
        } catch {
          // Ignore a failing listener, the stream keeps running.
        }
      }
    },
    () => {
      // The browser reconnects an EventSource on its own, so an error here is
      // informational. Nothing to do but let it retry.
    },
  )
}

function closeIfIdle(): void {
  if (handlers.size > 0 || !close) return
  close()
  close = null
}

/**
 * useStorixEvents runs a handler for every server event while the component is
 * mounted. The underlying connection is shared and closes when the last
 * listener goes away.
 */
export function useStorixEvents(handler: EventHandler): void {
  const ref = useRef(handler)
  ref.current = handler

  useEffect(() => {
    const listener: EventHandler = (type, data) => ref.current(type, data)
    handlers.add(listener)
    ensureOpen()
    return () => {
      handlers.delete(listener)
      closeIfIdle()
    }
  }, [])
}

/** eventListenerCount is exposed for diagnostics and tests. */
export function eventListenerCount(): number {
  return handlers.size
}
