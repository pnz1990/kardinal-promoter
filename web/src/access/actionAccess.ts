// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// access/actionAccess.ts — the permission each UI action needs, and why an
// action is not available to the person at the UI, in words they can act on.
import type { AccessCheck, PolicyGate } from '../types'
import type { StageActionKind } from '../stage/stageModel'
import type { Identity, Permission } from './access'

/** Every action the UI offers that changes something. */
export type UIAction = StageActionKind | 'ship'

/** The noun phrase of an action in a restriction ("Approving", "Rolling back"). */
const DOING: Record<UIAction, string> = {
  approve: 'Approving', reject: 'Declining', override: 'Overriding a gate', promote: 'Promoting', rollback: 'Rolling back',
  release: 'Releasing a hold', pause: 'Pausing the pipeline', resume: 'Resuming the pipeline', rejectVersion: 'Rejecting a version',
  openPR: 'Opening the pull request', ship: 'Shipping a version',
}

/** The check that decides whether action may be taken, or undefined when none does. */
export function actionCheck(action: UIAction, ctx: { pipeline?: string; gate?: string; bundle?: string }): AccessCheck | undefined {
  switch (action) {
    case 'approve':
    case 'reject': return { verb: 'create', resource: 'approvals' }
    case 'override': return { verb: 'update', resource: 'policygates', subresource: 'override', name: ctx.gate }
    case 'promote':
    case 'rollback':
    case 'ship': return { verb: 'create', resource: 'bundles' }
    case 'release': return { verb: 'update', resource: 'pipelines', subresource: 'hold', name: ctx.pipeline }
    case 'pause':
    case 'resume': return { verb: 'update', resource: 'pipelines', subresource: 'pause', name: ctx.pipeline }
    case 'rejectVersion': return { verb: 'update', resource: 'bundles', subresource: 'reject', name: ctx.bundle }
    case 'openPR': return undefined
  }
}

/** Whether user may decide on gate's approvals: its allowed users and groups (none listed: anyone with the role). */
export function mayDecide(gate: PolicyGate | undefined, user: { username: string; groups: string[] }): boolean {
  const a = gate?.approval
  if (!a) return true
  const users = a.allowedUsers ?? []
  const groups = a.allowedGroups ?? []
  if (users.length === 0 && groups.length === 0) return true
  return users.includes(user.username) || groups.some(g => user.groups.includes(g))
}

/** Who may decide on gate's approvals, in words ("release-managers", "alice or bob"). */
function deciders(gate: PolicyGate): string {
  const a = gate.approval
  const names = [...(a?.allowedGroups ?? []).map(g => `members of ${g}`), ...(a?.allowedUsers ?? [])]
  return names.length <= 1 ? names.join('') : `${names.slice(0, -1).join(', ')} or ${names[names.length - 1]}`
}

/**
 * Why action is not available, or undefined when it is. perm is the
 * controller's answer for actionCheck(action); identity who the caller is;
 * gate the approval gate an approve or decline is for.
 */
export function restriction(action: UIAction, perm: Permission | undefined, identity: Identity | undefined,
  gate?: PolicyGate, gateName?: string): string | undefined {
  const decides = action === 'approve' || action === 'reject'
  if (decides && identity === null) {
    return `${DOING[action]} from the UI names who decided, so it needs sign-in with your Kubernetes token (TokenReview mode). kardinal approve works without it.`
  }
  if (perm && perm.known && !perm.allowed) {
    if (perm.reason) return `${perm.reason.charAt(0).toUpperCase()}${perm.reason.slice(1)}.`
    return `${DOING[action]} needs the ${perm.grantedBy ?? 'matching'} role.`
  }
  if (decides && identity && gate && !mayDecide(gate, identity)) {
    return `Only ${deciders(gate)} can decide on ${gateName ?? gate.name}.`
  }
  return undefined
}
