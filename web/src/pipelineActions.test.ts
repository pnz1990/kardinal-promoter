// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// pipelineActions.test.ts — the one rule for when Promote and Roll back show,
// used by the pipeline lane and the node detail panel (C10b-web-08).
import { describe, it, expect } from 'vitest'
import type { GraphEdge, GraphNode } from './types'
import { canPromote, canRollback, upstreamSteps } from './pipelineActions'

const step = (env: string, state: string): GraphNode => ({
  id: `step-${env}`, type: 'PromotionStep', label: env, environment: env, state,
})

// test → uat → gate → prod (the shape Go sends: gates sit between steps).
function lane(states: { test: string; uat: string; prod: string }) {
  const nodes: GraphNode[] = [
    step('test', states.test),
    step('uat', states.uat),
    { id: 'gate-prod', type: 'PolicyGate', label: 'no-weekend', environment: 'prod', state: 'Pass' },
    step('prod', states.prod),
  ]
  const edges: GraphEdge[] = [
    { from: 'step-test', to: 'step-uat' },
    { from: 'step-uat', to: 'gate-prod' },
    { from: 'gate-prod', to: 'step-prod' },
  ]
  return { nodes, edges }
}

// test → eu and test → us → prod: prod waits on both regions.
function fanIn(eu: string, us: string, prod = 'NotStarted') {
  const nodes = [step('test', 'Verified'), step('eu', eu), step('us', us), step('prod', prod)]
  const edges: GraphEdge[] = [
    { from: 'step-test', to: 'step-eu' },
    { from: 'step-test', to: 'step-us' },
    { from: 'step-eu', to: 'step-prod' },
    { from: 'step-us', to: 'step-prod' },
  ]
  return { nodes, edges }
}

describe('upstreamSteps', () => {
  it.each([
    { name: 'looks through the gate', env: 'prod', want: ['step-uat'] },
    { name: 'direct parent', env: 'uat', want: ['step-test'] },
    { name: 'first environment', env: 'test', want: [] },
  ])('$name', ({ env, want }) => {
    const { nodes, edges } = lane({ test: 'Verified', uat: 'Verified', prod: 'NotStarted' })
    const node = nodes.find(n => n.id === `step-${env}`)!
    expect(upstreamSteps(node, nodes, edges).map(n => n.id)).toEqual(want)
  })
})

describe('canPromote', () => {
  it.each([
    { name: 'not reached yet, upstream verified (through a gate)', env: 'prod', states: { test: 'Verified', uat: 'Verified', prod: 'NotStarted' }, want: true },
    { name: 'failed, upstream verified', env: 'prod', states: { test: 'Verified', uat: 'Verified', prod: 'Failed' }, want: true },
    { name: 'stopped by an alarm, upstream verified', env: 'uat', states: { test: 'Verified', uat: 'AbortedByAlarm', prod: 'NotStarted' }, want: true },
    { name: 'not reached yet, upstream still promoting', env: 'prod', states: { test: 'Verified', uat: 'Promoting', prod: 'NotStarted' }, want: false },
    { name: 'not reached yet, upstream failed', env: 'prod', states: { test: 'Verified', uat: 'Failed', prod: 'NotStarted' }, want: false },
    { name: 'in flight', env: 'uat', states: { test: 'Verified', uat: 'Promoting', prod: 'NotStarted' }, want: false },
    { name: 'waiting for merge', env: 'uat', states: { test: 'Verified', uat: 'WaitingForMerge', prod: 'NotStarted' }, want: false },
    { name: 'health checking', env: 'uat', states: { test: 'Verified', uat: 'HealthChecking', prod: 'NotStarted' }, want: false },
    { name: 'rolling back', env: 'uat', states: { test: 'Verified', uat: 'RollingBack', prod: 'NotStarted' }, want: false },
    { name: 'already verified', env: 'uat', states: { test: 'Verified', uat: 'Verified', prod: 'NotStarted' }, want: false },
    { name: 'first environment (nothing upstream)', env: 'test', states: { test: 'Failed', uat: 'NotStarted', prod: 'NotStarted' }, want: false },
  ])('$name → $want', ({ env, states, want }) => {
    const { nodes, edges } = lane(states)
    const node = nodes.find(n => n.id === `step-${env}`)!
    expect(canPromote(node, nodes, edges)).toBe(want)
  })

  it.each([
    { name: 'both regions verified', eu: 'Verified', us: 'Verified', want: true },
    { name: 'one region still promoting', eu: 'Verified', us: 'Promoting', want: false },
  ])('fan-in: $name → $want', ({ eu, us, want }) => {
    const { nodes, edges } = fanIn(eu, us)
    expect(canPromote(nodes.find(n => n.id === 'step-prod')!, nodes, edges)).toBe(want)
  })

  it('is false without the graph (no way to know the upstream state)', () => {
    expect(canPromote(step('prod', 'NotStarted'), [], [])).toBe(false)
  })

  it('is false for a gate', () => {
    const { nodes, edges } = lane({ test: 'Verified', uat: 'Verified', prod: 'NotStarted' })
    const gate = { ...nodes.find(n => n.id === 'gate-prod')!, state: 'NotStarted' }
    expect(canPromote(gate, nodes, edges)).toBe(false)
  })
})

describe('canRollback', () => {
  it.each([
    { name: 'verified step', node: step('prod', 'Verified'), want: true },
    { name: 'promoting step', node: step('prod', 'Promoting'), want: false },
    { name: 'waiting for merge', node: step('prod', 'WaitingForMerge'), want: false },
    { name: 'health checking', node: step('prod', 'HealthChecking'), want: false },
    { name: 'already rolling back', node: step('prod', 'RollingBack'), want: false },
    { name: 'failed step', node: step('prod', 'Failed'), want: false },
    { name: 'not reached yet', node: step('prod', 'NotStarted'), want: false },
    { name: 'gate', node: { id: 'gate-prod', type: 'PolicyGate', label: 'g', environment: 'prod', state: 'Verified' } as GraphNode, want: false },
  ])('$name → $want', ({ node, want }) => {
    expect(canRollback(node)).toBe(want)
  })
})
