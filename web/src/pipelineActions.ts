// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// pipelineActions.ts — when the UI offers Promote and Roll back for an
// environment. The pipeline lane and the node detail panel both use these, so
// the two never disagree about which actions a step has.
import type { GraphEdge, GraphNode } from './types'

/** States where a promotion can be started again for the environment. */
const PROMOTABLE_STATES = new Set(['NotStarted', 'Failed', 'AbortedByAlarm'])

/**
 * The PromotionStep nodes directly upstream of node, looking through PolicyGate
 * (and any other non-step) nodes between them.
 */
export function upstreamSteps(node: GraphNode, nodes: GraphNode[], edges: GraphEdge[]): GraphNode[] {
  const byId = new Map(nodes.map(n => [n.id, n]))
  const found = new Map<string, GraphNode>()
  const seen = new Set<string>([node.id])
  const queue = [node.id]
  while (queue.length > 0) {
    const id = queue.shift()!
    for (const e of edges) {
      if (e.to !== id || seen.has(e.from)) continue
      seen.add(e.from)
      const from = byId.get(e.from)
      if (!from) continue
      if (from.type === 'PromotionStep') found.set(from.id, from)
      else queue.push(from.id)
    }
  }
  return [...found.values()]
}

/**
 * True when a new promotion to node's environment makes sense right now: the
 * environment is not reached yet, failed, or was stopped by an alarm, and every
 * upstream environment is Verified. The first environment has nothing upstream,
 * so it gets no Promote (CI starts it by creating a bundle).
 */
export function canPromote(node: GraphNode, nodes: GraphNode[], edges: GraphEdge[]): boolean {
  if (node.type !== 'PromotionStep' || !PROMOTABLE_STATES.has(node.state)) return false
  const upstream = upstreamSteps(node, nodes, edges)
  return upstream.length > 0 && upstream.every(n => n.state === 'Verified')
}

/** True when node's environment runs a verified version that can be rolled back. */
export function canRollback(node: GraphNode): boolean {
  return node.type === 'PromotionStep' && node.state === 'Verified'
}
