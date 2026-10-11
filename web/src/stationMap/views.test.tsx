// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

import { describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, within } from '@testing-library/react'
import type { Bundle, Pipeline } from '../types'
import { buildModel } from './model'
import StageTable, { sortStations } from './StageTable'
import VersionMatrix, { columnsOf } from './VersionMatrix'

const step = (env: string, state: string) => ({ id: `step-${env}`, type: 'PromotionStep' as const, label: env, environment: env, state })
const bundle = (name: string, phase: string, envs: Record<string, string>): Bundle => ({
  name, namespace: 'default', phase, type: 'image', pipeline: 'app', images: [{ repository: 'r', tag: name }],
  environments: Object.entries(envs).map(([n, p]) => ({ name: n, phase: p })),
})

function chain(): Pipeline {
  return {
    name: 'app', namespace: 'default', phase: 'Ready', environmentCount: 3, activeBundleName: 'app-2',
    environmentTopology: [{ name: 'test' }, { name: 'uat', upstreams: ['test'] }, { name: 'prod', upstreams: ['uat'] }],
    deployed: {
      test: { bundle: 'app-2', version: '2', verifiedAt: '2026-10-11T10:00:00Z' },
      uat: { bundle: 'app-2', version: '2', verifiedAt: '2026-10-11T11:00:00Z' },
      prod: { bundle: 'app-1', version: '1', verifiedAt: '2026-10-09T10:00:00Z' },
    },
  }
}
const followed = { bundle: bundle('app-2', 'Promoting', {}), graph: { nodes: [step('test', 'Verified'), step('uat', 'Verified'), step('prod', 'Failed')], edges: [] } }

describe('StageTable', () => {
  it('lists every environment with its state, what it runs and what is on its way; Enter opens one', () => {
    const onOpenStage = vi.fn()
    render(<StageTable model={buildModel(chain(), followed)} onOpenStage={onOpenStage} />)
    const prod = screen.getByRole('row', { name: /^prod/ })
    expect(within(prod).getByText('Failed')).toBeTruthy()
    expect(prod.textContent).toContain('→ app-2')
    fireEvent.click(within(prod).getByRole('button', { name: 'prod' }))
    expect(onOpenStage).toHaveBeenCalledWith('prod')
  })

  it('sorts by state (most urgent first) and by age, spec order breaking ties', () => {
    const m = buildModel(chain(), followed)
    expect(sortStations(m, 'env', 'ascending').map(s => s.env)).toEqual(['test', 'uat', 'prod'])
    expect(sortStations(m, 'state', 'ascending').map(s => s.env)).toEqual(['prod', 'test', 'uat'])
    expect(sortStations(m, 'age', 'ascending').map(s => s.env)).toEqual(['uat', 'test', 'prod'])
    render(<StageTable model={m} onOpenStage={vi.fn()} />)
    fireEvent.click(screen.getByRole('button', { name: 'State' }))
    expect(screen.getByRole('columnheader', { name: /State/ }).getAttribute('aria-sort')).toBe('ascending')
    expect(screen.getAllByRole('row').slice(1).map(r => r.getAttribute('data-station'))).toEqual(['prod', 'test', 'uat'])
  })
})

