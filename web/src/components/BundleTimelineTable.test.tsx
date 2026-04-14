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

// BundleTimelineTable.test.tsx — Tests for #503 bundle promotion timeline.
import { describe, it, expect } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import type { Bundle } from '../types'
import {
  BundleTimelineTable,
  sortBundlesNewestFirst,
  isBundleDimmed,
  formatBundleAge,
  shortBundleVersion,
} from './BundleTimelineTable'

// ─── Fixtures ─────────────────────────────────────────────────────────────────

function makeBundle(overrides: Partial<Bundle> = {}): Bundle {
  return {
    name: 'my-app-sha-abc1234',
    namespace: 'default',
    phase: 'Verified',
    type: 'image',
    pipeline: 'my-app',
    createdAt: new Date(Date.now() - 3600000).toISOString(),
    ...overrides,
  }
}

// ─── sortBundlesNewestFirst ───────────────────────────────────────────────────

describe('sortBundlesNewestFirst', () => {
  it('sorts by createdAt descending (newest first)', () => {
    const older = makeBundle({ name: 'b-older', createdAt: '2026-01-01T00:00:00Z' })
    const newer = makeBundle({ name: 'b-newer', createdAt: '2026-06-01T00:00:00Z' })
    const sorted = sortBundlesNewestFirst([older, newer])
    expect(sorted[0].name).toBe('b-newer')
    expect(sorted[1].name).toBe('b-older')
  })

  it('falls back to reverse-lexicographic name when no createdAt', () => {
    const a = makeBundle({ name: 'app-aaa', createdAt: undefined })
    const b = makeBundle({ name: 'app-bbb', createdAt: undefined })
    const sorted = sortBundlesNewestFirst([a, b])
    expect(sorted[0].name).toBe('app-bbb')
  })

  it('does not mutate the original array', () => {
    const original = [makeBundle({ name: 'a' }), makeBundle({ name: 'b' })]
    sortBundlesNewestFirst(original)
    expect(original[0].name).toBe('a')
  })
})

// ─── isBundleDimmed ───────────────────────────────────────────────────────────

describe('isBundleDimmed', () => {
  it('returns true for Superseded', () => {
    expect(isBundleDimmed('Superseded')).toBe(true)
  })

  it('returns true for Failed', () => {
    expect(isBundleDimmed('Failed')).toBe(true)
  })

  it('returns false for Verified', () => {
    expect(isBundleDimmed('Verified')).toBe(false)
  })

  it('returns false for Promoting', () => {
    expect(isBundleDimmed('Promoting')).toBe(false)
  })
})

// ─── formatBundleAge ──────────────────────────────────────────────────────────

describe('formatBundleAge', () => {
  it('returns — for undefined', () => {
    expect(formatBundleAge(undefined)).toBe('—')
  })

  it('formats seconds', () => {
    const iso = new Date(Date.now() - 30000).toISOString()
    expect(formatBundleAge(iso)).toBe('30s ago')
  })

  it('formats minutes', () => {
    const iso = new Date(Date.now() - 5 * 60 * 1000).toISOString()
    expect(formatBundleAge(iso)).toBe('5m ago')
  })

  it('formats hours', () => {
    const iso = new Date(Date.now() - 2 * 3600 * 1000).toISOString()
    expect(formatBundleAge(iso)).toBe('2h ago')
  })

  it('formats days', () => {
    const iso = new Date(Date.now() - 3 * 86400 * 1000).toISOString()
    expect(formatBundleAge(iso)).toBe('3d ago')
  })
})

// ─── shortBundleVersion ───────────────────────────────────────────────────────

describe('shortBundleVersion', () => {
  it('returns last 2 segments for hyphenated names', () => {
    expect(shortBundleVersion('my-app-sha-abc1234')).toBe('sha-abc1234')
  })

  it('falls back to last 8 chars for non-hyphenated names', () => {
    expect(shortBundleVersion('abc12345678')).toBe('12345678')
  })
})

