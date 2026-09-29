// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// client.timeout.test.ts — a read the API never answers fails after
// REQUEST_TIMEOUT_MS, so the poll that waits for it can run again.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { api, REQUEST_TIMEOUT_MS, setTokenPrompt, TOKEN_STORAGE_KEY } from './client'

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

let unregister: (() => void) | undefined

beforeEach(() => {
  vi.useFakeTimers()
  sessionStorage.clear()
})
afterEach(() => {
  unregister?.()
  unregister = undefined
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

// A server that wants `Bearer good`: answers 401 without it and, with it,
// `[]` (or never, when `hangWithToken`). Honors the request signal.
function authServer(hangWithToken = false) {
  const signals: AbortSignal[] = []
  const fetchMock = vi.fn((_url: string, init?: RequestInit) => new Promise<Response>((resolve, reject) => {
    const signal = init?.signal
    if (signal) signals.push(signal)
    if (signal?.aborted) return reject(new DOMException('aborted', 'AbortError'))
    signal?.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError')))
    const auth = (init?.headers as Record<string, string> | undefined)?.Authorization
    if (auth !== 'Bearer good') return resolve(new Response('unauthorized', { status: 401, statusText: 'Unauthorized' }))
    if (!hangWithToken) resolve(new Response('[]', { status: 200, headers: { 'Content-Type': 'application/json' } }))
  }))
  vi.stubGlobal('fetch', fetchMock)
  return { fetchMock, signals }
}

// A token prompt the user answers after `ms`.
function slowPrompt(ms: number) {
  const prompt = vi.fn(() => new Promise<string>(resolve => setTimeout(() => resolve('good'), ms)))
  unregister = setTokenPrompt(prompt)
  return prompt
}

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

  it('does not count the time the user spends in the sign-in prompt', async () => {
    const { signals } = authServer()
    const prompt = slowPrompt(REQUEST_TIMEOUT_MS * 3)
    const result = api.listPipelines()

    await vi.advanceTimersByTimeAsync(REQUEST_TIMEOUT_MS * 3)
    await expect(result).resolves.toEqual([])
    expect(prompt).toHaveBeenCalledTimes(1)
    expect(signals).toHaveLength(2)
    expect(signals[1].aborted).toBe(false)
    expect(sessionStorage.getItem(TOKEN_STORAGE_KEY)).toBe('good')
  })

  it('gives the retry after sign-in its own full timeout', async () => {
    const { signals } = authServer(true)
    slowPrompt(REQUEST_TIMEOUT_MS / 2)
    const result = api.listPipelines()
    const settled = vi.fn()
    result.then(settled, settled)

    await vi.advanceTimersByTimeAsync(REQUEST_TIMEOUT_MS / 2) // signed in, retry sent
    expect(signals).toHaveLength(2)
    await vi.advanceTimersByTimeAsync(REQUEST_TIMEOUT_MS - 1)
    expect(settled).not.toHaveBeenCalled()

    await vi.advanceTimersByTimeAsync(1)
    await expect(result).rejects.toThrow('GET /api/v1/ui/pipelines got no answer within 10 s')
  })

  it('does not fail a read that waits for a sign-in another read opened', async () => {
    authServer()
    const prompt = slowPrompt(REQUEST_TIMEOUT_MS * 3)
    const first = api.listPipelines()
    await vi.advanceTimersByTimeAsync(1) // first read got its 401; the prompt is open
    const second = api.listGates()

    await vi.advanceTimersByTimeAsync(REQUEST_TIMEOUT_MS * 3)
    await expect(first).resolves.toEqual([])
    await expect(second).resolves.toEqual([])
    expect(prompt).toHaveBeenCalledTimes(1)
  })
})
