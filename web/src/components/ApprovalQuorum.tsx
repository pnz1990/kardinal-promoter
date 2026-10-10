// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// components/ApprovalQuorum.tsx — an approval gate's quorum and who decided:
// one pip per approval the gate needs, filled for each counted approval;
// every decision with whether it counted (and why not), its comment and age;
// and how to decide: Approve, Reject and Revoke mine (ApprovalActions, for
// the user the UI authenticated) and the equivalent kardinal approve
// command.

import type { GateApproval, GateDecision } from '../types'
import CopyButton from './CopyButton'
import { ApprovalActions } from './ApprovalActions'
import { formatRelativeAge } from './approvalAge'
import '../styles/ApprovalQuorum.css'

/** Pips drawn; a larger quorum is shown as text only. */
const MAX_PIPS = 10

interface Props {
  approval: GateApproval
  bundle?: string
  environment?: string
  /** Namespace of the gate; with bundle and environment it enables the actions. */
  namespace?: string
  /** Called after a decision from the UI, so the parent can refresh. */
  onDecided?: () => void
  now?: number
}

/** What the gate's approvals amount to, in words. */
export function quorumSummary(a: GateApproval): string {
  const rejecter = a.decisions?.find(d => d.counted && d.decision === 'reject')
  if (a.rejected) return rejecter ? `Rejected by ${rejecter.user}` : 'Rejected'
  if (a.approved >= a.required) return `Approved (${a.approved} of ${a.required})`
  return `${a.approved} of ${a.required} ${a.required === 1 ? 'approval' : 'approvals'}`
}

/** Who may approve, in words. */
export function approverRule(a: GateApproval): string {
  const who: string[] = []
  if (a.allowedUsers?.length) who.push(a.allowedUsers.join(', '))
  if (a.allowedGroups?.length) who.push(`members of ${a.allowedGroups.join(', ')}`)
  const base = who.length ? `From ${who.join(' or ')}` : 'From anyone allowed to create Approvals'
  return a.excludeAuthor ? `${base}; not the Bundle's creator` : base
}

function DecisionRow({ d, now }: { d: GateDecision; now: number }) {
  const verb = d.decision === 'reject' ? 'rejected' : d.decision === 'approve' ? 'approved' : d.decision
  const age = d.firstSeenAt ? formatRelativeAge(d.firstSeenAt, now) : ''
  return (
    <li className="approval-decision" data-decision={d.decision} data-counted={d.counted}>
      <span className="approval-decision__user">{d.user}</span>
      <span className="approval-decision__verb">{verb}</span>
      {age && <span className="approval-decision__age">{age}</span>}
      {!d.counted && (
        <span className="approval-decision__why">not counted{d.reason ? `: ${d.reason}` : ''}</span>
      )}
      {d.comment && <q className="approval-decision__comment">{d.comment}</q>}
    </li>
  )
}

export function ApprovalQuorum({ approval: a, bundle, environment, namespace, onDecided, now = Date.now() }: Props) {
  const pips = a.required <= MAX_PIPS ? a.required : 0
  const state = a.rejected ? 'rejected' : a.approved >= a.required ? 'approved' : 'waiting'
  // The namespace makes the copied command work from any kubeconfig context.
  const command = bundle && environment
    ? `kardinal approve ${bundle} --env ${environment}${namespace ? ` -n ${namespace}` : ''}` : ''
  const decisions = a.decisions ?? []
  return (
    <div className="approval-quorum" data-state={state}>
      <div className="approval-quorum__head">
        <span
          className="approval-quorum__meter"
          role="meter"
          aria-label="Approvals"
          aria-valuemin={0}
          aria-valuemax={a.required}
          aria-valuenow={Math.min(a.approved, a.required)}
          aria-valuetext={quorumSummary(a)}
        >
          {Array.from({ length: pips }, (_, i) => (
            <span key={i} className="approval-quorum__pip" data-filled={i < a.approved || undefined} aria-hidden="true" />
          ))}
        </span>
        <span className="approval-quorum__summary">{quorumSummary(a)}</span>
      </div>
      <div className="approval-quorum__rule">{approverRule(a)}</div>
      {decisions.length > 0 ? (
        <ul className="approval-quorum__decisions" aria-label="Approval decisions">
          {decisions.map((d, i) => <DecisionRow key={`${d.user}-${i}`} d={d} now={now} />)}
        </ul>
      ) : (
        <div className="approval-quorum__none">No decisions yet.</div>
      )}
      {bundle && environment && namespace && (
        <ApprovalActions bundle={bundle} environment={environment} namespace={namespace} onDone={onDecided} />
      )}
      {command && state !== 'approved' && (
        <div className="approval-quorum__how">
          <span>Or from the CLI:</span>
          <code>{command}</code>
          <CopyButton text={command} title="Copy the approve command" />
        </div>
      )}
    </div>
  )
}
