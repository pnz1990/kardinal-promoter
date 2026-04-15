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

// components/PromotionErrorsPanel.test.tsx — Unit tests for #528 error aggregation.
import { describe, it, expect, vi } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import PromotionErrorsPanel, { groupStepErrors } from './PromotionErrorsPanel'
import type { PromotionStep } from '../types'

// ── Test fixtures ──────────────────────────────────────────────────────────

function makeStep(overrides: Partial<PromotionStep> = {}): PromotionStep {
  return {
    name: 'step-default',
    namespace: 'default',
    pipeline: 'my-pipeline',
    bundle: 'my-bundle',
    environment: 'test',
    stepType: 'promotion',
    state: 'Failed',
    message: 'git-push: authentication failed: 401 Unauthorized',
    ...overrides,
  }
}

const testStep = makeStep({ environment: 'test' })
const uatStep = makeStep({ environment: 'uat', name: 'step-uat' })
const prodStep = makeStep({
  environment: 'prod',
  name: 'step-prod',
  message: 'health-check: timeout after 300s',
})
const promotingStep = makeStep({ environment: 'staging', state: 'Promoting' }) // should NOT appear

// ── groupStepErrors unit tests ─────────────────────────────────────────────

describe('groupStepErrors', () => {
  it('returns empty array when no failed steps', () => {
    expect(groupStepErrors([promotingStep])).toHaveLength(0)
  })

  it('groups identical errors from different environments', () => {
    const groups = groupStepErrors([testStep, uatStep])
    expect(groups).toHaveLength(1)
    expect(groups[0].count).toBe(2)
    expect(groups[0].stepType).toBe('git-push')
    expect(groups[0].environments).toHaveLength(2)
  })

  it('creates separate groups for different errors', () => {
    const groups = groupStepErrors([testStep, uatStep, prodStep])
    expect(groups).toHaveLength(2)
    // Sorted by count desc
    expect(groups[0].count).toBe(2) // test + uat (git-push)
    expect(groups[1].count).toBe(1) // prod (health-check)
  })

  it('infers step type from message prefix', () => {
    const groups = groupStepErrors([testStep])
    expect(groups[0].stepType).toBe('git-push')
  })

  it('falls back to spec stepType when no known sub-step in message', () => {
    const step = makeStep({ message: 'unknown error occurred', stepType: 'rollback' })
    const groups = groupStepErrors([step])
    expect(groups[0].stepType).toBe('rollback')
  })

  it('filters out non-Failed steps', () => {
    expect(groupStepErrors([promotingStep])).toHaveLength(0)
    const verified = makeStep({ state: 'Verified' })
    expect(groupStepErrors([verified])).toHaveLength(0)
  })
})

// ── PromotionErrorsPanel component tests ──────────────────────────────────

describe('PromotionErrorsPanel', () => {
  it('renders nothing when no failed steps', () => {
    const { container } = render(<PromotionErrorsPanel steps={[promotingStep]} />)
    expect(container.firstChild).toBeNull()
  })

  it('renders the error panel when there are failed steps', () => {
    render(<PromotionErrorsPanel steps={[testStep, uatStep]} />)
    expect(screen.getByTestId('promotion-errors-panel')).toBeInTheDocument()
  })

  it('shows total failed count in header', () => {
    render(<PromotionErrorsPanel steps={[testStep, uatStep, prodStep]} />)
    expect(screen.getByText(/3 environments? failed/)).toBeInTheDocument()
  })

  it('shows pattern count in header', () => {
    render(<PromotionErrorsPanel steps={[testStep, uatStep, prodStep]} />)
    expect(screen.getByText(/2 distinct failure pattern/)).toBeInTheDocument()
  })

  it('shows step type badge for each group', () => {
    render(<PromotionErrorsPanel steps={[testStep, uatStep, prodStep]} />)
    const badges = screen.getAllByTestId('error-step-type')
    const types = badges.map(b => b.textContent)
    expect(types).toContain('git-push')
    expect(types).toContain('health-check')
  })

  it('shows affected count per group', () => {
    render(<PromotionErrorsPanel steps={[testStep, uatStep]} />)
    expect(screen.getByTestId('error-count')).toHaveTextContent('2×')
  })

  it('expands to show environment links when clicked', () => {
    render(<PromotionErrorsPanel steps={[testStep, uatStep]} />)
    const expandBtn = screen.getByTestId('expand-environments')
    fireEvent.click(expandBtn)
    const envLinks = screen.getAllByTestId('environment-link')
    expect(envLinks).toHaveLength(2)
    const envNames = envLinks.map(l => l.textContent)
    expect(envNames).toContain('test')
    expect(envNames).toContain('uat')
  })

  it('calls onSelectEnvironment when environment link is clicked', () => {
    const onSelect = vi.fn()
    render(<PromotionErrorsPanel steps={[testStep]} onSelectEnvironment={onSelect} />)
    const expandBtn = screen.getByTestId('expand-environments')
    fireEvent.click(expandBtn)
    const envLink = screen.getByTestId('environment-link')
    fireEvent.click(envLink)
    expect(onSelect).toHaveBeenCalledWith('test')
  })

  it('renders error groups sorted by count desc', () => {
    render(<PromotionErrorsPanel steps={[testStep, uatStep, prodStep]} />)
    const groups = screen.getAllByTestId('error-group')
    // First group should have git-push (count=2), second health-check (count=1)
    expect(groups).toHaveLength(2)
    const firstType = groups[0].querySelector('[data-testid="error-step-type"]')!.textContent
    expect(firstType).toBe('git-push')
  })
})
