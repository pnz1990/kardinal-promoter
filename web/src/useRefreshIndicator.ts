// useRefreshIndicator.ts — Tracks last successful poll time and returns elapsed seconds.
// Designed to pair with usePolling: call onSuccess() when a poll succeeds, and
// the hook returns elapsed seconds since the last success for display.
//
// Usage:
//   const { elapsedSeconds, onSuccess } = useRefreshIndicator()
//   usePolling(async () => { await fetchData(); onSuccess() }, 5000)
//   // Render: <span>Updated {elapsedSeconds}s ago</span>
import { useState, useEffect, useRef, useCallback } from 'react'

interface RefreshIndicatorResult {
  /** Seconds since last successful poll. null when no poll has succeeded yet. */
  elapsedSeconds: number | null
  /** Call this function when a poll succeeds to reset the counter. */
  onSuccess: () => void
}

/**
 * useRefreshIndicator tracks elapsed time since the last successful poll.
 *
 * Returns `elapsedSeconds` (null until first success) and an `onSuccess` callback.
 * The counter increments every second via a setInterval and resets to 0 on onSuccess().
 */
export function useRefreshIndicator(): RefreshIndicatorResult {
  const [elapsedSeconds, setElapsedSeconds] = useState<number | null>(null)
  // lastSuccessRef is the single source of truth; onSuccess sets it and resets elapsed.
  // A separate state for lastSuccess is not needed — the ref suffices for the tick closure.
  const lastSuccessRef = useRef<Date | null>(null)

  const onSuccess = useCallback(() => {
    lastSuccessRef.current = new Date()
    setElapsedSeconds(0)
  }, [])

  // Tick every second to update elapsed display.
  useEffect(() => {
    const id = setInterval(() => {
      if (lastSuccessRef.current !== null) {
        const seconds = Math.floor((Date.now() - lastSuccessRef.current.getTime()) / 1000)
        setElapsedSeconds(seconds)
      }
    }, 1000)
    return () => clearInterval(id)
  }, [])

  return { elapsedSeconds, onSuccess }
}
