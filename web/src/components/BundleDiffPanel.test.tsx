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
  type: 'image',
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
    { field: 'State', a: { phase: 'Verified' }, b: { phase: 'Failed' } },
    { field: 'Author', a: { provenance: { author: 'alice' } }, b: { provenance: { author: 'bob' } } },
    { field: 'Commit SHA', a: { provenance: { commitSHA: 'aaaaaa000000' } }, b: { provenance: { commitSHA: 'bbbbbb111111' } } },
  ])('marks $field as the one changed field', ({ field, a, b }) => {
    render(<BundleDiffPanel bundleA={makeBundle(a)} bundleB={makeBundle(b)} onClose={vi.fn()} />)
    expect(screen.getByText(`▸ ${field}`)).toBeInTheDocument()
    expect(screen.getByText(/1 field differs/)).toBeInTheDocument()
    for (const other of ['State', 'Author', 'Commit SHA'].filter(f => f !== field)) {
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
      summary: '1 field differs between these versions', shown: ['ghcr.io/pnz1990/kardinal-test-app:sha-bbb2222'],
    },
    {
      name: 'different environment states are a difference',
      a: { environments: [{ name: 'prod', phase: 'Verified' }] }, b: { environments: [{ name: 'prod', phase: 'Failed' }] },
      summary: '1 field differs between these versions', shown: ['prod: Failed'],
    },
    {
      name: 'only the creation time differs: no difference',
      a: { images: img('sha-aaa1111'), createdAt: '2026-04-15T10:00:00Z' },
      b: { images: img('sha-aaa1111'), createdAt: '2026-04-16T10:00:00Z' },
      summary: '0 fields differ between these versions', shown: ['✓ No differences found between these versions.'],
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

// E2E-R22: the CI Run cell links only a URL that isCIRunURL accepts, and
// shows "—" for anything else, never the value: it can hold credentials.
describe('BundleDiffPanel — CI Run link', () => {
  it.each([
    { name: 'https is linked', url: 'https://ci.example.com/1', linked: true },
    { name: 'javascript is not shown', url: 'javascript:alert(1)', linked: false },
    { name: 'user info is not shown', url: 'https://u:s3cret@ci.example.com/1', linked: false },
  ])('$name', ({ url, linked }) => {
    const { container } = render(<BundleDiffPanel bundleA={makeBundle({ name: 'a', provenance: { ciRunURL: url } })} bundleB={makeBundle({ name: 'b' })} onClose={vi.fn()} />)
    if (linked) {
      expect(screen.getByText(url).closest('a')).toHaveAttribute('href', url)
      return
    }
    expect(container.querySelector('a')).toBeNull()
    expect(container.innerHTML).not.toContain(url.slice(0, 12))
    expect(container.innerHTML).not.toContain('s3cret')
  })
})

describe('BundleDiffPanel — versions by tag', () => {
  it('names each version by what it ships, with its Bundle name after it', () => {
    const a = makeBundle({ name: 'app-cfg9', type: 'config', version: 'config 77aa001' })
    const b = makeBundle({ name: 'app-prev', images: [{ repository: 'ghcr.io/o/app', tag: 'sha-9f8e7d6' }] })
    const { container } = render(<BundleDiffPanel bundleA={a} bundleB={b} onClose={vi.fn()} />)
    const tags = [...container.querySelectorAll('.bundle-diff__row--names .version-label__tag')].map((e) => e.textContent)
    expect(tags).toEqual(['config 77aa001', 'sha-9f8e7d6'])
    const names = [...container.querySelectorAll('.bundle-diff__row--names .version-label__name')].map((e) => e.textContent)
    expect(names).toEqual(['app-cfg9', 'app-prev'])
  })
})
