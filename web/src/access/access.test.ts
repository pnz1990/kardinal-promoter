// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

import { describe, it, expect, vi, beforeEach } from 'vitest'
import { act, renderHook, waitFor } from '@testing-library/react'

const api = vi.hoisted(() => ({ checkAccess: vi.fn() }))
vi.mock('../api/client', () => ({ api }))

import { ACCESS_TTL_MS, resetAccessForTests, useAccess, useAccessIn } from './access'
import type { AccessCheck } from '../types'

const pause: AccessCheck = { verb: 'update', resource: 'pipelines', subresource: 'pause', name: 'web' }
const ship: AccessCheck = { verb: 'create', resource: 'bundles' }

describe('useAccess (U7)', () => {
  let clock = 0
  beforeEach(() => {
    clock = 1_000_000
    resetAccessForTests(() => clock)
    api.checkAccess.mockReset()
    api.checkAccess.mockImplementation(async (_ns: string, checks: AccessCheck[]) => ({
      user: { username: 'alice', groups: ['release-managers'] },
      results: checks.map(c => ({ allowed: c.resource === 'bundles', grantedBy: c.resource === 'bundles' ? 'promoter or admin' : 'promoter or admin' })),
    }))
  })

  it('allows until the answer arrives, then reports it with the role that grants it and who the caller is', async () => {
    const { result } = renderHook(() => useAccess('team', [pause, ship]))
    expect(result.current.can(pause)).toEqual({ allowed: true, known: false })
    await waitFor(() => expect(result.current.can(pause).known).toBe(true))
    expect(result.current.can(pause)).toEqual({ allowed: false, known: true, grantedBy: 'promoter or admin' })
    expect(result.current.can(ship).allowed).toBe(true)
    expect(result.current.identity).toEqual({ username: 'alice', groups: ['release-managers'] })
  })

  it('asks once per namespace for the checks of one render, and reuses answers for 30 s', async () => {
    const a = renderHook(() => useAccess('team', [pause]))
    renderHook(() => useAccess('team', [ship, pause]))
    renderHook(() => useAccessIn([['other', ship]]))
    await waitFor(() => expect(a.result.current.can(pause).known).toBe(true))
    expect(api.checkAccess).toHaveBeenCalledTimes(2)
    expect(api.checkAccess).toHaveBeenCalledWith('team', [pause, ship])
    expect(api.checkAccess).toHaveBeenCalledWith('other', [ship])

    renderHook(() => useAccess('team', [pause]))
    await act(async () => {})
    expect(api.checkAccess).toHaveBeenCalledTimes(2)
    clock += ACCESS_TTL_MS + 1
    renderHook(() => useAccess('team', [pause]))
    await waitFor(() => expect(api.checkAccess).toHaveBeenCalledTimes(3))
  })

  it('a failed check (an older controller, a review outage) offers the action: the controller decides', async () => {
    api.checkAccess.mockRejectedValue(new Error('API error 404: no mock'))
    const { result } = renderHook(() => useAccess('team', [pause]))
    await waitFor(() => expect(result.current.can(pause).known).toBe(true))
    expect(result.current.can(pause).allowed).toBe(true)
    expect(result.current.identity).toBeUndefined()
  })

  it('a UI API without TokenReview names nobody (identity null)', async () => {
    api.checkAccess.mockResolvedValue({ results: [{ allowed: true }] })
    const { result } = renderHook(() => useAccess('team', [pause]))
    await waitFor(() => expect(result.current.identity).toBeNull())
  })
})
