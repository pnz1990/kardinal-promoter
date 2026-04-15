// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// useRefreshIndicator.test.ts — Tests for the refresh indicator hook (#522).
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { renderHook, act } from '@testing-library/react'
import { useRefreshIndicator } from './useRefreshIndicator'

describe('useRefreshIndicator (#522)', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  it('starts with elapsedSeconds=null (Loading... state)', () => {
    const { result } = renderHook(() => useRefreshIndicator())
    expect(result.current.elapsedSeconds).toBeNull()
  })

  it('sets elapsedSeconds to 0 immediately after onSuccess()', () => {
    const { result } = renderHook(() => useRefreshIndicator())
    expect(result.current.elapsedSeconds).toBeNull()

    act(() => {
      result.current.onSuccess()
    })

    expect(result.current.elapsedSeconds).toBe(0)
  })

  it('increments elapsedSeconds each second after onSuccess()', () => {
    const { result } = renderHook(() => useRefreshIndicator())

    act(() => {
      result.current.onSuccess()
    })
    expect(result.current.elapsedSeconds).toBe(0)

    act(() => {
      vi.advanceTimersByTime(1000)
    })
    expect(result.current.elapsedSeconds).toBe(1)

    act(() => {
      vi.advanceTimersByTime(4000)
    })
    expect(result.current.elapsedSeconds).toBe(5)
  })

  it('resets elapsedSeconds to 0 on second onSuccess() call', () => {
    const { result } = renderHook(() => useRefreshIndicator())

    act(() => {
      result.current.onSuccess()
    })
    act(() => {
      vi.advanceTimersByTime(10000)
    })
    expect(result.current.elapsedSeconds).toBe(10)

    // Simulate a new successful poll
    act(() => {
      result.current.onSuccess()
    })
    expect(result.current.elapsedSeconds).toBe(0)
  })

  it('does not increment before first onSuccess()', () => {
    const { result } = renderHook(() => useRefreshIndicator())

    act(() => {
      vi.advanceTimersByTime(5000)
    })
    // Should still be null — no success yet
    expect(result.current.elapsedSeconds).toBeNull()
  })

  it('onSuccess is stable across re-renders (referential equality)', () => {
    const { result, rerender } = renderHook(() => useRefreshIndicator())
    const firstOnSuccess = result.current.onSuccess
    rerender()
    expect(result.current.onSuccess).toBe(firstOnSuccess)
  })
})
