// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

import { describe, expect, it } from 'vitest'
import { stepTimeline } from './stepTimeline'
import type { StepStatus } from './types'

const at = (s: number) => new Date(Date.UTC(2026, 9, 1, 9, 0, s)).toISOString()

describe('stepTimeline', () => {
  const cases: { name: string; steps: StepStatus[]; now?: number; want: ({ offset: number; width: number; running: boolean } | null)[] }[] = [
    {
      name: 'completed steps share the span by duration',
      steps: [
        { name: 'git-clone', state: 'Completed', startedAt: at(0), completedAt: at(10) },
        { name: 'git-push', state: 'Completed', startedAt: at(10), completedAt: at(40) },
      ],
      want: [{ offset: 0, width: 25, running: false }, { offset: 25, width: 75, running: false }],
    },
    {
      name: 'a running step runs to now; a pending one has no bar',
      steps: [
        { name: 'git-clone', state: 'Completed', startedAt: at(0), completedAt: at(10) },
        { name: 'health-check', state: 'InProgress', startedAt: at(10) },
        { name: 'next', state: 'Pending' },
      ],
      now: Date.parse(at(20)),
      want: [{ offset: 0, width: 50, running: false }, { offset: 50, width: 50, running: true }, null],
    },
    {
      name: 'durationMs stands in for a missing completedAt',
      steps: [
        { name: 'a', state: 'Completed', startedAt: at(0), durationMs: 5000 },
        { name: 'b', state: 'Completed', startedAt: at(5), completedAt: at(10) },
      ],
      want: [{ offset: 0, width: 50, running: false }, { offset: 50, width: 50, running: false }],
    },
    {
      name: 'a zero-length step keeps a sliver',
      steps: [
        { name: 'a', state: 'Completed', startedAt: at(0), completedAt: at(0) },
        { name: 'b', state: 'Completed', startedAt: at(0), completedAt: at(100) },
      ],
      want: [{ offset: 0, width: 0.8, running: false }, { offset: 0, width: 100, running: false }],
    },
    {
      name: 'everything in the same instant fills the track',
      steps: [{ name: 'a', state: 'Completed', startedAt: at(0), completedAt: at(0) }],
      want: [{ offset: 0, width: 100, running: false }],
    },
    { name: 'nothing started', steps: [{ name: 'a', state: 'Pending' }], want: [null] },
  ]
  for (const c of cases) {
    it(c.name, () => {
      expect(stepTimeline(c.steps, c.now)).toEqual(c.want)
    })
  }
})
