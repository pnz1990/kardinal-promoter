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

describe('FleetBoard — keyboard', () => {
  it('each line is one Tab stop; arrow keys move along a line and between lines', async () => {
    const { default: userEvent } = await import('@testing-library/user-event')
    const user = userEvent.setup()
    const three = [{ name: 'test' }, { name: 'uat', upstreams: ['test'] }, { name: 'prod', upstreams: ['uat'] }]
    const ps: Pipeline[] = [
      { name: 'a', namespace: 'ns', phase: 'Ready', environmentCount: 3, environmentTopology: three },
      { name: 'b', namespace: 'ns', phase: 'Ready', environmentCount: 2, environmentTopology: linear },
    ]
    const onSelect = vi.fn()
    render(<FleetBoard pipelines={ps} total={2} onSelect={onSelect} now={now} />)
    const station = (p: string, env: string) => screen.getByRole('button', { name: new RegExp(`^${p} ${env}:`) })
    expect([station('a', 'test'), station('a', 'uat'), station('a', 'prod')].map(s => s.tabIndex)).toEqual([0, -1, -1])

    await user.click(screen.getByRole('button', { name: 'a' }))
    await user.tab()
    expect(station('a', 'test')).toHaveFocus()
    await user.keyboard('{ArrowRight}{ArrowRight}')
    expect(station('a', 'prod')).toHaveFocus()
    await user.keyboard('{ArrowDown}')
    expect(station('b', 'prod')).toHaveFocus() // the same position, or the last
    await user.keyboard('{ArrowUp}')
    expect(station('a', 'uat')).toHaveFocus() // the same position (b has two)
    await user.keyboard('{Home}')
    expect(station('a', 'test')).toHaveFocus()
    await user.tab()
    expect(screen.getByRole('button', { name: 'b' })).toHaveFocus() // past the line in one step
    await user.keyboard('{Shift>}{Tab}{/Shift}')
    expect(station('a', 'test')).toHaveFocus()
    await user.keyboard('{Enter}')
    expect(onSelect).toHaveBeenCalledWith('a', 'ns')
  })

  it('arrow keys with Alt, Ctrl or Meta are left to the browser and screen readers', async () => {
    const { default: userEvent } = await import('@testing-library/user-event')
    const user = userEvent.setup()
    const ps: Pipeline[] = [
      { name: 'a', namespace: 'ns', phase: 'Ready', environmentCount: 2, environmentTopology: linear },
      { name: 'b', namespace: 'ns', phase: 'Ready', environmentCount: 2, environmentTopology: linear },
    ]
    render(<FleetBoard pipelines={ps} total={2} onSelect={vi.fn()} now={now} />)
    const station = (p: string, env: string) => screen.getByRole('button', { name: new RegExp(`^${p} ${env}:`) })
    await user.click(screen.getByRole('button', { name: 'a' }))
    await user.tab()
    expect(station('a', 'test')).toHaveFocus()
    for (const mod of ['Alt', 'Control', 'Meta']) {
      for (const key of ['ArrowRight', 'ArrowDown', 'End']) {
        await user.keyboard(`{${mod}>}{${key}}{/${mod}}`)
        expect(station('a', 'test'), `${mod}+${key}`).toHaveFocus()
      }
    }
    await user.keyboard('{Shift>}{ArrowRight}{/Shift}')
    expect(station('a', 'prod'), 'Shift is not a command modifier').toHaveFocus()
  })

  it('draws a wave of many environments as one plate, not a column of stations (#1580)', () => {
    const envs = Array.from({ length: 149 }, (_, i) => `env-${String(i + 1).padStart(3, '0')}`)
    const p: Pipeline = {
      name: 'fleet', namespace: 'team-w', phase: 'Ready', environmentCount: 150,
      activeBundleName: 'fleet-2', activeBundleVersion: '2.0.0',
      environmentTopology: [{ name: 'env-000' }, ...envs.map(name => ({ name, upstreams: ['env-000'] }))],
      environmentStates: Object.fromEntries([['env-000', 'Verified'], ...envs.map((e, i) => [e, i < 100 ? 'Verified' : 'Promoting'])]),
      deployed: Object.fromEntries(['env-000', ...envs].map((e, i) => [e, { bundle: i <= 100 ? 'fleet-2' : 'fleet-1', version: i <= 100 ? '2.0.0' : '1.0.0' }])),
    }
    render(<FleetBoard pipelines={[p]} total={1} onSelect={() => {}} now={now} />)
    expect(screen.getAllByRole('button', { name: /^fleet / })).toHaveLength(2)
    const plate = screen.getByRole('button', { name: /^fleet env-001 to env-149, 149 environments:/ })
    expect(plate).toHaveAttribute('data-state', 'arriving')
    expect(plate).toHaveAccessibleName('fleet env-001 to env-149, 149 environments: 2.0.0 (+49 on others), 49 on its way, 100 settled')
    expect(within(plate).getByText('149 environments')).toBeInTheDocument()
    expect(within(plate).getByText('env-001 … env-149')).toBeInTheDocument()
  })
})
