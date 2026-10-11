// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// approvals/approvalsModel.ts — the approvals inbox (U7): every approval gate
// still collecting its quorum, from /gates, split into those waiting on the
// person at the UI and those waiting on someone else (and why). Oldest wait
// first: the one waiting longest is the one to clear first.
import type { Identity } from '../access/access'
import { mayDecide } from '../access/actionAccess'
import { awaitingQuorum, gateLabel } from '../stage/stageModel'
import type { Pipeline, PolicyGate } from '../types'

export interface ApprovalItem {
  gate: PolicyGate
  /** The gate as the user named it. */
  gateName: string
  pipeline: string
  namespace: string
  env: string
  bundle: string
  /** The version as people know it (the tag), else the Bundle name. */
  version: string
  /** RFC 3339: since when the environment waits on the gate. */
  waitingSince?: string
  approved: number
  required: number
  /** Why it waits on someone else, when it does. */
  notMine?: string
}

/** Who may decide, in words, for a gate that limits it. */
function whoDecides(g: PolicyGate): string {
  const a = g.approval
  const names = [...(a?.allowedGroups ?? []).map(x => `members of ${x}`), ...(a?.allowedUsers ?? [])]
  return names.join(' or ')
}

/** Why gate waits on someone other than identity, or undefined when it waits on them. */
export function notMine(g: PolicyGate, identity: Identity | undefined): string | undefined {
  if (!identity) return undefined
  const mine = g.approval?.decisions?.find(d => d.user === identity.username)
  if (mine?.decision === 'approve') {
    return mine.counted ? 'You approved it; it needs someone else.' : `Your approval does not count${mine.reason ? `: ${mine.reason}` : ''}.`
  }
  if (!mayDecide(g, identity)) return `Only ${whoDecides(g)} can approve it.`
  return undefined
}

/** The approvals inbox: waiting on the caller, then waiting on others. */
export function approvalItems(gates: PolicyGate[], pipelines: Pipeline[], identity: Identity | undefined): {
  mine: ApprovalItem[]; others: ApprovalItem[]
} {
  const items: ApprovalItem[] = []
  for (const g of gates) {
    if (g.template || !g.holding || !g.approval || !awaitingQuorum(g) || !g.bundle || !g.environment) continue
    const p = pipelines.find(x => x.name === g.pipeline && x.namespace === g.namespace)
    const version = p?.activeBundleName === g.bundle && p.activeBundleVersion ? p.activeBundleVersion : g.bundle
    items.push({
      gate: g, gateName: gateLabel(g), pipeline: g.pipeline ?? '', namespace: g.namespace, env: g.environment, bundle: g.bundle,
      version, waitingSince: g.createdAt ?? g.lastEvaluatedAt, approved: g.approval.approved, required: g.approval.required,
      notMine: notMine(g, identity),
    })
  }
  items.sort((a, b) => (a.waitingSince ?? '').localeCompare(b.waitingSince ?? '') || a.pipeline.localeCompare(b.pipeline) || a.env.localeCompare(b.env))
  return { mine: items.filter(i => !i.notMine), others: items.filter(i => i.notMine) }
}

/** How many approvals wait on the caller (the nav badge). */
export function approvalsWaiting(gates: PolicyGate[], identity: Identity | undefined): number {
  return gates.filter(g => !g.template && g.holding && g.approval && awaitingQuorum(g) && g.bundle && !notMine(g, identity)).length
}