// ─── BundleTimelineTable component ───────────────────────────────────────────

describe('BundleTimelineTable', () => {
  it('shows empty state when no bundles', () => {
    render(<BundleTimelineTable bundles={[]} />)
    expect(screen.getByText('No bundles promoted yet.')).toBeInTheDocument()
  })

  it('renders bundle version and phase chip', () => {
    const b = makeBundle({ name: 'my-app-sha-abc1234', phase: 'Verified' })
    render(<BundleTimelineTable bundles={[b]} />)
    expect(screen.getByText('sha-abc1234')).toBeInTheDocument()
  })

  it('renders env chips for each environment', () => {
    const b = makeBundle({
      environments: [
        { name: 'test', phase: 'Verified' },
        { name: 'prod', phase: 'WaitingForMerge', prURL: 'https://github.com/org/repo/pull/42' },
      ],
    })
    render(<BundleTimelineTable bundles={[b]} />)
    expect(screen.getByTitle(/test: Verified/)).toBeInTheDocument()
    expect(screen.getByTitle(/prod: WaitingForMerge/)).toBeInTheDocument()
  })

  it('renders PR link for env with prURL', () => {
    const b = makeBundle({
      environments: [
        { name: 'prod', phase: 'WaitingForMerge', prURL: 'https://github.com/org/repo/pull/42' },
      ],
    })
    render(<BundleTimelineTable bundles={[b]} />)
    const link = screen.getByLabelText('Open PR for prod')
    expect(link).toBeInTheDocument()
    expect(link).toHaveAttribute('href', 'https://github.com/org/repo/pull/42')
  })

  it('shows author when provenance is set', () => {
    const b = makeBundle({ provenance: { author: 'alice', commitSHA: 'abc123' } })
    render(<BundleTimelineTable bundles={[b]} />)
    expect(screen.getByText('alice')).toBeInTheDocument()
  })

  it('dims Superseded bundles (lower opacity)', () => {
    const b = makeBundle({ phase: 'Superseded', name: 'old-bundle-sha-aaa' })
    const { container } = render(<BundleTimelineTable bundles={[b]} />)
    const row = container.querySelector('tr[style*="opacity: 0.45"]')
    expect(row).toBeInTheDocument()
  })

  it('does not dim Verified bundles', () => {
    const b = makeBundle({ phase: 'Verified', name: 'good-bundle-sha-bbb' })
    const { container } = render(<BundleTimelineTable bundles={[b]} />)
    const row = container.querySelector('tr[style*="opacity: 1"]')
    expect(row).toBeInTheDocument()
  })

  it('shows "Load more" button when bundles exceed pageSize', () => {
    const bundles = Array.from({ length: 5 }, (_, i) =>
      makeBundle({ name: `app-sha-${String(i).padStart(7, '0')}` })
    )
    render(<BundleTimelineTable bundles={bundles} pageSize={3} />)
    expect(screen.getByText('+ 2 more bundles')).toBeInTheDocument()
  })

  it('does not show "Load more" when bundles <= pageSize', () => {
    const bundles = [makeBundle(), makeBundle({ name: 'app-sha-2222222' })]
    render(<BundleTimelineTable bundles={bundles} pageSize={5} />)
    expect(screen.queryByText(/more bundles/)).not.toBeInTheDocument()
  })

  it('shows all bundles after clicking "Load more"', () => {
    const bundles = Array.from({ length: 5 }, (_, i) =>
      makeBundle({ name: `app-sha-${String(i).padStart(7, '0')}` })
    )
    render(<BundleTimelineTable bundles={bundles} pageSize={3} />)
    fireEvent.click(screen.getByText('+ 2 more bundles'))
    expect(screen.queryByText(/more bundles/)).not.toBeInTheDocument()
    // All 5 rows visible (not just 3)
    const rows = screen.getAllByRole('row')
    expect(rows.length).toBe(6) // 1 header + 5 data rows
  })
})
