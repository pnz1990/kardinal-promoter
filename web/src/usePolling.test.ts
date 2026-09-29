// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

import { renderHook, act } from '@testing-library/react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { usePolling } from './usePolling'

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
      await new Promise(r => setTimeout(r, 12_000)) // slower than the 5 s interval
      running--
    })
    renderHook(() => usePolling(fn, 5000))
    await act(async () => { await vi.advanceTimersByTimeAsync(30_000) })
    expect(maxRunning).toBe(1)
    // 0 s start, finishes at 12 s; 15 s start, finishes at 27 s; 30 s start.
    expect(fn).toHaveBeenCalledTimes(3)
  })

  it('does not poll when disabled', async () => {
    const fn = vi.fn(async () => {})
    renderHook(() => usePolling(fn, 1000, false))
    await act(async () => { await vi.advanceTimersByTimeAsync(3000) })
    expect(fn).not.toHaveBeenCalled()
  })
})
