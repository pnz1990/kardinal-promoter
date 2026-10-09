// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// RejectedLiveBanner.test.tsx — the roll-back hint of a Rejected bundle whose
// change is live (QA #1489).

import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'
import { RejectedLiveBanner } from './RejectedLiveBanner'
import type { Bundle } from '../types'

const bundle = (phase: string, envs?: string[]): Bundle => ({
  name: 'app-v2', namespace: 'default', phase, type: 'image', pipeline: 'app', rejectedLiveEnvironments: envs,
})

describe('RejectedLiveBanner', () => {
  it('names the environments and the rollback for a live rejected change', () => {
    render(<RejectedLiveBanner bundle={bundle('Rejected', ['prod', 'uat'])} />)
    const banner = screen.getByRole('alert')
    expect(banner.textContent).toContain('Rejected change is live in prod, uat; roll back')
    expect(banner.textContent).toContain('kardinal rollback app --env prod')
    expect(banner.textContent).toContain('kardinal rollback app --env uat')
  })

  it.each([
    { name: 'rejected, not live', b: bundle('Rejected') },
    { name: 'rejected, empty list', b: bundle('Rejected', []) },
    { name: 'not rejected', b: bundle('Verified', ['prod']) },
  ])('$name: nothing', ({ b }) => {
    const { container } = render(<RejectedLiveBanner bundle={b} />)
    expect(container.textContent).toBe('')
  })
})
