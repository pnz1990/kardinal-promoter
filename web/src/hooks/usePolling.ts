// hooks/usePolling.ts — Generic polling hook for periodic data refresh.
// Calls `fn` immediately on mount, then every `intervalMs` milliseconds.
// Stops polling when the component unmounts or when `active` is false.
// Per spec 006: the UI uses polling every 5 seconds on the active page.
import { useEffect, useRef, useCallback } from 'react'

interface UsePollingOptions {
  /** Polling interval in milliseconds (default: 5000). */
  intervalMs?: number
  /** Only poll when true (default: true). Pauses polling while false. */
  active?: boolean
}

/**
 * usePolling repeatedly calls `fn` at the given interval.
 * The first call happens immediately (synchronous scheduling via setTimeout 0).
 *
 * @example
 * usePolling(() => {
 *   api.listPipelines().then(setPipelines).catch(console.error)
 * }, { intervalMs: 5000, active: !!selectedPipeline })
 */
export function usePolling(fn: () => void, options: UsePollingOptions = {}): void {
  const { intervalMs = 5000, active = true } = options
  const fnRef = useRef(fn)

  // Keep fnRef current without restarting the interval on every fn change.
  useEffect(() => {
    fnRef.current = fn
  }, [fn])

  const tick = useCallback(() => {
    fnRef.current()
  }, [])

  useEffect(() => {
    if (!active) return

    // Fire immediately, then on interval.
    tick()
    const id = setInterval(tick, intervalMs)
    return () => clearInterval(id)
  }, [active, intervalMs, tick])
}
