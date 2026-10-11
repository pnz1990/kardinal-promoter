// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// stationMap/summary.ts — the pipeline header's one sentence: where the
// followed version is ("1.4.2: live in test, uat; waiting for PR merge in prod").

import type { GraphResponse } from '../types'
import { displayState } from './display'

/** followSummaryOf names, for version, each state its steps are in, with their environments, in graph order. */
export function followSummaryOf(version: string | undefined, graph: GraphResponse | undefined): string {
  if (!version || !graph) return ''
  const byLabel = new Map<string, string[]>()
  for (const n of graph.nodes) {
    if (n.type !== 'PromotionStep' || !n.state || n.state === 'NotStarted') continue
    const label = lowerFirst(displayState('step', n.state).label)
    byLabel.set(label, [...(byLabel.get(label) ?? []), n.environment])
  }
  if (byLabel.size === 0) return `${version}: not started`
  const parts = [...byLabel].map(([label, envs]) => {
    const where = envs.length > 3 ? `${envs.length} environments` : envs.join(', ')
    return `${label} in ${where}`
  })
  return `${version}: ${parts.join('; ')}`
}

/** "Waiting for PR merge" → "waiting for PR merge": only a leading capital is lowered. */
function lowerFirst(s: string): string {
  return /^[A-Z][a-z]/.test(s) ? s[0].toLowerCase() + s.slice(1) : s
}
