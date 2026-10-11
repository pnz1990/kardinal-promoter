// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// stage/useStageAccess.ts — which of the drawer's actions the person at the
// UI may take (U7). One POST /access per drawer (cached); an action they may
// not take stays visible but disabled, with the reason (the role that grants
// it, sign-in for an approval, or who may approve the gate).
import { useAccess } from '../access/access'
import { actionCheck, restriction } from '../access/actionAccess'
import type { AccessCheck, PolicyGate } from '../types'
import { gateLabel, type StageActionKind, type StageView } from './stageModel'

/** Why an action (for an optional gate) is not available, or undefined. */
export type ReasonFor = (kind: StageActionKind, gate?: PolicyGate) => string | undefined

export function useStageAccess(view: StageView | undefined, pipeline: string, namespace: string): ReasonFor {
  const approval = view?.gates.find(g => g.holding && g.approval)
  const plain = view?.gates.filter(g => g.holding && !g.approval) ?? []
  const ctx = { pipeline, bundle: view?.incoming?.bundle }
  const checks: AccessCheck[] = []
  for (const k of view?.actions ?? []) {
    if (k === 'override') {
      for (const g of plain) checks.push(actionCheck(k, { ...ctx, gate: g.name })!)
      continue
    }
    const c = actionCheck(k, ctx)
    if (c) checks.push(c)
  }
  const { can, identity } = useAccess(view ? namespace : undefined, checks)
  return (kind, gate) => {
    const g = gate ?? (kind === 'override' ? plain[0] : kind === 'approve' || kind === 'reject' ? approval : undefined)
    const c = actionCheck(kind, { ...ctx, gate: g?.name })
    return restriction(kind, c ? can(c) : undefined, identity, g?.approval ? g : undefined, g ? gateLabel(g) : undefined)
  }
}
