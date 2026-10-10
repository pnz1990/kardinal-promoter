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

// components/ReleaseMetricsBar.test.tsx — release efficiency metrics (#465, C10b-web-10).
// Bundles use the shape the Go API returns: provenance.rollbackOf for rollbacks
// and status.environments[].healthCheckedAt for when an environment was verified.
import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'
import { cfrColor, computeReleaseMetrics, formatHours, formatMinutes, ReleaseMetricsBar } from './ReleaseMetricsBar'
import type { Bundle } from '../types'

const HOUR = 3_600_000
const created = Date.parse('2026-03-01T00:00:00Z')

/** A bundle created `ageDays` before the reference time that reached prod `ttpHours` after creation. */
function bundle(name: string, opts: { ageDays?: number; ttpHours?: number; rollbackOf?: string } = {}): Bundle {
  const c = created - (opts.ageDays ?? 0) * 24 * HOUR
  return {
    name,
    namespace: 'default',
    phase: 'Promoting', // Bundle phase does not reach Verified; the metrics must not depend on it.
    type: 'image',
    pipeline: 'my-app',
    createdAt: new Date(c).toISOString(),
    provenance: opts.rollbackOf ? { rollbackOf: opts.rollbackOf } : undefined,
    environments: [
      { name: 'test', phase: 'Verified', healthCheckedAt: new Date(c + 0.1 * HOUR).toISOString() },
      ...(opts.ttpHours !== undefined
        ? [{ name: 'prod', phase: 'Verified', healthCheckedAt: new Date(c + opts.ttpHours * HOUR).toISOString() }]
        : []),
    ],
  }
}

describe('computeReleaseMetrics', () => {
  it.each([
    { name: 'no final environment known', bundles: [bundle('a', { ttpHours: 1 })], env: undefined },
    { name: 'no bundle reached the final environment', bundles: [bundle('a'), bundle('b')], env: ['prod'] },
    { name: 'no bundles', bundles: [], env: ['prod'] },
  ])('returns null when $name', ({ bundles, env }) => {
    expect(computeReleaseMetrics(bundles, env)).toBeNull()
  })

  it('measures time to prod from healthCheckedAt, not from bundle age', () => {
    // 30-day-old bundles that reached prod 1h and 3h after creation.
    const m = computeReleaseMetrics([bundle('a', { ageDays: 30, ttpHours: 1 }), bundle('b', { ageDays: 31, ttpHours: 3 })], ['prod'])
    expect(m?.meanTtpHours).toBe(2)
  })

  it('counts rollbacks from provenance.rollbackOf', () => {
    const m = computeReleaseMetrics([
      bundle('v1', { ttpHours: 1 }),
      bundle('v2', { ttpHours: 1 }),
      bundle('v3'),
      bundle('v4', { rollbackOf: 'v3', ttpHours: 1 }),
      bundle('v5', { rollbackOf: 'v2' }),
    ], ['prod'])
    expect(m).toMatchObject({ totalBundles: 5, rollbackCount: 2, rollbackRatePct: 40 })
  })

  it('counts deploys that reached the final environment, not the window size', () => {
    const m = computeReleaseMetrics([bundle('a', { ttpHours: 1 }), bundle('b'), bundle('c'), bundle('d', { ttpHours: 2 })], ['prod'])
    expect(m).toMatchObject({ totalBundles: 4, deployCount: 2 })
  })

  it('covers the 10 newest bundles only', () => {
    const bundles = Array.from({ length: 12 }, (_, i) => bundle(`b${i}`, { ageDays: i, ttpHours: i < 10 ? 1 : 100 }))
    const m = computeReleaseMetrics(bundles, ['prod'])
    expect(m).toMatchObject({ totalBundles: 10, deployCount: 10, meanTtpHours: 1 })
  })

  it('counts a bundle once every final environment of a wave is verified, at the last of them', () => {
    const wave = (name: string, done: Record<string, number>): Bundle => ({
      name, namespace: 'default', phase: 'Promoting', type: 'image', pipeline: 'fleet',
      createdAt: new Date(created).toISOString(),
      environments: Object.entries(done).map(([env, h]) => (
        { name: env, phase: 'Verified', healthCheckedAt: new Date(created + h * HOUR).toISOString() })),
    })
    const finals = ['w1', 'w2', 'w3']
    const m = computeReleaseMetrics([
      wave('all', { w0: 0.1, w1: 1, w2: 4, w3: 2 }),
      wave('partial', { w0: 0.1, w1: 1, w3: 1 }),
    ], finals)
    expect(m).toMatchObject({ totalBundles: 2, deployCount: 1, meanTtpHours: 4 })
  })

  it('does not mutate the input array', () => {
    const bundles = [bundle('old', { ageDays: 2, ttpHours: 1 }), bundle('new', { ageDays: 1, ttpHours: 1 })]
    computeReleaseMetrics(bundles, ['prod'])
    expect(bundles.map(b => b.name)).toEqual(['old', 'new'])
  })
})

