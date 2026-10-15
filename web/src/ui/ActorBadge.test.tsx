// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

import { describe, expect, it } from 'vitest'
import { render } from '@testing-library/react'
import { ActorBadge } from './ActorBadge'
import { actorOfBundle, actorOfRef, actorOfUser, isAgent } from '../actor'
import type { Bundle } from '../types'

const bundle = (actor: Bundle['actor'], actorText?: string, actorAsserted?: string): Bundle =>
  ({ name: 'web-3', namespace: 'team', phase: 'Promoting', type: 'image', pipeline: 'web', actor, actorText, actorAsserted })

describe('actor views (#1908)', () => {
  it('a Bundle names its verified actor; one without a stamp names nobody', () => {
    expect(actorOfBundle(bundle({ kind: 'agent', principal: 'system:serviceaccount:ci:fixer', agent: 'fixer', sponsor: 'alice' },
      'agent fixer for alice', 'model m (asserted)'))).toEqual({ kind: 'agent', name: 'fixer', sponsor: 'alice', text: 'agent fixer for alice', asserted: 'model m (asserted)' })
    expect(actorOfBundle(bundle({ kind: 'human', principal: 'bob' }, 'bob via the UI'))).toMatchObject({ kind: 'human', name: 'bob' })
    expect(actorOfBundle(bundle({ kind: 'bot', principal: 'system:serviceaccount:ci:deployer' }, 'bot ci'))!.name).toBe('ci/deployer')
    expect(actorOfBundle(bundle({ kind: 'controller', principal: 'x', via: 'auto-rollback' }, 'kardinal (auto-rollback)'))!.name).toBe('kardinal')
    expect(actorOfBundle(bundle({ kind: 'unknown', principal: 'carol' }, 'unknown'))).toBeUndefined()
    expect(actorOfBundle(bundle(undefined))).toBeUndefined()
  })

  it('a bare user is a person, or a bot when it is a ServiceAccount', () => {
    expect(actorOfUser('alice')).toEqual({ kind: 'human', name: 'alice', text: 'alice' })
    expect(actorOfUser('system:serviceaccount:ci:bot')).toMatchObject({ kind: 'bot', name: 'ci/bot' })
    expect(actorOfUser(undefined)).toBeUndefined()
    expect(actorOfRef({ kind: 'agent', agent: 'fixer', sponsor: 'alice', text: 'agent fixer for alice' })).toMatchObject({ kind: 'agent', name: 'fixer' })
    expect(isAgent({ kind: 'agent' })).toBe(true)
    expect(isAgent(undefined)).toBe(false)
  })
})

describe('ActorBadge (#1908)', () => {
  it('an agent: its glyph, its name, for its sponsor, and the sentence with what it asserted in the title', () => {
    const { container } = render(<ActorBadge actor={{ kind: 'agent', name: 'fixer', sponsor: 'alice', text: 'agent fixer for alice', asserted: 'model m (asserted)' }} />)
    const el = container.querySelector('.k-actor')!
    expect(el).toHaveAttribute('data-actor-kind', 'agent')
    expect(el).toHaveTextContent('agent fixer for alice')
    expect(el).toHaveAttribute('title', 'agent fixer for alice · model m (asserted)')
    expect(el.querySelector('svg')).toHaveAttribute('aria-hidden', 'true')
  })

  it('compact leaves the sponsor to the title; a person has no kind word', () => {
    const { container, rerender } = render(<ActorBadge actor={{ kind: 'agent', name: 'fixer', sponsor: 'alice', text: 'agent fixer for alice' }} compact />)
    expect(container.querySelector('.k-actor')).toHaveTextContent(/^agent fixer$/)
    rerender(<ActorBadge actor={{ kind: 'human', name: 'bob', text: 'bob' }} />)
    expect(container.querySelector('.k-actor')).toHaveTextContent(/^bob$/)
  })
})
