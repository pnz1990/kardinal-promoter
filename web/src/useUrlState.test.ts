// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

import { renderHook, act } from '@testing-library/react'
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { useUrlState } from './useUrlState'

describe('useUrlState', () => {
  beforeEach(() => {
    // Reset hash before each test
    window.history.replaceState(null, '', window.location.pathname)
  })

  // A failed assertion must not leave a pushState spy behind for the next test.
  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('initializes with empty state when hash is empty', () => {
    const { result } = renderHook(() => useUrlState())
    expect(result.current[0].pipeline).toBeUndefined()
    expect(result.current[0].node).toBeUndefined()
    expect(result.current[0].bundle).toBeUndefined()
  })

  it('initializes from existing hash', () => {
    window.history.replaceState(null, '', '#pipeline=nginx-demo')
    const { result } = renderHook(() => useUrlState())
    expect(result.current[0].pipeline).toBe('nginx-demo')
    expect(result.current[0].node).toBeUndefined()
  })

  it('updates the hash when pipeline is set', () => {
    const { result } = renderHook(() => useUrlState())
    act(() => result.current[1]({ pipeline: 'my-app' }))
    expect(result.current[0].pipeline).toBe('my-app')
    expect(window.location.hash).toBe('#pipeline=my-app')
  })

  it('updates the hash when node is set', () => {
    const { result } = renderHook(() => useUrlState())
    act(() => result.current[1]({ pipeline: 'my-app' }))
    act(() => result.current[1]({ node: 'prod-step' }))
    expect(result.current[0].pipeline).toBe('my-app')
    expect(result.current[0].node).toBe('prod-step')
    expect(window.location.hash).toContain('pipeline=my-app')
    expect(window.location.hash).toContain('node=prod-step')
  })

  it('updates the hash when bundle is set for diff comparison', () => {
    const { result } = renderHook(() => useUrlState())
    act(() => result.current[1]({ pipeline: 'my-app', bundle: 'bundle-abc' }))
    expect(result.current[0].pipeline).toBe('my-app')
    expect(result.current[0].bundle).toBe('bundle-abc')
    expect(window.location.hash).toContain('bundle=bundle-abc')
  })

  it('clears bundle without clearing pipeline', () => {
    const { result } = renderHook(() => useUrlState())
    act(() => result.current[1]({ pipeline: 'my-app', bundle: 'bundle-abc' }))
    act(() => result.current[1]({ bundle: undefined }))
    expect(result.current[0].pipeline).toBe('my-app')
    expect(result.current[0].bundle).toBeUndefined()
    expect(window.location.hash).toBe('#pipeline=my-app')
  })

  it('initializes bundle from existing hash', () => {
    window.history.replaceState(null, '', '#pipeline=my-app&bundle=bundle-xyz')
    const { result } = renderHook(() => useUrlState())
    expect(result.current[0].pipeline).toBe('my-app')
    expect(result.current[0].bundle).toBe('bundle-xyz')
  })

  it('clears node independently without clearing pipeline', () => {
    const { result } = renderHook(() => useUrlState())
    act(() => result.current[1]({ pipeline: 'my-app', node: 'prod-step' }))
    act(() => result.current[1]({ node: undefined }))
    expect(result.current[0].pipeline).toBe('my-app')
    expect(result.current[0].node).toBeUndefined()
    expect(window.location.hash).toBe('#pipeline=my-app')
  })

  it('responds to popstate events (back/forward navigation)', () => {
    const { result } = renderHook(() => useUrlState())
    act(() => result.current[1]({ pipeline: 'first' }))
    act(() => result.current[1]({ pipeline: 'second' }))

    // Simulate back navigation by changing hash and firing popstate
    act(() => {
      window.history.replaceState(null, '', '#pipeline=first')
      window.dispatchEvent(new PopStateEvent('popstate'))
    })
    expect(result.current[0].pipeline).toBe('first')
  })

  it('produces empty hash when state is cleared', () => {
    const { result } = renderHook(() => useUrlState())
    act(() => result.current[1]({ pipeline: 'my-app' }))
    act(() => result.current[1]({ pipeline: undefined }))
    expect(result.current[0].pipeline).toBeUndefined()
    // Hash should be empty or just the pathname
    expect(window.location.hash).toBe('')
  })
  it('pushes one history entry when several fields change together', () => {
    window.history.replaceState(null, '', '#pipeline=a&node=n1')
    const { result } = renderHook(() => useUrlState())
    const push = vi.spyOn(window.history, 'pushState')
    act(() => result.current[1]({ pipeline: 'b', ns: 'team-b', node: undefined }))
    expect(push).toHaveBeenCalledTimes(1)
    expect(window.location.hash).toBe('#pipeline=b&ns=team-b')
  })

  it('merges consecutive calls made in the same event', () => {
    const { result } = renderHook(() => useUrlState())
    act(() => {
      result.current[1]({ pipeline: 'my-app' })
      result.current[1]({ node: 'prod-step' })
    })
    expect(result.current[0]).toMatchObject({ pipeline: 'my-app', node: 'prod-step' })
    expect(window.location.hash).toBe('#pipeline=my-app&node=prod-step')
  })

  it('does not push when nothing changes', () => {
    window.history.replaceState(null, '', '#pipeline=my-app')
    const { result } = renderHook(() => useUrlState())
    const push = vi.spyOn(window.history, 'pushState')
    act(() => result.current[1]({ node: undefined }))
    expect(push).not.toHaveBeenCalled()
  })

  it('reads the namespace from the hash', () => {
    window.history.replaceState(null, '', '#pipeline=app&ns=team-b')
    const { result } = renderHook(() => useUrlState())
    expect(result.current[0].ns).toBe('team-b')
  })
})
