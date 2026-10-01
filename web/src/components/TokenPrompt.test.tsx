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

// TokenPrompt: the sign-in dialog opened by a 401 from the UI API
// (C07-controller-06 / C10a-web-03 / C10b-web-03).
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, cleanup, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { TokenPrompt } from './TokenPrompt'
import { api, TOKEN_STORAGE_KEY } from '../api/client'

function stubServer(validToken: string) {
  const auths: Array<string | undefined> = []
  vi.stubGlobal('fetch', vi.fn(async (_url: string, init?: RequestInit) => {
    const auth = ((init?.headers ?? {}) as Record<string, string>).Authorization
    auths.push(auth)
    if (auth !== `Bearer ${validToken}`) return new Response('unauthorized', { status: 401 })
    return new Response('[]', { status: 200 })
  }))
  return auths
}

beforeEach(() => {
  sessionStorage.clear()
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe('TokenPrompt', () => {
  it('renders nothing until a request needs a token', () => {
    render(<TokenPrompt />)
    expect(screen.queryByRole('dialog')).toBeNull()
  })

  it('opens on 401, sends the entered token and closes', async () => {
    const auths = stubServer('s3cret')
    render(<TokenPrompt />)

    const pipelines = api.listPipelines()
    const dialog = await screen.findByRole('dialog', { name: 'Sign in to kardinal' })
    expect(dialog).toBeInTheDocument()
    expect(screen.queryByRole('alert')).toBeNull()

    const input = screen.getByLabelText('Token')
    expect(input).toHaveAttribute('type', 'password')
    expect(input).toHaveFocus()
    await userEvent.type(input, '  s3cret  ')
    await userEvent.click(screen.getByRole('button', { name: 'Sign in' }))

    await expect(pipelines).resolves.toEqual([])
    expect(auths).toEqual([undefined, 'Bearer s3cret'])
    expect(sessionStorage.getItem(TOKEN_STORAGE_KEY)).toBe('s3cret')
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
  })

  it('says the token was rejected and asks again', async () => {
    stubServer('right')
    render(<TokenPrompt />)

    const pipelines = api.listPipelines()
    await userEvent.type(await screen.findByLabelText('Token'), 'wrong{Enter}')

    expect(await screen.findByRole('alert')).toHaveTextContent('The controller rejected this token')
    const input = screen.getByLabelText('Token')
    expect(input).toHaveValue('')
    expect(input).toHaveAttribute('aria-invalid', 'true')

    await userEvent.type(input, 'right{Enter}')
    await expect(pipelines).resolves.toEqual([])
  })

  it('does not submit an empty token', async () => {
    const auths = stubServer('t')
    render(<TokenPrompt />)

    const pipelines = api.listPipelines()
    await screen.findByRole('dialog')
    await userEvent.click(screen.getByRole('button', { name: 'Sign in' }))
    expect(screen.getByRole('alert')).toHaveTextContent('Enter a token.')
    expect(auths).toEqual([undefined])

    await userEvent.type(screen.getByLabelText('Token'), 't{Enter}')
    await expect(pipelines).resolves.toEqual([])
  })

  it('gives up pending requests when unmounted', async () => {
    stubServer('t')
    const { unmount } = render(<TokenPrompt />)
    const pipelines = api.listPipelines()
    await screen.findByRole('dialog')
    unmount()
    await expect(pipelines).rejects.toThrow('API error 401')
  })
})

// The page's insecure-connection banner sits behind the dialog's backdrop, and
// the token is typed here, so over plain HTTP from a non-loopback address the
// dialog warns by itself (docs/installation.md: a NodePort without TLS shows a
// security warning).
describe('TokenPrompt over plain HTTP', () => {
  const original = window.location
  const at = (href: string) => Object.defineProperty(window, 'location', { value: new URL(href), writable: true, configurable: true })
  afterEach(() => {
    Object.defineProperty(window, 'location', { value: original, writable: true, configurable: true })
  })

  it('warns that the token is sent unencrypted from a NodePort', async () => {
    at('http://10.0.0.1:30082/ui/')
    stubServer('t')
    render(<TokenPrompt />)
    void api.listPipelines().catch(() => {})
    const dialog = await screen.findByRole('dialog', { name: 'Sign in to kardinal' })
    expect(dialog).toHaveAccessibleDescription(/plain HTTP, so the token is sent unencrypted/)
    expect(dialog).toHaveTextContent('kubectl port-forward svc/kardinal-promoter -n kardinal-system 8082:8082')
    expect(screen.getByRole('link', { name: 'http://localhost:8082/ui/' })).toHaveAttribute('href', 'http://localhost:8082/ui/')
    // Advisory, not an error: the form's alert stays free for token errors.
    expect(screen.queryByRole('alert')).toBeNull()
  })

  it.each(['http://localhost:8082/ui/', 'http://127.0.0.1:8082/ui/', 'https://10.0.0.1:30443/ui/'])('does not warn on %s', async href => {
    at(href)
    stubServer('t')
    render(<TokenPrompt />)
    void api.listPipelines().catch(() => {})
    const dialog = await screen.findByRole('dialog', { name: 'Sign in to kardinal' })
    expect(dialog).not.toHaveTextContent('plain HTTP')
    expect(dialog).toHaveAttribute('aria-describedby', 'ui-token-desc')
  })
})
