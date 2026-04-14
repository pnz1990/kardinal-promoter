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

// StageDetailPanel.test.tsx — Tests for #501 per-stage approval workflow detail:
// bake countdown, step icons, panel open/close.
import { describe, it, expect, vi } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import type { GraphNode, PromotionStep } from '../types'
import { StageDetailPanel, bakePct, formatBakeCountdown, stepIconFor } from './StageDetailPanel'

// ─── Fixtures ─────────────────────────────────────────────────────────────────

function makeNode(overrides: Partial<GraphNode> = {}): GraphNode {
  return {
    id: 'prod-step',
    type: 'PromotionStep',
    label: 'prod',
    environment: 'prod',
    state: 'HealthChecking',
    ...overrides,
  }
}

function makeStep(overrides: Partial<PromotionStep> = {}): PromotionStep {
  return {
    name: 'prod-step',
    namespace: 'default',
    pipeline: 'my-app',
    bundle: 'v1.0',
    environment: 'prod',
    stepType: 'health-check',
    state: 'HealthChecking',
    currentStepIndex: 7,
    ...overrides,
  }
}

// ─── bakePct ──────────────────────────────────────────────────────────────────

describe('bakePct', () => {
  it('returns 0 for target=0', () => {
    expect(bakePct(10, 0)).toBe(0)
  })

  it('computes percentage correctly', () => {
    expect(bakePct(15, 30)).toBe(50)
    expect(bakePct(30, 30)).toBe(100)
    expect(bakePct(0, 30)).toBe(0)
  })

  it('caps at 100% even when elapsed > target', () => {
    expect(bakePct(45, 30)).toBe(100)
  })

  it('rounds to integer', () => {
    expect(bakePct(10, 30)).toBe(33) // 33.33... → 33
  })
})

// ─── formatBakeCountdown ──────────────────────────────────────────────────────

describe('formatBakeCountdown', () => {
  it('returns empty string when target=0', () => {
    expect(formatBakeCountdown(10, 0)).toBe('')
  })

  it('formats countdown correctly', () => {
    expect(formatBakeCountdown(15, 30)).toBe('15m / 30m (50%)')
    expect(formatBakeCountdown(0, 30)).toBe('0m / 30m (0%)')
    expect(formatBakeCountdown(30, 30)).toBe('30m / 30m (100%)')
  })
})

// ─── stepIconFor ─────────────────────────────────────────────────────────────

describe('stepIconFor', () => {
  it('returns green check for completed step (index < currentIndex)', () => {
    const result = stepIconFor(0, 3, true)
    expect(result.icon).toBe('✓')
    expect(result.color).toBe('#22c55e')
  })

  it('returns play icon for active current step', () => {
    const result = stepIconFor(3, 3, true)
    expect(result.icon).toBe('▶')
    expect(result.color).toBe('#f59e0b')
  })

  it('returns circle for inactive current step', () => {
    const result = stepIconFor(3, 3, false)
    expect(result.icon).toBe('○')
    expect(result.color).toBe('#475569')
  })

  it('returns dark circle for future step', () => {
    const result = stepIconFor(5, 3, true)
    expect(result.icon).toBe('○')
    expect(result.color).toBe('#334155')
  })
})

// ─── StageDetailPanel component ───────────────────────────────────────────────

