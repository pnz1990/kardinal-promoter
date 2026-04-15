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

// BundleTimeline.test.tsx — Tests for the bundle timeline component (#533).
import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { BundleTimeline } from './BundleTimeline'
import type { Bundle } from '../types'

const makeBundle = (overrides: Partial<Bundle> = {}): Bundle => ({
  name: 'my-app-abc123',
  namespace: 'default',
  phase: 'Promoting',
  type: 'standard',
  pipeline: 'my-app',
  createdAt: '2026-04-15T10:00:00Z',
  ...overrides,
})

describe('BundleTimeline — empty state', () => {
  it('renders nothing when bundles=[]', () => {
    const { container } = render(<BundleTimeline bundles={[]} />)
    expect(container.firstChild).toBeNull()
  })
})

describe('BundleTimeline — bundle rendering', () => {
  it('renders bundle phase label', () => {
    const bundles = [makeBundle({ phase: 'Verified' })]
    render(<BundleTimeline bundles={bundles} />)
    expect(screen.getByText('Verified')).toBeInTheDocument()
  })

  it('renders Promoting phase', () => {
    const bundles = [makeBundle({ phase: 'Promoting' })]
    render(<BundleTimeline bundles={bundles} />)
    expect(screen.getByText('Promoting')).toBeInTheDocument()
  })

  it('renders Superseded as "Sup"', () => {
    const bundles = [makeBundle({ phase: 'Superseded' })]
    render(<BundleTimeline bundles={bundles} />)
    expect(screen.getByText('Sup')).toBeInTheDocument()
  })

  it('shows bundle history header', () => {
    const bundles = [makeBundle()]
    render(<BundleTimeline bundles={bundles} />)
    expect(screen.getByText(/Bundle History/i)).toBeInTheDocument()
  })

  it('shows shift-click hint when 2+ bundles and no comparison', () => {
    const bundles = [makeBundle(), makeBundle({ name: 'my-app-def456' })]
    render(<BundleTimeline bundles={bundles} />)
    expect(screen.getByText(/Shift-click to compare/i)).toBeInTheDocument()
  })
})

describe('BundleTimeline — selection', () => {
  it('calls onSelectBundle when bundle is clicked', async () => {
    const user = userEvent.setup()
    const onSelect = vi.fn()
    const bundles = [makeBundle({ name: 'my-app-abc123' })]
    render(<BundleTimeline bundles={bundles} onSelectBundle={onSelect} />)
    // Click any button in the timeline (the bundle chip)
    const buttons = screen.getAllByRole('button')
    const bundleBtn = buttons.find(b => b.title?.includes('my-app-abc123'))
    if (bundleBtn) await user.click(bundleBtn)
    expect(onSelect).toHaveBeenCalledWith('my-app-abc123')
  })

  it('shows Compare button when two bundles are selected', () => {
    const bundles = [
      makeBundle({ name: 'bundle-a' }),
      makeBundle({ name: 'bundle-b', phase: 'Verified' }),
    ]
    render(
      <BundleTimeline
        bundles={bundles}
        selectedBundle="bundle-a"
        compareBundle="bundle-b"
        onCompare={vi.fn()}
      />
    )
    expect(screen.getByText(/Compare ↔/i)).toBeInTheDocument()
  })

  it('shows clear comparison button when compareBundle is set', () => {
    const bundles = [makeBundle({ name: 'bundle-a' }), makeBundle({ name: 'bundle-b' })]
    render(
      <BundleTimeline
        bundles={bundles}
        selectedBundle="bundle-a"
        compareBundle="bundle-b"
        onCompareBundle={vi.fn()}
      />
    )
    expect(screen.getByText(/× clear/i)).toBeInTheDocument()
  })
})
