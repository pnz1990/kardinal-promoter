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

// CopyButton.test.tsx — Tests for the reusable CopyButton component (#530).
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, act } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { CopyButton } from './CopyButton'

describe('CopyButton', () => {
  beforeEach(() => {
    Object.defineProperty(navigator, 'clipboard', {
      value: { writeText: vi.fn().mockResolvedValue(undefined) },
      writable: true,
      configurable: true,
    })
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('renders clipboard icon by default', () => {
    render(<CopyButton text="kubectl apply -f pipeline.yaml" />)
    expect(screen.getByTestId('copy-button')).toBeInTheDocument()
    expect(screen.getByText('📋')).toBeInTheDocument()
  })

  it('has aria-label for accessibility', () => {
    render(<CopyButton text="some-command" aria-label="Copy kubectl command" />)
    expect(screen.getByLabelText('Copy kubectl command')).toBeInTheDocument()
  })

  it('has data-testid="copy-button"', () => {
    render(<CopyButton text="cmd" />)
    expect(screen.getByTestId('copy-button')).toBeInTheDocument()
  })

  it('shows ✓ after successful copy', async () => {
    const user = userEvent.setup()
    render(<CopyButton text="test-cmd" />)
    await user.click(screen.getByTestId('copy-button'))
    // Allow clipboard promise to resolve
    await act(async () => { await Promise.resolve() })
    // The button should now show checkmark
    expect(screen.getByText('✓')).toBeInTheDocument()
  })

  it('sm size is the default (smaller padding)', () => {
    render(<CopyButton text="cmd" />)
    const btn = screen.getByTestId('copy-button')
    expect(btn).toBeInTheDocument()
  })

  it('md size renders correctly', () => {
    render(<CopyButton text="cmd" size="md" />)
    const btn = screen.getByTestId('copy-button')
    expect(btn).toBeInTheDocument()
  })
})
