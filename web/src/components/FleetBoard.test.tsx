// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

import { describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, within } from '@testing-library/react'
import { FleetBoard } from './FleetBoard'
import type { Pipeline } from '../types'

const now = Date.parse('2026-10-01T12:00:00Z')
const linear = [{ name: 'test' }, { name: 'prod', upstreams: ['test'] }]

const pipelines: Pipeline[] = [
  {
    name: 'payments', namespace: 'team-b', phase: 'Ready', environmentCount: 2,
    activeBundleName: 'payments-7', activeBundleVersion: '3.1.0', environmentTopology: linear,
    environmentStates: { test: 'Verified', prod: 'WaitingForMerge' },
    deployed: {
      test: { bundle: 'payments-7', version: '3.1.0', verifiedAt: '2026-10-01T11:00:00Z' },
      prod: { bundle: 'payments-6', version: '3.0.9', verifiedAt: '2026-09-29T12:00:00Z' },
    },
  },
  {
    name: 'search', namespace: 'team-a', phase: 'Degraded', environmentCount: 2, failedStepCount: 1, paused: true,
    activeBundleName: 'search-2', activeBundleVersion: '0.2.0', environmentTopology: linear,
    environmentStates: { test: 'Failed', prod: 'Pending' },
  },
  { name: 'fresh', namespace: 'team-a', phase: 'Ready', environmentCount: 2, environmentTopology: linear },
]

describe('FleetBoard', () => {
  it('shows what each environment runs and where releases are', () => {
    render(<FleetBoard pipelines={pipelines} total={3} onSelect={() => {}} now={now} />)
    expect(screen.getByRole('heading', { level: 2, name: 'Fleet' })).toBeInTheDocument()
    expect(screen.getByText('3 pipelines')).toBeInTheDocument()
    expect(screen.getByText('1 moving')).toBeInTheDocument()
    expect(screen.getByText('1 needs attention')).toBeInTheDocument()

    const prod = screen.getByRole('button', { name: /^payments prod:/ })
    expect(prod).toHaveAttribute('data-state', 'arriving')
    expect(within(prod).getByText('3.0.9')).toBeInTheDocument()
    expect(within(prod).getByText('waiting for merge')).toBeInTheDocument()
    const test = screen.getByRole('button', { name: /^payments test:/ })
    expect(within(test).getByText('verified 1h ago')).toBeInTheDocument()
    // The release on its way sits on the rail into prod.
    expect(screen.getByText('3.1.0', { selector: '.fleet-rail__version' })).toBeInTheDocument()

    const failed = screen.getByRole('button', { name: /^search test:/ })
    expect(failed).toHaveAttribute('data-state', 'failed')
    expect(screen.getByText('paused')).toBeInTheDocument()
    expect(screen.getByText('1 failed')).toBeInTheDocument()

    const empty = screen.getByRole('button', { name: /^fresh prod:/ })
    expect(empty).toHaveAttribute('data-state', 'empty')
    expect(within(empty).getByText('nothing deployed')).toBeInTheDocument()
  })

  it('shows the config an image Bundle runs with, as kardinal status does (#1353)', () => {
    const p: Pipeline = {
      name: 'mixed', namespace: 'team-c', phase: 'Ready', environmentCount: 1, environmentTopology: [{ name: 'test' }],
      deployed: { test: { bundle: 'mixed-img', version: '1.4.0', configFrom: 'mixed-cfg', configVersion: 'config abcdef0' } },
    }
    render(<FleetBoard pipelines={[p]} total={1} onSelect={() => {}} now={now} />)
    const st = screen.getByRole('button', { name: /^mixed test: 1\.4\.0, with config abcdef0 from mixed-cfg,/ })
    expect(within(st).getByText('+ config abcdef0')).toHaveAttribute('title', 'from mixed-cfg')
  })

  it('lists pipelines by namespace, then name', () => {
    render(<FleetBoard pipelines={pipelines} total={3} onSelect={() => {}} now={now} />)
    const names = screen.getAllByRole('listitem').map(li => li.querySelector('.fleet-line__name')?.textContent)
    expect(names).toEqual(['fresh', 'search', 'payments'])
  })

  it('opens a pipeline from its name or a station', () => {
    const onSelect = vi.fn()
    render(<FleetBoard pipelines={pipelines} total={3} onSelect={onSelect} now={now} />)
    fireEvent.click(screen.getByRole('button', { name: 'payments' }))
    fireEvent.click(screen.getByRole('button', { name: /^search prod:/ }))
    expect(onSelect.mock.calls).toEqual([['payments', 'team-b'], ['search', 'team-a']])
  })

  it('says when the sidebar filter hides everything', () => {
    render(<FleetBoard pipelines={[]} total={3} onSelect={() => {}} now={now} />)
    expect(screen.getByText('0 of 3 pipelines')).toBeInTheDocument()
    expect(screen.getByText(/No pipeline matches this filter/)).toBeInTheDocument()
  })
})

describe('FleetBoard: fleet environments (D1)', () => {
  const targets = Array.from({ length: 50 }, (_, i) => `prod-c${String(i + 1).padStart(2, '0')}`)
  const states: Record<string, string> = { test: 'Verified' }
  targets.slice(0, 20).forEach(t => { states[t] = 'Verified' })
  targets.slice(20, 25).forEach(t => { states[t] = 'Promoting' })
  const p: Pipeline = {
    name: 'edge', namespace: 'team-f', phase: 'Ready', environmentCount: 2,
    activeBundleName: 'edge-9', activeBundleVersion: '9.0.0',
    environmentTopology: [{ name: 'test' }, { name: 'prod', upstreams: ['test'], fleet: { targets, maxConcurrent: 5 } }],
    environmentStates: states,
  }

  it('draws a 50-target fleet as one station with its progress', () => {
    render(<FleetBoard pipelines={[p]} total={1} onSelect={() => {}} now={now} />)
    const prod = screen.getByRole('button', { name: /^edge prod:/ })
    expect(prod).toHaveAttribute('data-state', 'arriving')
    expect(prod).toHaveAttribute('data-fleet', 'true')
    expect(within(prod).getByText('20/50 verified, 5 in flight (max 5)')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /^edge prod-c01/ })).not.toBeInTheDocument()
  })

  it('says when maxUnavailable stopped the rollout', () => {
    const stopped: Pipeline = {
      ...p,
      environmentTopology: [{ name: 'test' }, { name: 'prod', upstreams: ['test'], fleet: { targets, maxConcurrent: 5, maxUnavailable: 2 } }],
      environmentStates: { ...states, 'prod-c21': 'Failed', 'prod-c22': 'Failed' },
    }
    render(<FleetBoard pipelines={[stopped]} total={1} onSelect={() => {}} now={now} />)
    const prod = screen.getByRole('button', { name: /^edge prod:/ })
    expect(prod).toHaveAttribute('data-state', 'failed')
    expect(within(prod).getByText('stopped: 2 failed (max 2)')).toBeInTheDocument()
  })
})
