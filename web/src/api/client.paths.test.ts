// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// client.paths.test.ts — names from the URL hash cannot change which API path
// the UI calls (audit C10b-web-18). The pipeline name comes from a deep link,
// so "../gates" or "a?b=c" must stay one encoded path segment.

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { api } from './client'

let fetchMock: ReturnType<typeof vi.fn>

beforeEach(() => {
  fetchMock = vi.fn(async () => new Response('[]', { status: 200, headers: { 'Content-Type': 'application/json' } }))
  vi.stubGlobal('fetch', fetchMock)
})
afterEach(() => {
  vi.unstubAllGlobals()
})

const calledPath = () => String(fetchMock.mock.calls[0][0])

describe('api client path segments', () => {
  it.each([
    { name: 'listBundles with a parent-directory name', call: () => api.listBundles('../gates'), want: '/api/v1/ui/pipelines/..%2Fgates/bundles' },
    { name: 'listBundles with a query in the name', call: () => api.listBundles('a?b=c'), want: '/api/v1/ui/pipelines/a%3Fb%3Dc/bundles' },
    { name: 'listBundles with an encoded namespace', call: () => api.listBundles('app', 'team a&x=1'), want: '/api/v1/ui/pipelines/app/bundles?namespace=team%20a%26x%3D1' },
    { name: 'getGraph', call: () => api.getGraph('../gates'), want: '/api/v1/ui/bundles/..%2Fgates/graph' },
    { name: 'getSteps', call: () => api.getSteps('b#frag'), want: '/api/v1/ui/bundles/b%23frag/steps' },
    { name: 'getStepEvents', call: () => api.getStepEvents('ns/x', 'step?y'), want: '/api/v1/ui/steps/ns%2Fx/step%3Fy/events' },
    { name: 'a real Kubernetes name is unchanged', call: () => api.listBundles('kardinal-test-app', 'default'), want: '/api/v1/ui/pipelines/kardinal-test-app/bundles?namespace=default' },
  ])('$name', async ({ call, want }) => {
    await call()
    expect(fetchMock).toHaveBeenCalledOnce()
    expect(calledPath()).toBe(want)
  })
})
