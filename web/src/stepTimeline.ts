// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// stepTimeline.ts — where each entry of a PromotionStep's status.steps sits in
// time, for the waterfall bars in NodeDetail: offset and width as percentages
// of the span from the first start to the last end (now for a step that is
// still running).

import type { StepStatus } from './types'

export interface StepBar {
  /** Percent of the span before the step started. */
  offset: number
  /** Percent of the span the step took; at least a sliver so it stays visible. */
  width: number
  running: boolean
}

const MIN_WIDTH = 0.8

/** One bar per step, or null for a step that has not started (or bad times). */
export function stepTimeline(steps: StepStatus[], now: number = Date.now()): (StepBar | null)[] {
  const times = steps.map(s => {
    const start = s.startedAt ? Date.parse(s.startedAt) : NaN
    if (isNaN(start)) return null
    const running = s.state === 'InProgress' && !s.completedAt
    let end = s.completedAt ? Date.parse(s.completedAt) : running ? now : NaN
    if (isNaN(end) && s.durationMs) end = start + s.durationMs
    if (isNaN(end) || end < start) end = start
    return { start, end, running }
  })
  const known = times.filter((t): t is NonNullable<typeof t> => t !== null)
  if (known.length === 0) return steps.map(() => null)
  const first = Math.min(...known.map(t => t.start))
  const span = Math.max(...known.map(t => t.end)) - first
  return times.map(t => {
    if (!t) return null
    if (span <= 0) return { offset: 0, width: 100, running: t.running }
    const offset = ((t.start - first) / span) * 100
    const width = Math.max(((t.end - t.start) / span) * 100, MIN_WIDTH)
    return { offset: Math.min(offset, 100 - width), width, running: t.running }
  })
}
