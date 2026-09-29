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

// Regression tests for C07-controller-06 / C10a-web-03 / C10b-web-03: the
// client never sent Authorization, so enabling UI auth broke every call.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { api, setTokenPrompt, TOKEN_STORAGE_KEY, type TokenPromptReason } from './client'

/** A fake controller: accepts only `valid` tokens (all tokens when `valid` is empty). */
function fakeServer(valid: string[], status401 = 401) {
  const calls: Array<{ url: string; auth: string | undefined; contentType: string | undefined }> = []
  const fetchMock = vi.fn(async (url: string, init?: RequestInit) => {
    const headers = (init?.headers ?? {}) as Record<string, string>
    const auth = headers.Authorization
    calls.push({ url, auth, contentType: headers['Content-Type'] })
    const ok = valid.length === 0 || valid.some(t => auth === `Bearer ${t}`)
    if (!ok) return new Response('unauthorized', { status: status401, statusText: 'Unauthorized' })
    return new Response('[]', { status: 200, headers: { 'Content-Type': 'application/json' } })
  })
  vi.stubGlobal('fetch', fetchMock)
  return calls
}

let unregister: (() => void) | undefined

beforeEach(() => {
  sessionStorage.clear()
})

afterEach(() => {
  unregister?.()
  unregister = undefined
  vi.unstubAllGlobals()
})

describe('api client auth', () => {
  it('sends no Authorization header when no token is stored', async () => {
    const calls = fakeServer([])
    await api.listPipelines()
    expect(calls).toEqual([{ url: '/api/v1/ui/pipelines', auth: undefined, contentType: undefined }])
  })

  it('sends the stored token on GET and POST', async () => {
    sessionStorage.setItem(TOKEN_STORAGE_KEY, 'abc')
    const calls = fakeServer(['abc'])
    await api.listPipelines()
    await api.pause('app', 'team-a')
    expect(calls.map(c => c.auth)).toEqual(['Bearer abc', 'Bearer abc'])
    expect(calls[1].contentType).toBe('application/json')
  })

  it('asks for a token on 401, stores it and retries', async () => {
    const calls = fakeServer(['good'])
    const prompt = vi.fn(async (_: TokenPromptReason) => 'good')
    unregister = setTokenPrompt(prompt)

    await expect(api.listPipelines()).resolves.toEqual([])
    expect(prompt).toHaveBeenCalledTimes(1)
    expect(prompt).toHaveBeenCalledWith('required')
    expect(calls.map(c => c.auth)).toEqual([undefined, 'Bearer good'])
    expect(sessionStorage.getItem(TOKEN_STORAGE_KEY)).toBe('good')
  })

  it('drops a rejected token and asks again with reason "rejected"', async () => {
    sessionStorage.setItem(TOKEN_STORAGE_KEY, 'expired')
    const calls = fakeServer(['fresh'])
    const prompt = vi.fn(async (_: TokenPromptReason) => 'fresh')
    unregister = setTokenPrompt(prompt)

    await api.listGates()
    expect(prompt).toHaveBeenCalledWith('rejected')
    expect(calls.map(c => c.auth)).toEqual(['Bearer expired', 'Bearer fresh'])
    expect(sessionStorage.getItem(TOKEN_STORAGE_KEY)).toBe('fresh')
  })

  it('keeps asking until the token is accepted', async () => {
    const calls = fakeServer(['right'])
    const answers = ['wrong', 'right']
    const prompt = vi.fn(async (_: TokenPromptReason) => answers.shift() ?? '')
    unregister = setTokenPrompt(prompt)

    await api.listPipelines()
    expect(prompt.mock.calls.map(c => c[0])).toEqual(['required', 'rejected'])
    expect(calls.map(c => c.auth)).toEqual([undefined, 'Bearer wrong', 'Bearer right'])
  })

  it('shares one prompt between concurrent requests', async () => {
    fakeServer(['t'])
    let resolve: (t: string) => void = () => {}
    const prompt = vi.fn((_: TokenPromptReason) => new Promise<string>(r => { resolve = r }))
    unregister = setTokenPrompt(prompt)

    const all = Promise.all([api.listPipelines(), api.listGates(), api.getSteps('b1')])
    await vi.waitFor(() => expect(prompt).toHaveBeenCalled())
    resolve('t')
    await expect(all).resolves.toEqual([[], [], []])
    expect(prompt).toHaveBeenCalledTimes(1)
  })

  it('reports the 401 when no prompt is registered', async () => {
    fakeServer(['t'])
    await expect(api.listPipelines()).rejects.toThrow('API error 401')
  })

  it('reports the 401 when the prompt is abandoned', async () => {
    fakeServer(['t'])
    unregister = setTokenPrompt(() => Promise.reject(new Error('closed')))
    await expect(api.pause('app')).rejects.toThrow('API error 401')
  })

  it('does not prompt or drop the token on 403', async () => {
    sessionStorage.setItem(TOKEN_STORAGE_KEY, 'viewer')
    fakeServer(['nobody'], 403)
    const prompt = vi.fn(async (_: TokenPromptReason) => 'x')
    unregister = setTokenPrompt(prompt)

    await expect(api.pause('app')).rejects.toThrow('API error 403')
    expect(prompt).not.toHaveBeenCalled()
    expect(sessionStorage.getItem(TOKEN_STORAGE_KEY)).toBe('viewer')
  })
})
