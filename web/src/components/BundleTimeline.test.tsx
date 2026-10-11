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
import { fireEvent, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { BundleTimeline } from './BundleTimeline'
import type { Bundle } from '../types'

const makeBundle = (overrides: Partial<Bundle> = {}): Bundle => ({
  name: 'my-app-abc123',
  namespace: 'default',
  phase: 'Promoting',
  type: 'image',
  pipeline: 'my-app',
  createdAt: '2026-04-15T10:00:00Z',
  ...overrides,
})

describe('BundleTimeline — empty state', () => {
  it('renders nothing when bundles=[]', () => {
    const { container } = render(<BundleTimeline bundles={[]} />)
    expect(container.firstChild).toBeNull()
  })

  it('renders skeleton when loading=true (#784)', () => {
    const { container } = render(<BundleTimeline bundles={[]} loading />)
    expect(container.firstChild).not.toBeNull()
    expect(screen.queryByText('Verified')).not.toBeInTheDocument()
    expect(container.querySelector('[data-testid="bundle-timeline-skeleton"]')).not.toBeNull()
  })

  it('renders bundles when loading=false even if passed explicitly', () => {
    const bundles = [makeBundle({ phase: 'Verified' })]
    render(<BundleTimeline bundles={bundles} loading={false} />)
    expect(screen.getByText('Verified')).toBeInTheDocument()
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

  it('shows each phase by its display label, never a raw or truncated value', () => {
    const bundles = [makeBundle({ phase: 'Superseded' }), makeBundle({ name: 'my-app-def456', phase: 'Available' })]
    render(<BundleTimeline bundles={bundles} />)
    expect(screen.getByText('Replaced by newer version')).toBeInTheDocument()
    expect(screen.getByText('Ready to promote')).toBeInTheDocument()
    expect(screen.queryByText('Sup')).toBeNull()
  })

  it('names chips by version: the Bundle name without the pipeline prefix', () => {
    render(<BundleTimeline bundles={[makeBundle({ name: 'my-app-1-4-2', pipeline: 'my-app' })]} />)
    expect(screen.getByText('1-4-2')).toBeInTheDocument()
  })

  it('shows the versions header and no shift-click helper copy', () => {
    const bundles = [makeBundle(), makeBundle({ name: 'my-app-def456' })]
    render(<BundleTimeline bundles={bundles} />)
    expect(screen.getByText(/Versions \(oldest → newest\)/)).toBeInTheDocument()
    expect(screen.queryByText(/Shift-click to compare/i)).toBeNull()
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

  // The shown Bundle is Bundle A; it cannot also be Bundle B.
  it('ignores a shift-click on the selected bundle', async () => {
    const user = userEvent.setup()
    const onCompareBundle = vi.fn()
    const onSelectBundle = vi.fn()
    const bundles = [makeBundle({ name: 'bundle-a' }), makeBundle({ name: 'bundle-b' })]
    render(
      <BundleTimeline
        bundles={bundles}
        selectedBundle="bundle-a"
        onSelectBundle={onSelectBundle}
        onCompareBundle={onCompareBundle}
      />
    )
    const chipOf = (name: string) => screen.getAllByRole('button').find(b => b.title.startsWith(`${name}: `))!
    expect(chipOf('bundle-a').title).toBe('bundle-a: Promoting')
    expect(chipOf('bundle-b').title).toBe('bundle-b: Promoting\nShift-click or c to compare')

    await user.keyboard('{Shift>}')
    await user.click(chipOf('bundle-a'))
    expect(onCompareBundle).not.toHaveBeenCalled()
    expect(onSelectBundle).not.toHaveBeenCalled()
    await user.click(chipOf('bundle-b'))
    await user.keyboard('{/Shift}')
    expect(onCompareBundle).toHaveBeenCalledExactlyOnceWith('bundle-b')
  })
})

describe('BundleTimeline — skeleton loading state (#784)', () => {
  it('shows data-testid=bundle-timeline-skeleton when loading=true', () => {
    const { getByTestId } = render(
      <BundleTimeline
        bundles={[]}
        loading={true}
      />
    )
    expect(getByTestId('bundle-timeline-skeleton')).toBeDefined()
  })

  it('renders bundles normally when loading=false', () => {
    const bundles = [makeBundle({ name: 'b1', createdAt: '2026-04-15T10:00:00Z' })]
    const { queryByTestId } = render(
      <BundleTimeline
        bundles={bundles}
        loading={false}
      />
    )
    expect(queryByTestId('bundle-timeline-skeleton')).toBeNull()
  })
})

// Audit C10a-web-19 / C10b-web-29: one ordering shared with App, the selected
// bundle never drops off the timeline, and chips report their state.
describe('BundleTimeline — ordering and visibility', () => {
  const chipNames = () =>
    screen.getAllByRole('button', { pressed: undefined })
      .filter(b => b.classList.contains('bundle-chip'))
      .map(b => b.title.split(':')[0])

  const a = makeBundle({ name: 'app-aaaaa', createdAt: '2026-04-15T10:00:00Z' })
  const b = makeBundle({ name: 'app-bbbbb', createdAt: '2026-04-15T11:00:00Z' })
  const c = makeBundle({ name: 'app-ccccc', createdAt: undefined })

  it.each([
    { name: 'oldest first input', input: [a, b, c] },
    { name: 'newest first input', input: [c, b, a] },
  ])('shows newest on the right (undated oldest, on the left), for $name', ({ input }) => {
    render(<BundleTimeline bundles={input} />)
    expect(chipNames()).toEqual(['app-ccccc', 'app-aaaaa', 'app-bbbbb'])
  })

  it('keeps the selected and comparison bundles visible when they are older than the 10 newest', () => {
    const many = Array.from({ length: 14 }, (_, i) =>
      makeBundle({ name: `app-${String(i).padStart(5, '0')}`, createdAt: `2026-04-15T${String(i).padStart(2, '0')}:00:00Z` }))
    render(<BundleTimeline bundles={many} selectedBundle="app-00000" compareBundle="app-00001" />)
    const names = chipNames()
    expect(names).toHaveLength(12)
    // Oldest on the left: the kept older ones first, then the 10 newest.
    expect(names.slice(0, 2)).toEqual(['app-00000', 'app-00001'])
    expect(names.slice(2)).toEqual(many.slice(4).map(x => x.name))
  })

  it('marks only the selected chip as pressed', () => {
    render(<BundleTimeline bundles={[a, b]} selectedBundle="app-aaaaa" />)
    expect(screen.getByRole('button', { pressed: true })).toHaveAttribute('title', expect.stringContaining('app-aaaaa'))
    expect(screen.getAllByRole('button', { pressed: false })).toHaveLength(1)
  })

  it.each([
    { phase: '', wantClass: 'bundle-chip--unknown', label: 'Unknown' },
    { phase: 'Failed', wantClass: 'bundle-chip--failed', label: 'Failed' },
  ])('phase "$phase" gets class $wantClass and label $label', ({ phase, wantClass, label }) => {
    render(<BundleTimeline bundles={[makeBundle({ phase })]} />)
    const chip = screen.getByRole('button', { pressed: false })
    expect(chip).toHaveClass(wantClass)
    expect(chip).toHaveTextContent(label)
  })

  it.each([
    { name: 'shift-click picks the comparison bundle', compare: undefined, want: 'app-bbbbb' },
    { name: 'shift-click on the comparison bundle clears it', compare: 'app-bbbbb', want: null },
  ])('$name', async ({ compare, want }) => {
    const user = userEvent.setup()
    const onCompareBundle = vi.fn()
    const onSelectBundle = vi.fn()
    render(<BundleTimeline bundles={[a, b]} selectedBundle="app-aaaaa" compareBundle={compare}
      onCompareBundle={onCompareBundle} onSelectBundle={onSelectBundle} />)
    const chip = screen.getAllByRole('button').find(x => x.title.startsWith('app-bbbbb'))!
    await user.keyboard('{Shift>}')
    await user.click(chip)
    await user.keyboard('{/Shift}')
    expect(onCompareBundle).toHaveBeenCalledExactlyOnceWith(want)
    expect(onSelectBundle).not.toHaveBeenCalled()
  })
})

describe('BundleTimeline — keyboard and type badges', () => {
  const bundles = [
    makeBundle({ name: 'app-ccccc', createdAt: '2026-04-15T12:00:00Z' }),
    makeBundle({ name: 'app-bbbbb', createdAt: '2026-04-15T11:00:00Z', type: 'config', phase: 'Verified' }),
    makeBundle({ name: 'app-aaaaa', createdAt: '2026-04-15T10:00:00Z', type: 'mixed', phase: 'Superseded' }),
  ]

  it('is one Tab stop, the shown bundle, and arrow keys move between bundles', async () => {
    const user = userEvent.setup()
    render(<><button>before</button><BundleTimeline bundles={bundles} selectedBundle="app-bbbbb" /><button>after</button></>)
    const bar = screen.getByRole('toolbar', { name: /^Versions/ })
    const chips = Array.from(bar.querySelectorAll<HTMLButtonElement>('.bundle-chip'))
    expect(chips.map(c => c.tabIndex)).toEqual([-1, 0, -1])

    await user.click(screen.getByText('before'))
    await user.tab()
    expect(chips[1]).toHaveFocus()
    await user.keyboard('{ArrowRight}')
    expect(chips[2]).toHaveFocus()
    await user.keyboard('{ArrowRight}')
    expect(chips[2]).toHaveFocus()
    await user.keyboard('{Home}')
    expect(chips[0]).toHaveFocus()
    await user.keyboard('{End}')
    expect(chips[2]).toHaveFocus()
    await user.keyboard('{ArrowLeft}')
    expect(chips[1]).toHaveFocus()
    await user.tab()
    expect(screen.getByText('after')).toHaveFocus()
    await user.tab({ shift: true })
    expect(chips[1]).toHaveFocus()
  })

  it('badges config and mixed bundles, not image ones', () => {
    render(<BundleTimeline bundles={bundles} />)
    expect(screen.getByRole('img', { name: 'config version' })).toBeInTheDocument()
    expect(screen.getByRole('img', { name: 'image and config version' })).toBeInTheDocument()
    expect(screen.queryByRole('img', { name: 'image version' })).not.toBeInTheDocument()
    const chip = screen.getAllByRole('button').find(b => b.title.startsWith('app-bbbbb: '))!
    expect(chip.title).toContain('config version')
  })
})

describe('BundleTimeline — the version rail keys', () => {
  const a = makeBundle({ name: 'app-aaaaa', createdAt: '2026-04-15T01:00:00Z' })
  const b = makeBundle({ name: 'app-bbbbb', createdAt: '2026-04-15T02:00:00Z' })
  const c = makeBundle({ name: 'app-ccccc', createdAt: '2026-04-15T03:00:00Z' })
  it('[ and ] step to an older and a newer version, f to the newest', () => {
    const onSelect = vi.fn()
    render(<BundleTimeline bundles={[a, b, c]} selectedBundle="app-bbbbb" activeBundle="app-ccccc" onSelectBundle={onSelect} globalKeys />)
    fireEvent.keyDown(document.body, { key: '[' })
    expect(onSelect).toHaveBeenLastCalledWith('app-aaaaa')
    fireEvent.keyDown(document.body, { key: ']' })
    expect(onSelect).toHaveBeenLastCalledWith('app-ccccc')
    fireEvent.keyDown(document.body, { key: 'f' })
    expect(onSelect).toHaveBeenCalledTimes(3)
  })

  it('c on a focused tick compares it with the followed version; hover previews its route', () => {
    const onCompareBundle = vi.fn()
    const onCompare = vi.fn()
    const onPreview = vi.fn()
    render(<BundleTimeline bundles={[a, b, c]} selectedBundle="app-ccccc" onCompareBundle={onCompareBundle} onCompare={onCompare} onPreview={onPreview} />)
    const tick = screen.getAllByRole('button').find(x => x.title.startsWith('app-aaaaa'))!
    fireEvent.pointerEnter(tick)
    expect(onPreview).toHaveBeenLastCalledWith('app-aaaaa')
    fireEvent.keyDown(tick, { key: 'c' })
    expect(onCompareBundle).toHaveBeenCalledWith('app-aaaaa')
    expect(onCompare).toHaveBeenCalledWith('app-aaaaa')
  })
})
