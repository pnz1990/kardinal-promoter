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
import { render, screen, fireEvent } from '@testing-library/react'
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

  // C10a-web-16: assert the changed marker and the count, not just the label
  // (the label renders on every row, changed or not).
  it.each([
    { field: 'Phase', a: { phase: 'Verified' }, b: { phase: 'Failed' } },
    { field: 'Author', a: { provenance: { author: 'alice' } }, b: { provenance: { author: 'bob' } } },
    { field: 'Commit SHA', a: { provenance: { commitSHA: 'aaaaaa000000' } }, b: { provenance: { commitSHA: 'bbbbbb111111' } } },
  ])('marks $field as the one changed field', ({ field, a, b }) => {
    render(<BundleDiffPanel bundleA={makeBundle(a)} bundleB={makeBundle(b)} onClose={vi.fn()} />)
    expect(screen.getByText(`▸ ${field}`)).toBeInTheDocument()
    expect(screen.getByText(/1 field differs/)).toBeInTheDocument()
    for (const other of ['Phase', 'Author', 'Commit SHA'].filter(f => f !== field)) {
      expect(screen.queryByText(`▸ ${other}`)).not.toBeInTheDocument()
    }
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

describe('BundleDiffPanel — what counts as a difference (C10a-web-07)', () => {
  const img = (tag: string) => [{ repository: 'ghcr.io/pnz1990/kardinal-test-app', tag }]
  it.each([
    {
      name: 'different image tags are a difference',
      a: { images: img('sha-aaa1111') }, b: { images: img('sha-bbb2222') },
      summary: '1 field differs between these bundles', shown: ['ghcr.io/pnz1990/kardinal-test-app:sha-bbb2222'],
    },
    {
      name: 'different environment states are a difference',
      a: { environments: [{ name: 'prod', phase: 'Verified' }] }, b: { environments: [{ name: 'prod', phase: 'Failed' }] },
      summary: '1 field differs between these bundles', shown: ['prod: Failed'],
    },
    {
      name: 'only the creation time differs: no difference',
      a: { images: img('sha-aaa1111'), createdAt: '2026-04-15T10:00:00Z' },
      b: { images: img('sha-aaa1111'), createdAt: '2026-04-16T10:00:00Z' },
      summary: '0 fields differ between these bundles', shown: ['✓ No differences found between these bundles.'],
    },
  ])('$name', ({ a, b, summary, shown }) => {
    render(<BundleDiffPanel bundleA={makeBundle({ name: 'a', ...a })} bundleB={makeBundle({ name: 'b', ...b })} onClose={vi.fn()} />)
    expect(screen.getByText(summary)).toBeInTheDocument()
    for (const text of shown) expect(screen.getByText(text)).toBeInTheDocument()
  })
})

describe('BundleDiffPanel — keyboard (C10a-web-17)', () => {
  it('moves focus into the panel and closes on Escape', () => {
    const onClose = vi.fn()
    render(<BundleDiffPanel bundleA={makeBundle()} bundleB={makeBundle({ name: 'b2' })} onClose={onClose} />)
    expect(screen.getByLabelText('Close comparison')).toHaveFocus()
    fireEvent.keyDown(document.activeElement!, { key: 'Escape' })
    expect(onClose).toHaveBeenCalledOnce()
  })
})
