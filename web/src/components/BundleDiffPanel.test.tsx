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

// BundleDiffPanel.test.tsx — Tests for the bundle comparison panel (#533).
import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { BundleDiffPanel } from './BundleDiffPanel'
import type { Bundle } from '../types'

const makeBundle = (overrides: Partial<Bundle> = {}): Bundle => ({
  name: 'bundle-a',
  namespace: 'default',
  phase: 'Verified',
  type: 'standard',
  pipeline: 'my-app',
  createdAt: '2026-04-15T10:00:00Z',
  ...overrides,
})

describe('BundleDiffPanel — rendering', () => {
  it('renders bundle A name', () => {
    const a = makeBundle({ name: 'bundle-alpha' })
    const b = makeBundle({ name: 'bundle-beta', phase: 'Superseded' })
    render(<BundleDiffPanel bundleA={a} bundleB={b} onClose={vi.fn()} />)
    expect(screen.getByText(/bundle-alpha/i)).toBeInTheDocument()
  })

  it('renders bundle B name', () => {
    const a = makeBundle()
    const b = makeBundle({ name: 'bundle-beta', phase: 'Failed' })
    render(<BundleDiffPanel bundleA={a} bundleB={b} onClose={vi.fn()} />)
    expect(screen.getByText(/bundle-beta/i)).toBeInTheDocument()
  })

  it('shows changed Phase field when phases differ', () => {
    const a = makeBundle({ phase: 'Verified' })
    const b = makeBundle({ phase: 'Failed' })
    render(<BundleDiffPanel bundleA={a} bundleB={b} onClose={vi.fn()} />)
    // Label is rendered as "▸ Phase" when changed
    expect(screen.getByText(/Phase/)).toBeInTheDocument()
    expect(screen.getByText('Verified')).toBeInTheDocument()
    expect(screen.getByText('Failed')).toBeInTheDocument()
  })

  it('shows Author field when provenance differs', () => {
    const a = makeBundle({ provenance: { author: 'alice' } })
    const b = makeBundle({ provenance: { author: 'bob' } })
    render(<BundleDiffPanel bundleA={a} bundleB={b} onClose={vi.fn()} />)
    expect(screen.getByText(/Author/)).toBeInTheDocument()
  })

  it('shows Commit SHA when SHAs differ', () => {
    const a = makeBundle({ provenance: { commitSHA: 'aaaaaa000000' } })
    const b = makeBundle({ provenance: { commitSHA: 'bbbbbb111111' } })
    render(<BundleDiffPanel bundleA={a} bundleB={b} onClose={vi.fn()} />)
    expect(screen.getByText(/Commit SHA/)).toBeInTheDocument()
  })
})

describe('BundleDiffPanel — close button', () => {
  it('calls onClose when close comparison button is clicked', async () => {
    const user = userEvent.setup()
    const onClose = vi.fn()
    render(<BundleDiffPanel bundleA={makeBundle()} bundleB={makeBundle({ name: 'b2' })} onClose={onClose} />)
    // The header × button has aria-label="Close comparison"
    const closeBtn = screen.getByLabelText('Close comparison')
    await user.click(closeBtn)
    expect(onClose).toHaveBeenCalledTimes(1)
  })
})