describe('formatHours', () => {
  it.each([
    { hours: 0.4, want: '< 1h' },
    { hours: 2.4, want: '2h' },
    { hours: 72, want: '3d' },
  ])('$hours → $want', ({ hours, want }) => {
    expect(formatHours(hours)).toBe(want)
  })
})

describe('ReleaseMetricsBar', () => {
  it('renders nothing until a bundle has reached the final environment', () => {
    const { container } = render(<ReleaseMetricsBar bundles={[bundle('a')]} finalEnvironments={['prod']} />)
    expect(container).toBeEmptyDOMElement()
  })

  it('shows the three metrics named after the final environment', () => {
    render(
      <ReleaseMetricsBar
        bundles={[bundle('a', { ttpHours: 2 }), bundle('b', { rollbackOf: 'a', ttpHours: 4 }), bundle('c')]}
        finalEnvironments={['prod']}
      />,
    )
    const bar = screen.getByRole('region', { name: 'Release metrics' })
    expect(bar).toHaveTextContent('Time to prod3h')
    expect(bar).toHaveTextContent('Rollback rate33%1 rollback')
    expect(bar).toHaveTextContent('Deploys to prod2last 3 bundles')
  })

  it('counts a final wave instead of naming one of its environments', () => {
    const b = bundle('a', { ttpHours: 2 })
    b.environments = [...(b.environments ?? []), { name: 'eu', phase: 'Verified', healthCheckedAt: b.environments![1].healthCheckedAt }]
    render(<ReleaseMetricsBar bundles={[b]} finalEnvironments={['prod', 'eu']} />)
    const bar = screen.getByRole('region', { name: 'Release metrics' })
    expect(bar).toHaveTextContent('Time to all 2 final envs2h')
    expect(bar).toHaveTextContent('Deploys to all 2 final envs1')
    expect(screen.getAllByTitle('Final environments: prod, eu')).toHaveLength(2)
  })

  it('adds the controller change failure rate and time to restore', () => {
    render(
      <ReleaseMetricsBar
        bundles={[bundle('a', { ttpHours: 2 })]}
        finalEnvironments={['prod']}
        deploymentMetrics={{ deployments: 4, failedDeployments: 1, changeFailureRateMillis: 250,
          meanTimeToRestoreMinutes: 95, restoredFailures: 1 }}
      />,
    )
    const bar = screen.getByRole('region', { name: 'Release metrics' })
    expect(bar).toHaveTextContent('Change failure rate25.0%1 of 4 deployments')
    expect(bar).toHaveTextContent('Time to restore1h35mmean of 1 restored')
  })

  it('shows a dash when no failure was restored, and no stability cells without deployments', () => {
    const { rerender } = render(
      <ReleaseMetricsBar
        bundles={[bundle('a', { ttpHours: 2 })]}
        finalEnvironments={['prod']}
        deploymentMetrics={{ deployments: 2, failedDeployments: 1, changeFailureRateMillis: 500 }}
      />,
    )
    const bar = screen.getByRole('region', { name: 'Release metrics' })
    expect(bar).toHaveTextContent('Time to restore—no restored failure')
    rerender(
      <ReleaseMetricsBar bundles={[bundle('a', { ttpHours: 2 })]} finalEnvironments={['prod']} deploymentMetrics={{ sampleSize: 1 }} />,
    )
    expect(screen.getByRole('region', { name: 'Release metrics' })).not.toHaveTextContent('Change failure rate')
  })
})

describe('formatMinutes and cfrColor', () => {
  it.each([[0, '0m'], [59, '59m'], [60, '1h'], [95, '1h35m'], [47 * 60, '47h'], [72 * 60, '3d']])('formatMinutes(%i) = %s', (m, want) => {
    expect(formatMinutes(m)).toBe(want)
  })
  it('colors the change failure rate by DORA band', () => {
    expect(cfrColor(0)).toBe('var(--color-success)')
    expect(cfrColor(150)).toBe('var(--color-success)')
    expect(cfrColor(300)).toBe('var(--color-warning)')
    expect(cfrColor(301)).toBe('var(--color-error)')
  })
})
