// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

import { renderHook, act } from '@testing-library/react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { usePolling, POLL_TIMEOUT_MS } from './usePolling'

describe('usePolling', () => {
  beforeEach(() => { vi.useFakeTimers() })
  afterEach(() => { vi.useRealTimers() })

  it('calls fn immediately and then on every interval', async () => {
    const fn = vi.fn(async () => {})
    renderHook(() => usePolling(fn, 1000))
    await act(async () => { await vi.advanceTimersByTimeAsync(0) })
    expect(fn).toHaveBeenCalledTimes(1)
    await act(async () => { await vi.advanceTimersByTimeAsync(3000) })
    expect(fn).toHaveBeenCalledTimes(4)
  })

  it('skips ticks while the previous call is still running', async () => {
    let running = 0
    let maxRunning = 0
    const fn = vi.fn(async () => {
      running++
      maxRunning = Math.max(maxRunning, running)
      await new Promise(r => setTimeout(r, 7_000)) // slower than the 5 s interval, under the timeout
      running--
    })
    renderHook(() => usePolling(fn, 5000))
    await act(async () => { await vi.advanceTimersByTimeAsync(30_000) })
    expect(maxRunning).toBe(1)
    // Starts at 0, 10, 20 and 30 s; the ticks at 5, 15 and 25 s find a call running.
    expect(fn).toHaveBeenCalledTimes(4)
  })

  // Review of #1245: a request that never answers must not stop polling.
  it('gives up on a call that never finishes and polls again after the timeout', async () => {
    const signals: AbortSignal[] = []
    const fn = vi.fn((signal: AbortSignal) => {
      signals.push(signal)
      return new Promise<void>(() => {}) // never settles
    })
    renderHook(() => usePolling(fn, 5000))
    await act(async () => { await vi.advanceTimersByTimeAsync(9_999) })
    expect(fn).toHaveBeenCalledTimes(1) // the 5 s tick waited
    expect(signals[0].aborted).toBe(false)
    await act(async () => { await vi.advanceTimersByTimeAsync(1) })
    expect(signals[0].aborted).toBe(true)
    expect(fn).toHaveBeenCalledTimes(2) // the 10 s tick ran
    // The second call hangs too: it is given up on at 20 s and the third
    // starts on the 20 s or the 25 s tick.
    await act(async () => { await vi.advanceTimersByTimeAsync(15_000) })
    expect(fn).toHaveBeenCalledTimes(3)
  })

  it.each([
    { name: 'default timeout', timeoutMs: undefined, wantAbortAt: POLL_TIMEOUT_MS },
    { name: 'custom timeout', timeoutMs: 3000, wantAbortAt: 3000 },
  ])('aborts the running call at the timeout ($name)', async ({ timeoutMs, wantAbortAt }) => {
    let signal: AbortSignal | undefined
    renderHook(() => usePolling(s => { signal = s; return new Promise<void>(() => {}) }, 60_000, true, timeoutMs))
    await act(async () => { await vi.advanceTimersByTimeAsync(wantAbortAt - 1) })
    expect(signal?.aborted).toBe(false)
    await act(async () => { await vi.advanceTimersByTimeAsync(1) })
    expect(signal?.aborted).toBe(true)
  })

  it('aborts the running call on unmount', async () => {
    let signal: AbortSignal | undefined
    const { unmount } = renderHook(() => usePolling(s => { signal = s; return new Promise<void>(() => {}) }, 5000))
    await act(async () => { await vi.advanceTimersByTimeAsync(0) })
    unmount()
    expect(signal?.aborted).toBe(true)
  })

  it('does not poll when disabled', async () => {
    const fn = vi.fn(async () => {})
    renderHook(() => usePolling(fn, 1000, false))
    await act(async () => { await vi.advanceTimersByTimeAsync(3000) })
    expect(fn).not.toHaveBeenCalled()
  })
})
