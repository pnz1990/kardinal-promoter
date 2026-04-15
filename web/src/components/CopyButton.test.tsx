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
import { render, screen, fireEvent } from '@testing-library/react'
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
})
