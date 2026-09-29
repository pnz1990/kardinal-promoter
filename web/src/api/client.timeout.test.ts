// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// client.timeout.test.ts — a read the API never answers fails after
// REQUEST_TIMEOUT_MS, so the poll that waits for it can run again.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { api, REQUEST_TIMEOUT_MS } from './client'

// A fetch that answers only when told to, and rejects when its signal aborts.
function hangingFetch() {
  const signals: AbortSignal[] = []
  const fetchMock = vi.fn((_url: string, init?: RequestInit) => new Promise<Response>((_resolve, reject) => {
    const signal = init?.signal
    if (signal) {
      signals.push(signal)
      signal.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError')))
    }
  }))
  return { fetchMock, signals }
}

beforeEach(() => { vi.useFakeTimers() })
afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

describe('api client read timeout', () => {
  it.each([
    { name: 'listPipelines', call: () => api.listPipelines(), path: '/api/v1/ui/pipelines' },
    { name: 'listBundles', call: () => api.listBundles('app', 'default'), path: '/api/v1/ui/pipelines/app/bundles?namespace=default' },
    { name: 'getGraph', call: () => api.getGraph('b1'), path: '/api/v1/ui/bundles/b1/graph' },
    { name: 'getSteps', call: () => api.getSteps('b1'), path: '/api/v1/ui/bundles/b1/steps' },
    { name: 'listGates', call: () => api.listGates(), path: '/api/v1/ui/gates' },
  ])('$name fails with a clear message when the API never answers', async ({ call, path }) => {
    const { fetchMock, signals } = hangingFetch()
    vi.stubGlobal('fetch', fetchMock)
    const result = call()
    const settled = vi.fn()
    result.then(settled, settled)

    await vi.advanceTimersByTimeAsync(REQUEST_TIMEOUT_MS - 1)
    expect(settled).not.toHaveBeenCalled()
    expect(signals[0].aborted).toBe(false)

    await vi.advanceTimersByTimeAsync(1)
    expect(signals[0].aborted).toBe(true)
    await expect(result).rejects.toThrow(`GET ${path} got no answer within 10 s`)
  })

  it('does not abort a read that answers in time', async () => {
    let signal: AbortSignal | undefined
    vi.stubGlobal('fetch', vi.fn(async (_url: string, init?: RequestInit) => {
      signal = init?.signal ?? undefined
      return new Response('[]', { status: 200, headers: { 'Content-Type': 'application/json' } })
    }))
    await expect(api.listPipelines()).resolves.toEqual([])
    await vi.advanceTimersByTimeAsync(REQUEST_TIMEOUT_MS * 2)
    expect(signal?.aborted).toBe(false)
  })

  it('keeps the HTTP error for a read that fails in time', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response('nope', { status: 503, statusText: 'Service Unavailable' })))
    await expect(api.listPipelines()).rejects.toThrow('API error 503: Service Unavailable')
  })
})
