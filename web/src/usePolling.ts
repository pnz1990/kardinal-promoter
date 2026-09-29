// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// usePolling.ts — Generic polling hook for the kardinal UI.
// Calls `fn` immediately on mount and then every `intervalMs` milliseconds.
// Stops polling when the component unmounts or `enabled` becomes false.
import { useEffect, useRef } from 'react'

/**
 * usePolling calls `fn` once immediately and then at the given interval.
 * A tick is skipped while the previous call is still running, so a slow API
 * never has several copies of the same request in flight.
 *
 * @param fn          Async function to call on each tick.
 * @param intervalMs  Polling interval in milliseconds (default 5000).
 * @param enabled     When false, polling is suspended (default true).
 */
export function usePolling(
  fn: () => Promise<void> | void,
  intervalMs = 5000,
  enabled = true,
): void {
  // Keep a stable ref to fn so that stale closures don't prevent updates.
  const fnRef = useRef(fn)
  fnRef.current = fn

  useEffect(() => {
    if (!enabled) return

    let cancelled = false
    let inFlight = false

    const tick = async () => {
      if (cancelled || inFlight) return
      inFlight = true
      try {
        await fnRef.current()
      } finally {
        inFlight = false
      }
    }

    void tick()
    const id = setInterval(() => void tick(), intervalMs)

    return () => {
      cancelled = true
      clearInterval(id)
    }
  }, [intervalMs, enabled])
}
