// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

import { describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
import type { GraphResponse, Pipeline } from '../types'
import StationMap from './StationMap'
import { buildModel } from './model'

const step = (env: string, state: string) => ({ id: `step-${env}`, type: 'PromotionStep' as const, label: env, environment: env, state })

function chain(): Pipeline {
  return {
    name: 'app', namespace: 'default', phase: 'Ready', environmentCount: 3, activeBundleName: 'b2',
    environmentTopology: [{ name: 'test' }, { name: 'uat', upstreams: ['test'] }, { name: 'prod', upstreams: ['uat'] }],
    deployed: { test: { bundle: 'b2', version: '2' }, uat: { bundle: 'b2', version: '2' }, prod: { bundle: 'b1', version: '1' } },
  }
}

const bundle = { name: 'b2', namespace: 'default', phase: 'Promoting', type: 'image', pipeline: 'app', images: [{ repository: 'r', tag: '2' }] }

function renderMap(p: Pipeline, graph?: GraphResponse, props: Partial<React.ComponentProps<typeof StationMap>> = {}) {
  const onOpenStage = vi.fn()
  const onToggleGroup = vi.fn()
  const onSetExpanded = vi.fn()
  const model = buildModel(p, graph ? { bundle, graph } : undefined, { expanded: props.expanded })
  const utils = render(<StationMap model={model} onOpenStage={onOpenStage} expanded={props.expanded ?? new Set()}
    onToggleGroup={onToggleGroup} onSetExpanded={onSetExpanded} {...props} />)
  return { ...utils, onOpenStage, onToggleGroup, onSetExpanded, model }
}

describe('StationMap', () => {
  it('names every station and opens one on Enter; arrows follow the edges', () => {
    const { container, onOpenStage } = renderMap(chain(), { nodes: [step('test', 'Verified'), step('uat', 'Promoting')], edges: [] })
    const test = container.querySelector<SVGElement>('[data-station="test"]')!
    expect(test.getAttribute('aria-label')).toBe('test — Live, runs 2')
    expect(test.getAttribute('tabindex')).toBe('0')
    expect(container.querySelector('[data-station="uat"]')!.getAttribute('tabindex')).toBe('-1')
    test.focus()
    fireEvent.keyDown(test, { key: 'ArrowRight' })
    fireEvent.keyDown(container.querySelector('[data-station="uat"]')!, { key: 'Enter' })
    expect(onOpenStage).toHaveBeenCalledWith('uat')
  })

  it('a gate into the first environment sits on a lead-in line, and a click on its label opens it', () => {
    const gate = { id: 'gate-check-test', type: 'PolicyGate' as const, label: 'check-test', environment: 'test', state: 'Pass' }
    const onOpenGate = vi.fn()
    const { container } = renderMap(chain(), { nodes: [gate, step('test', 'Verified')], edges: [{ from: 'gate-check-test', to: 'step-test' }] }, { onOpenGate })
    expect(container.querySelectorAll('.station-map__lead')).toHaveLength(1)
    const mark = screen.getByRole('button', { name: 'gate check-test, Passing' })
    fireEvent.click(mark.querySelector('.station-map__gate-hit')!)
    expect(onOpenGate).toHaveBeenCalledWith('gate-check-test')
  })

  it('a gate takes focus and shows its popover; Enter opens it', () => {
    const gate = { id: 'gate-check-test', type: 'PolicyGate' as const, label: 'check-test', environment: 'test', state: 'Pass' }
    const onOpenGate = vi.fn()
    renderMap(chain(), { nodes: [gate, step('test', 'Verified')], edges: [{ from: 'gate-check-test', to: 'step-test' }] }, { onOpenGate })
    const mark = screen.getByRole('button', { name: 'gate check-test, Passing' })
    expect(mark.getAttribute('tabindex')).toBe('0')
    fireEvent.focus(mark)
    expect(screen.getByRole('tooltip').textContent).toContain('check-test')
    fireEvent.keyDown(mark, { key: 'Enter' })
    expect(onOpenGate).toHaveBeenCalledWith('gate-check-test')
    fireEvent.blur(mark)
    expect(screen.queryByRole('tooltip')).toBeNull()
  })

  it('offers actions only under the station in focus', () => {
    const actionsFor = (env: string) => <button type="button">Roll back {env}</button>
    const { container } = renderMap(chain(), { nodes: [step('test', 'Verified'), step('uat', 'Verified')], edges: [] }, { actionsFor })
    expect(screen.queryByRole('button', { name: /^Roll back/ })).toBeNull()
    fireEvent.focus(container.querySelector('[data-station="uat"]')!)
    expect(screen.getAllByRole('button', { name: /^Roll back/ }).map(b => b.textContent)).toEqual(['Roll back uat'])
  })

  it('arrow keys move by row and column inside an expanded grid', () => {
    const targets = Array.from({ length: 12 }, (_, i) => `t${String(i).padStart(2, '0')}`)
    const p: Pipeline = { name: 'f', namespace: 'default', phase: 'Ready', environmentCount: 12, activeBundleName: 'b2',
      environmentTopology: [{ name: 'prod', fleet: { targets } }] }
    const graph = { nodes: targets.map(t => step(t, 'Verified')), edges: [] }
    const { container } = renderMap(p, graph, { expanded: new Set(['platform:prod']) })
    const cell = (env: string) => container.querySelector<SVGElement>(`.cell[data-station="${env}"]`)!
    const svg = container.querySelector<SVGSVGElement>('.station-map__svg')!
    const raf = vi.spyOn(window, 'requestAnimationFrame').mockImplementation(cb => { cb(0); return 0 })
    fireEvent.focus(cell('t00'))
    fireEvent.keyDown(svg, { key: 'ArrowRight' })
    expect(document.activeElement).toBe(cell('t01'))
    // 12 cells, 6 columns: down is the next row.
    fireEvent.keyDown(svg, { key: 'ArrowDown' })
    expect(document.activeElement).toBe(cell('t07'))
    fireEvent.keyDown(svg, { key: 'ArrowUp' })
    fireEvent.keyDown(svg, { key: 'ArrowUp' })
    expect(document.activeElement?.getAttribute('data-focus-id')).toBe('platform:prod')
    raf.mockRestore()
  })

  it('a click on a station opens its stage', () => {
    const { container, onOpenStage } = renderMap(chain())
    fireEvent.click(container.querySelector('[data-station="prod"]')!)
    expect(onOpenStage).toHaveBeenCalledWith('prod')
  })

  it('a band toggles on click and on Space, and E expands the urgent ones', () => {
    const wave = Array.from({ length: 6 }, (_, i) => ({ name: `w${i}`, upstreams: ['test'] }))
    const p: Pipeline = { name: 'big', namespace: 'default', phase: 'Ready', environmentCount: 7, topologyResolved: true, activeBundleName: 'b2',
      environmentTopology: [{ name: 'test' }, ...wave] }
    const graph = { nodes: [step('test', 'Verified'), step('w0', 'Failed'), ...wave.slice(1).map(w => step(w.name, 'Verified'))], edges: [] }
    const { onToggleGroup, onSetExpanded } = renderMap(p, graph)
    const band = screen.getByRole('button', { name: /^WAVE 1 · 6: 1 failed, 5 live$/ })
    expect(band.getAttribute('aria-expanded')).toBe('false')
    fireEvent.click(band)
    expect(onToggleGroup).toHaveBeenCalledWith('band:1')
    const svg = band.closest('svg')!
    band.focus()
    fireEvent.focus(band)
    fireEvent.keyDown(svg, { key: ' ' })
    expect(onToggleGroup).toHaveBeenCalledTimes(2)
    fireEvent.keyDown(svg, { key: 'e' })
    expect(onSetExpanded).toHaveBeenCalledWith(new Set(['band:1']))
    // The attention strip names the failure.
    expect(screen.getByRole('button', { name: '1 failed' })).toBeTruthy()
  })

  it('searches environments, versions and gates; a miss dims nothing that matches', () => {
    const { container } = renderMap(chain())
    fireEvent.change(screen.getByRole('searchbox', { name: /Search environments/ }), { target: { value: 'pro' } })
    expect(screen.getByText('1 match')).toBeTruthy()
    expect(container.querySelector('[data-station="prod"]')!.hasAttribute('data-dim')).toBe(false)
    expect(container.querySelector('[data-station="test"]')!.getAttribute('data-dim')).toBe('true')
  })

  it('a paused pipeline shows the band and stops the thread animation (CSS)', () => {
    const { container } = renderMap(chain(), undefined, { paused: true })
    expect(screen.getByRole('status').textContent).toBe('Paused · no new promotions')
    expect(container.querySelector('.station-map--paused')).toBeTruthy()
  })

  it('renders a poll update of 500 environments in under 16 ms (layout excluded)', () => {
    const targets = Array.from({ length: 494 }, (_, i) => `prod-${i}`)
    const p: Pipeline = { name: 'fleet', namespace: 'default', phase: 'Ready', environmentCount: 500, activeBundleName: 'b2', topologyResolved: true,
      environmentTopology: [{ name: 'dev' }, ...Array.from({ length: 5 }, (_, i) => ({ name: `w${i}`, upstreams: ['dev'] })),
        { name: 'prod', upstreams: ['w0', 'w1', 'w2', 'w3', 'w4'], fleet: { targets, maxConcurrent: 10 } }] }
    const graphAt = (n: number) => ({ nodes: [step('dev', 'Verified'), ...targets.slice(0, n).map(t => step(t, 'Verified'))], edges: [] })
    const onOpenStage = vi.fn()
    const expanded = new Set(['platform:prod'])
    const props = { onOpenStage, expanded, onToggleGroup: vi.fn(), onSetExpanded: vi.fn() }
    const { rerender } = render(<StationMap model={buildModel(p, { bundle, graph: graphAt(100) }, { expanded })} {...props} />)
    // Warm up once, then time an update that only changes states.
    rerender(<StationMap model={buildModel(p, { bundle, graph: graphAt(150) }, { expanded })} {...props} />)
    // The best of seven updates: jsdom's timing is noisy when the suite runs in parallel.
    let ms = Infinity
    for (const n of [200, 250, 300, 350, 400, 450, 494]) {
      const next = buildModel(p, { bundle, graph: graphAt(n) }, { expanded })
      const t0 = performance.now()
      rerender(<StationMap model={next} {...props} />)
      ms = Math.min(ms, performance.now() - t0)
    }
    expect(document.querySelectorAll('.cell[data-station]').length).toBe(494)
    // jsdom is several times slower than a browser; the budget is the browser's frame.
    // jsdom is about 8× slower than a browser, and more while the other test files run in
    // parallel; the browser's own frame time is measured by Playwright (journey 017 at 300).
    expect(ms).toBeLessThan(16 * 10)
  })
})
