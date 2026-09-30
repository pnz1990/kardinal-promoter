// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// PipelineList.virtual.test.tsx — the selected row keeps aria-pressed=true on
// the virtual-scrolling path (#815, #819, #1318). jsdom has no layout, so the
// real virtualizer renders no rows; this file stubs it to return a fixed
// window of rows, the way it does in a browser.
import { describe, it, expect, vi } from 'vitest'
import { render, screen, within } from '@testing-library/react'
import { PipelineList } from './PipelineList'
import type { Pipeline } from '../types'

// The stub renders the first 20 rows, 52px apart (the list's estimateSize).
vi.mock('@tanstack/react-virtual', () => ({
  useVirtualizer: ({ count }: { count: number }) => ({
    getVirtualItems: () =>
      Array.from({ length: Math.min(count, 20) }, (_, index) => ({
        index,
        key: index,
        start: index * 52,
        end: (index + 1) * 52,
        size: 52,
        lane: 0,
      })),
    getTotalSize: () => count * 52,
    measureElement: () => {},
  }),
}))

const pipelines: Pipeline[] = Array.from({ length: 100 }, (_, i) => ({
  name: `pipe-${i + 1}`,
  namespace: 'default',
  phase: 'Ready',
  environmentCount: 3,
}))

describe('PipelineList — aria-pressed in virtual mode (O4, #819)', () => {
  it('renders the virtual window, not all 100 rows', () => {
    const { container } = render(<PipelineList pipelines={pipelines} onSelect={vi.fn()} />)
    // Only the virtual path sets data-index on its rows.
    expect(container.querySelectorAll('li[data-index]')).toHaveLength(20)
    expect(screen.getByText('pipe-20')).toBeInTheDocument()
    expect(screen.queryByText('pipe-21')).not.toBeInTheDocument()
  })

  it.each([
    { name: 'first row', selected: 'pipe-1', selectedNamespace: undefined, want: 'pipe-1' },
    { name: 'row inside the window', selected: 'pipe-8', selectedNamespace: undefined, want: 'pipe-8' },
    { name: 'matching namespace', selected: 'pipe-8', selectedNamespace: 'default', want: 'pipe-8' },
    { name: 'other namespace', selected: 'pipe-8', selectedNamespace: 'other', want: null },
    { name: 'row outside the window', selected: 'pipe-60', selectedNamespace: undefined, want: null },
  ])('$name: selected=$selected', ({ selected, selectedNamespace, want }) => {
    render(
      <PipelineList
        pipelines={pipelines}
        selected={selected}
        selectedNamespace={selectedNamespace}
        onSelect={vi.fn()}
      />
    )
    const pressed = screen.queryAllByRole('button', { pressed: true })
    if (want === null) {
      expect(pressed).toHaveLength(0)
      return
    }
    expect(pressed).toHaveLength(1)
    expect(within(pressed[0]).getByText(want)).toBeInTheDocument()
    // Every other rendered row is explicitly not pressed.
    expect(screen.getAllByRole('button', { pressed: false })).toHaveLength(19)
  })
})
