// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// usePolling.ts — Generic polling hook for the kardinal UI.
// Calls `fn` immediately on mount and then every `intervalMs` milliseconds.
// Stops polling when the component unmounts or `enabled` becomes false.
import { useEffect, useRef } from 'react'

/** How long one poll may run before the next one may start. */
export const POLL_TIMEOUT_MS = 10_000

/**
 * usePolling calls `fn` once immediately and then at the given interval.
 * A tick is skipped while the previous call is still running, so a slow API
 * never has several copies of the same request in flight. A call that has not
 * finished after `timeoutMs` is given up on: its signal is aborted and the next
 * tick runs, so one hung request cannot stop polling.
 *
 * @param fn          Async function to call on each tick. It gets an AbortSignal
 *                    that aborts at the timeout or on unmount.
 * @param intervalMs  Polling interval in milliseconds (default 5000).
 * @param enabled     When false, polling is suspended (default true).
 * @param timeoutMs   How long one call may run before the next tick may start
 *                    (default POLL_TIMEOUT_MS).
 */
export function usePolling(
  fn: (signal: AbortSignal) => Promise<void> | void,
  intervalMs = 5000,
  enabled = true,
  timeoutMs = POLL_TIMEOUT_MS,
): void {
  // Keep a stable ref to fn so that stale closures don't prevent updates.
  const fnRef = useRef(fn)
  fnRef.current = fn

  useEffect(() => {
    if (!enabled) return

    let cancelled = false
    // The controller of the call now running; null when none is.
    let inFlight: AbortController | null = null

    const tick = async () => {
      if (cancelled || inFlight) return
      const ctrl = new AbortController()
      inFlight = ctrl
      const release = () => { if (inFlight === ctrl) inFlight = null }
      const timer = setTimeout(() => { ctrl.abort(); release() }, timeoutMs)
      try {
        await fnRef.current(ctrl.signal)
      } finally {
        clearTimeout(timer)
        release()
      }
    }

    void tick()
    const id = setInterval(() => void tick(), intervalMs)

    return () => {
      cancelled = true
      clearInterval(id)
      inFlight?.abort()
    }
  }, [intervalMs, enabled, timeoutMs])
}
