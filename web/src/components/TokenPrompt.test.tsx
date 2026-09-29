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
