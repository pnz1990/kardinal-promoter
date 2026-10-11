// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

import { renderHook, act } from '@testing-library/react'
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { EMPTY_URL_STATE, isLegacyHash, parseHash, serializeHash, useUrlState, type UrlState } from './useUrlState'

const state = (s: Partial<UrlState>): UrlState => ({ ...EMPTY_URL_STATE, ...s })

describe('routes', () => {
  it.each<[string, Partial<UrlState>]>([
    ['#/', {}],
    ['#/p/team-a/checkout', { ns: 'team-a', pipeline: 'checkout' }],
    ['#/p/team-a/checkout/env/prod', { ns: 'team-a', pipeline: 'checkout', env: 'prod' }],
    ['#/p/team-a/checkout/v/checkout-1-4-2', { ns: 'team-a', pipeline: 'checkout', version: 'checkout-1-4-2' }],
    ['#/p/team-a/checkout/env/prod?v=checkout-1-4-2', { ns: 'team-a', pipeline: 'checkout', env: 'prod', version: 'checkout-1-4-2' }],
    ['#/p/team-a/checkout?compare=checkout-1-4-1', { ns: 'team-a', pipeline: 'checkout', compare: 'checkout-1-4-1' }],
    ['#/p/team-a/checkout?node=gate-1', { ns: 'team-a', pipeline: 'checkout', node: 'gate-1' }],
    ['#/?view=ops&filter=blocked&q=pay', { view: 'ops', filter: 'blocked', q: 'pay' }],
    ['#/p/*/checkout', { pipeline: 'checkout' }],
    ['#/approvals', { approvals: true }],
  ])('%s round-trips', (hash, want) => {
    expect(parseHash(hash)).toEqual(state(want))
    expect(serializeHash(state(want))).toBe(hash)
  })

  it('encodes names that need it', () => {
    const s = state({ ns: 'a b', pipeline: 'x/y', env: 'eu west' })
    expect(serializeHash(s)).toBe('#/p/a%20b/x%2Fy/env/eu%20west')
    expect(parseHash(serializeHash(s))).toEqual(s)
  })

  it('ignores an unknown view and empty values', () => {
    expect(parseHash('#/?view=table&q=')).toEqual(state({}))
  })

  it.each<[string, Partial<UrlState>]>([
    ['#pipeline=app', { pipeline: 'app' }],
    ['#pipeline=app&ns=team-b', { pipeline: 'app', ns: 'team-b' }],
    ['#pipeline=app&ns=default&node=app-v1-prod', { pipeline: 'app', ns: 'default', node: 'app-v1-prod' }],
    ['#pipeline=app&bundle=app-v0', { pipeline: 'app', compare: 'app-v0' }],
  ])('reads the old link %s', (hash, want) => {
    expect(isLegacyHash(hash)).toBe(true)
    expect(parseHash(hash)).toEqual(state(want))
  })

  it('tells old links from the routes', () => {
    expect(isLegacyHash('#/p/a/b')).toBe(false)
    expect(isLegacyHash('')).toBe(false)
    expect(isLegacyHash('#main-content')).toBe(false)
  })
})

describe('useUrlState', () => {
  beforeEach(() => {
    window.history.replaceState(null, '', window.location.pathname)
  })
  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('starts empty with no hash, and writes nothing until something is set', () => {
    const { result } = renderHook(() => useUrlState())
    expect(result.current[0]).toEqual(EMPTY_URL_STATE)
    expect(window.location.hash).toBe('')
  })

  it('reads the route it starts on', () => {
    window.history.replaceState(null, '', '#/p/default/nginx-demo/env/prod')
    const { result } = renderHook(() => useUrlState())
    expect(result.current[0]).toEqual(state({ ns: 'default', pipeline: 'nginx-demo', env: 'prod' }))
  })

  it('rewrites an old link to its route without a history entry', () => {
    window.history.replaceState(null, '', '#pipeline=app&ns=team-b&bundle=app-v0')
    const push = vi.spyOn(window.history, 'pushState')
    const { result } = renderHook(() => useUrlState())
    expect(result.current[0]).toEqual(state({ pipeline: 'app', ns: 'team-b', compare: 'app-v0' }))
    expect(window.location.hash).toBe('#/p/team-b/app?compare=app-v0')
    expect(push).not.toHaveBeenCalled()
  })

  it('merges fields and pushes one history entry per change', () => {
    const push = vi.spyOn(window.history, 'pushState')
    const { result } = renderHook(() => useUrlState())
    act(() => result.current[1]({ pipeline: 'my-app', ns: 'default' }))
    act(() => result.current[1]({ env: 'prod' }))
    expect(window.location.hash).toBe('#/p/default/my-app/env/prod')
    expect(push).toHaveBeenCalledTimes(2)
    act(() => result.current[1]({ env: undefined, compare: 'b-1' }))
    expect(window.location.hash).toBe('#/p/default/my-app?compare=b-1')
  })

  it('merges consecutive calls in one event into one entry each', () => {
    const push = vi.spyOn(window.history, 'pushState')
    const { result } = renderHook(() => useUrlState())
    act(() => {
      result.current[1]({ pipeline: 'my-app', ns: 'default' })
      result.current[1]({ version: 'b-2' })
    })
    expect(result.current[0]).toEqual(state({ pipeline: 'my-app', ns: 'default', version: 'b-2' }))
    expect(window.location.hash).toBe('#/p/default/my-app/v/b-2')
    expect(push).toHaveBeenCalledTimes(2)
  })

  it('replaces the entry when asked', () => {
    const push = vi.spyOn(window.history, 'pushState')
    const { result } = renderHook(() => useUrlState())
    act(() => result.current[1]({ q: 'pay' }, { replace: true }))
    expect(window.location.hash).toBe('#/?q=pay')
    expect(push).not.toHaveBeenCalled()
  })

  it('does not push when the hash would not change', () => {
    window.history.replaceState(null, '', '#/p/default/my-app')
    const push = vi.spyOn(window.history, 'pushState')
    const { result } = renderHook(() => useUrlState())
    act(() => result.current[1]({ pipeline: 'my-app' }))
    expect(push).not.toHaveBeenCalled()
  })

  it('follows Back and Forward (popstate) and a hash typed by hand (hashchange)', () => {
    const { result } = renderHook(() => useUrlState())
    act(() => {
      window.history.replaceState(null, '', '#/p/default/other')
      window.dispatchEvent(new PopStateEvent('popstate'))
    })
    expect(result.current[0].pipeline).toBe('other')
    act(() => {
      window.history.replaceState(null, '', '#pipeline=legacy')
      window.dispatchEvent(new HashChangeEvent('hashchange'))
    })
    expect(result.current[0].pipeline).toBe('legacy')
    expect(window.location.hash).toBe('#/p/*/legacy')
  })
})
