// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

import { renderHook, act } from '@testing-library/react'
import { describe, it, expect, beforeEach } from 'vitest'
import { useUrlState } from './useUrlState'

describe('useUrlState', () => {
  beforeEach(() => {
    // Reset hash before each test
    window.history.replaceState(null, '', window.location.pathname)
  })

  it('initializes with empty state when hash is empty', () => {
    const { result } = renderHook(() => useUrlState())
    expect(result.current[0].pipeline).toBeUndefined()
    expect(result.current[0].node).toBeUndefined()
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
})
