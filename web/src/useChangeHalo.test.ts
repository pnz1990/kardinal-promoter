// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

import { describe, it, expect, vi, afterEach } from 'vitest'
import { act, renderHook } from '@testing-library/react'
import { CHANGE_MARK_MS, useChangeHalo } from './useChangeHalo'

describe('useChangeHalo (U7)', () => {
  afterEach(() => { vi.useRealTimers() })

  it('marks a changed value for one poll; the first value and a change of scope are not changes', () => {
    vi.useFakeTimers()
    const { result, rerender } = renderHook(({ v, scope }) => useChangeHalo(v, scope), { initialProps: { v: '', scope: 'prod' } })
    rerender({ v: 'Live', scope: 'prod' })
    expect(result.current).toBe(false)
    rerender({ v: 'Failed', scope: 'prod' })
    expect(result.current).toBe(true)
    act(() => { vi.advanceTimersByTime(CHANGE_MARK_MS) })
    expect(result.current).toBe(false)
    rerender({ v: 'Live', scope: 'uat' })
    expect(result.current).toBe(false)
  })
})