describe('VersionMatrix', () => {
  const bundles = [
    bundle('app-2', 'Promoting', { test: 'Verified', uat: 'Verified', prod: 'Failed' }),
    bundle('app-1', 'Superseded', { test: 'Verified', uat: 'Verified', prod: 'Verified' }),
  ]

  it('shows each version in each environment by its display state; click follows, Shift-click compares', () => {
    const onSelect = vi.fn()
    const onCompare = vi.fn()
    render(<VersionMatrix model={buildModel(chain(), followed)} pipeline="app" bundles={bundles} followed="app-2"
      onSelect={onSelect} onCompare={onCompare} onToggleGroup={vi.fn()} />)
    const row = screen.getByRole('row', { name: /^app-2/ })
    expect(row.getAttribute('data-followed')).toBe('true')
    expect(within(row).getAllByRole('gridcell').map(c => c.getAttribute('aria-label'))).toEqual(['test: Live', 'uat: Live', 'prod: Failed'])
    fireEvent.click(screen.getByRole('button', { name: 'app-1' }))
    expect(onSelect).toHaveBeenCalledWith('app-1')
    fireEvent.click(screen.getByRole('button', { name: 'app-1' }), { shiftKey: true })
    expect(onCompare).toHaveBeenCalledWith('app-1')
  })

  it('collapses a fleet into one column that counts its live targets, and expands it into its targets', () => {
    const targets = ['p1', 'p2', 'p3']
    const p: Pipeline = { name: 'app', namespace: 'default', phase: 'Ready', environmentCount: 4, activeBundleName: 'app-2',
      environmentTopology: [{ name: 'test' }, { name: 'prod', upstreams: ['test'], fleet: { targets } }] }
    const b = [bundle('app-2', 'Promoting', { test: 'Verified', p1: 'Verified', p2: 'Failed' })]
    const onToggleGroup = vi.fn()
    const { rerender } = render(<VersionMatrix model={buildModel(p, undefined)} pipeline="app" bundles={b} onSelect={vi.fn()} onCompare={vi.fn()} onToggleGroup={onToggleGroup} />)
    expect(screen.getByRole('gridcell', { name: /^prod · 3: 1 of 3 live/ }).getAttribute('data-tone')).toBe('failed')
    fireEvent.click(screen.getByRole('button', { name: /^prod · 3/ }))
    expect(onToggleGroup).toHaveBeenCalledWith('platform:prod')
    const expanded = buildModel(p, undefined, { expanded: new Set(['platform:prod']) })
    expect(columnsOf(expanded).map(c => c.id)).toEqual(['test', 'p1', 'p2', 'p3'])
    rerender(<VersionMatrix model={expanded} pipeline="app" bundles={b} onSelect={vi.fn()} onCompare={vi.fn()} onToggleGroup={onToggleGroup} />)
    expect(screen.getAllByRole('columnheader').map(h => h.textContent)).toEqual(['Version', 'test', 'p1', 'p2', 'p3'])
  })

  it('renders only a window of rows for 200 versions', () => {
    const many = Array.from({ length: 200 }, (_, i) => bundle(`app-${200 - i}`, 'Superseded', { test: 'Verified' }))
    render(<VersionMatrix model={buildModel(chain(), undefined)} pipeline="app" bundles={many} onSelect={vi.fn()} onCompare={vi.fn()} onToggleGroup={vi.fn()} />)
    const rows = document.querySelectorAll('tr[data-bundle]').length
    expect(rows).toBeGreaterThan(0)
    expect(rows).toBeLessThan(60)
  })

  it('arrow keys move a roving focus between cells; Enter follows, Shift+Enter compares; the cell shows in words', () => {
    const onSelect = vi.fn()
    const onCompare = vi.fn()
    const raf = vi.spyOn(window, 'requestAnimationFrame').mockImplementation(cb => { cb(0); return 0 })
    render(<VersionMatrix model={buildModel(chain(), followed)} pipeline="app" bundles={bundles} followed="app-2"
      onSelect={onSelect} onCompare={onCompare} onToggleGroup={vi.fn()} />)
    const cells = () => screen.getAllByRole('gridcell')
    expect(cells().filter(c => c.getAttribute('tabindex') === '0')).toHaveLength(1)
    cells()[0].focus()
    fireEvent.keyDown(cells()[0], { key: 'ArrowRight' })
    expect(document.activeElement?.getAttribute('aria-label')).toBe('uat: Live')
    fireEvent.keyDown(document.activeElement!, { key: 'ArrowDown' })
    expect(document.activeElement?.getAttribute('data-cell')).toBe('1:1')
    expect(screen.getByText('app-1 in uat: Live')).toBeTruthy()
    fireEvent.keyDown(document.activeElement!, { key: 'Enter' })
    expect(onSelect).toHaveBeenCalledWith('app-1')
    fireEvent.keyDown(document.activeElement!, { key: 'Enter', shiftKey: true })
    expect(onCompare).toHaveBeenCalledWith('app-1')
    raf.mockRestore()
  })

  it('renders only a window of columns when 300 are shown', () => {
    const targets = Array.from({ length: 300 }, (_, i) => `t${i}`)
    const p: Pipeline = { name: 'app', namespace: 'default', phase: 'Ready', environmentCount: 300, activeBundleName: 'app-2',
      environmentTopology: [{ name: 'prod', fleet: { targets } }] }
    const m = buildModel(p, undefined, { expanded: new Set(['platform:prod']) })
    render(<VersionMatrix model={m} pipeline="app" bundles={bundles} onSelect={vi.fn()} onCompare={vi.fn()} onToggleGroup={vi.fn()} />)
    const heads = screen.getAllByRole('columnheader').length
    expect(heads).toBeGreaterThan(5)
    expect(heads).toBeLessThan(60)
  })
})

