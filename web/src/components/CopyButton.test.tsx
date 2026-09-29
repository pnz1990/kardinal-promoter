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

// components/CopyButton.test.tsx — Unit tests for #530 shared CopyButton.
import { describe, it, expect, vi, afterEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import CopyButton from './CopyButton'

describe('CopyButton', () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('renders the copy icon by default', () => {
    render(<CopyButton text="hello" />)
    const btn = screen.getByTestId('copy-button')
    expect(btn).toHaveTextContent('📋')
  })

  it('uses clipboard API when available', async () => {
    const writeFn = vi.fn().mockResolvedValue(undefined)
    Object.defineProperty(navigator, 'clipboard', {
      value: { writeText: writeFn },
      configurable: true,
    })
    render(<CopyButton text="kubectl apply" />)
    fireEvent.click(screen.getByTestId('copy-button'))
    expect(writeFn).toHaveBeenCalledWith('kubectl apply')
  })

  it('has correct title attribute', () => {
    render(<CopyButton text="x" title="Copy command" />)
    expect(screen.getByTestId('copy-button')).toHaveAttribute('title', 'Copy command')
  })

  it('shows default title when no title prop', () => {
    render(<CopyButton text="x" />)
    expect(screen.getByTestId('copy-button')).toHaveAttribute('title', 'Copy to clipboard')
  })

  describe('without a working Clipboard API (C10a-web-08)', () => {
    afterEach(() => {
      vi.useRealTimers()
      Object.defineProperty(navigator, 'clipboard', { value: undefined, configurable: true })
    })

    it.each([
      { name: 'no Clipboard API (plain-HTTP origin)', clipboard: undefined, exec: true, want: '✓', title: 'Copied!' },
      { name: 'Clipboard API rejects', clipboard: { writeText: () => Promise.reject(new Error('denied')) }, exec: true, want: '✓', title: 'Copied!' },
      { name: 'both fail', clipboard: undefined, exec: false, want: '✕', title: 'Copy failed: select the text and copy it by hand' },
    ])('$name', async ({ clipboard, exec, want, title }) => {
      Object.defineProperty(navigator, 'clipboard', { value: clipboard, configurable: true })
      const execCommand = vi.fn().mockReturnValue(exec)
      Object.defineProperty(document, 'execCommand', { value: execCommand, configurable: true })
      render(<CopyButton text="kubectl get pipelines" />)
      fireEvent.click(screen.getByTestId('copy-button'))
      await waitFor(() => expect(screen.getByTestId('copy-button')).toHaveTextContent(want))
      expect(execCommand).toHaveBeenCalledWith('copy')
      expect(screen.getByTestId('copy-button')).toHaveAttribute('title', title)
    })

    it('clears its reset timer on unmount', () => {
      Object.defineProperty(document, 'execCommand', { value: vi.fn().mockReturnValue(true), configurable: true })
      const setSpy = vi.spyOn(globalThis, 'setTimeout')
      const clearSpy = vi.spyOn(globalThis, 'clearTimeout')
      const { unmount } = render(<CopyButton text="x" />)
      fireEvent.click(screen.getByTestId('copy-button'))
      const idx = setSpy.mock.calls.findIndex(c => c[1] === 2000)
      expect(idx).toBeGreaterThanOrEqual(0)
      const id = setSpy.mock.results[idx].value
      unmount()
      expect(clearSpy).toHaveBeenCalledWith(id)
    })
  })
})