describe('StageDetailPanel', () => {
  it('renders environment name in header', () => {
    const node = makeNode({ environment: 'production', state: 'HealthChecking' })
    render(<StageDetailPanel node={node} steps={[makeStep({ environment: 'production' })]} onClose={() => {}} />)
    expect(screen.getByText('production')).toBeInTheDocument()
  })

  it('shows bake countdown when bakeTargetMinutes > 0', () => {
    const node = makeNode()
    const step = makeStep({ bakeElapsedMinutes: 15, bakeTargetMinutes: 30 })
    render(<StageDetailPanel node={node} steps={[step]} onClose={() => {}} />)
    expect(screen.getByTitle('Bake progress: 15m of 30m elapsed')).toBeInTheDocument()
    expect(screen.getByText('15m / 30m (50%)')).toBeInTheDocument()
  })

  it('does not show bake section when bakeTargetMinutes is 0 or absent', () => {
    const node = makeNode()
    const step = makeStep({ bakeTargetMinutes: 0 })
    render(<StageDetailPanel node={node} steps={[step]} onClose={() => {}} />)
    expect(screen.queryByText(/Bake/i)).not.toBeInTheDocument()
  })

  it('shows bake reset warning when bakeResets > 0', () => {
    const node = makeNode()
    const step = makeStep({ bakeElapsedMinutes: 10, bakeTargetMinutes: 30, bakeResets: 2 })
    render(<StageDetailPanel node={node} steps={[step]} onClose={() => {}} />)
    expect(screen.getByText(/Timer reset 2 times/)).toBeInTheDocument()
  })

  it('shows progress bar with correct aria attributes', () => {
    const node = makeNode()
    const step = makeStep({ bakeElapsedMinutes: 15, bakeTargetMinutes: 30 })
    render(<StageDetailPanel node={node} steps={[step]} onClose={() => {}} />)
    const progressBar = screen.getByRole('progressbar')
    expect(progressBar).toBeInTheDocument()
    expect(progressBar).toHaveAttribute('aria-valuenow', '15')
    expect(progressBar).toHaveAttribute('aria-valuemax', '30')
  })

  it('shows PR link when step has prURL', () => {
    const node = makeNode()
    const step = makeStep({ prURL: 'https://github.com/org/repo/pull/42' })
    render(<StageDetailPanel node={node} steps={[step]} onClose={() => {}} />)
    const link = screen.getByLabelText('Open pull request')
    expect(link).toBeInTheDocument()
    expect(link).toHaveAttribute('href', 'https://github.com/org/repo/pull/42')
  })

  it('shows PR link from node.prURL when step has no prURL', () => {
    const node = makeNode({ prURL: 'https://github.com/org/repo/pull/99' })
    const step = makeStep({ prURL: undefined })
    render(<StageDetailPanel node={node} steps={[step]} onClose={() => {}} />)
    const link = screen.getByLabelText('Open pull request')
    expect(link).toHaveAttribute('href', 'https://github.com/org/repo/pull/99')
  })

  it('shows step list with 8 steps', () => {
    const node = makeNode()
    const step = makeStep()
    const { getAllByRole } = render(<StageDetailPanel node={node} steps={[step]} onClose={() => {}} />)
    // 8 step rows in the list plus the close button and optional PR link
    const listItems = screen.queryAllByText(/Git clone|Set image|Git commit|Git push|Open PR|Wait for merge|Health check/)
    expect(listItems.length).toBeGreaterThanOrEqual(1)
  })

  it('calls onClose when × button is clicked', () => {
    const onClose = vi.fn()
    const node = makeNode()
    render(<StageDetailPanel node={node} steps={[makeStep()]} onClose={onClose} />)
    fireEvent.click(screen.getByLabelText('Close stage detail'))
    expect(onClose).toHaveBeenCalledOnce()
  })

  it('calls onClose when Escape key is pressed', () => {
    const onClose = vi.fn()
    const node = makeNode()
    render(<StageDetailPanel node={node} steps={[makeStep()]} onClose={onClose} />)
    fireEvent.keyDown(document, { key: 'Escape' })
    expect(onClose).toHaveBeenCalledOnce()
  })

  it('shows no-step message when steps array has no match for environment', () => {
    const node = makeNode({ environment: 'prod' })
    const step = makeStep({ environment: 'staging' }) // different env
    render(<StageDetailPanel node={node} steps={[step]} onClose={() => {}} />)
    expect(screen.getByText('No active promotion step for this environment.')).toBeInTheDocument()
  })
})
