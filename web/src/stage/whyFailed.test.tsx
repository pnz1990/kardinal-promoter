// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// whyFailed.test.tsx — a failed stage's drawer says why it failed, in the controller's
// words, above Roll back (stageModel failureMessage).
import { describe, it, expect, vi } from 'vitest'
import { render, screen, within } from '@testing-library/react'

vi.mock('../api/client', () => ({ api: { getStepEvents: vi.fn(async () => []), validateCEL: vi.fn(async () => ({ valid: true })) } }))

import { StageDrawer } from './StageDrawer'
import { failureMessage } from './stageModel'
import type { Bundle, Pipeline, PromotionStep } from '../types'

const pipeline: Pipeline = {
  name: 'search', namespace: 'shop', phase: 'Degraded', environmentCount: 2, activeBundleName: 'search-2',
  environmentTopology: [{ name: 'test' }, { name: 'prod', upstreams: ['test'] }],
  environmentStates: { test: 'Verified', prod: 'Failed' },
  deployed: { prod: { bundle: 'search-1', version: '4.0.3', verifiedAt: '2026-10-01T00:00:00Z' } },
}
const bundle = (name: string, tag: string, prod: string): Bundle => ({ name, namespace: 'shop', phase: 'Verified', type: 'image', pipeline: 'search',
  createdAt: '2026-10-01T00:00:00Z', images: [{ repository: 'r', tag }], environments: [{ name: 'prod', phase: prod }] })
const bundles = [bundle('search-1', '4.0.3', 'Verified'), { ...bundle('search-2', '4.1.0', 'Failed'), phase: 'Failed', createdAt: '2026-10-05T00:00:00Z' }]
const step = (over: Partial<PromotionStep>): PromotionStep => ({ name: 'search-2-prod', namespace: 'shop', pipeline: 'search', bundle: 'search-2',
  environment: 'prod', stepType: 'auto', state: 'Failed', ...over })

describe('failureMessage', () => {
  it.each<[string, Partial<PromotionStep>, string | undefined]>([
    ['the step message', { message: 'health check timed out' }, 'health check timed out'],
    ['the failed sub-step', { steps: [{ name: 'git-push', state: 'Failed', message: 'push rejected' }] }, 'push rejected'],
    ['the Ready condition', { conditions: [{ type: 'Ready', status: 'False', message: '0/3 replicas ready' }] }, '0/3 replicas ready'],
    ['nothing', {}, undefined],
  ])('reads %s', (_, over, want) => {
    expect(failureMessage(step(over))).toBe(want)
  })
})

describe('a failed stage', () => {
  it('says "Why it failed" with the failure message above Roll back', () => {
    render(<StageDrawer namespace="shop" pipeline="search" env="prod" onClose={vi.fn()} onNavigate={vi.fn()} onChanged={vi.fn()}
      data={{ pipeline, bundles, steps: [step({ message: 'health check: deployment search-prod has 0/3 ready replicas after 10m' })], gates: [] }} />)
    const why = screen.getByRole('region', { name: 'Why it failed' })
    const message = within(why).getByText(/0\/3 ready replicas/)
    const rollback = within(why).getByRole('button', { name: 'Roll back' })
    expect(message.compareDocumentPosition(rollback) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })
})
