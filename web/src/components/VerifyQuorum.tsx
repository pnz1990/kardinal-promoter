// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// components/VerifyQuorum.tsx — a verify gate's quorum and its verdicts
// (docs/agentic/verify-with-agents.md): one pip per passing vote the gate
// needs, filled for each counted pass; every verdict with its verifier,
// result, run, whether it counted (and why not), summary, findings and
// evidence link; and the kardinal verify command. The verifier is an
// ActorBadge and the result a chip; an inconclusive one says what it does
// (#1908). It uses ApprovalQuorum's styles: the two are the same kind of
// evidence.

import type { GateVerdict, GateVerify } from '../types'
import { actorOfRef } from '../actor'
import { ActorBadge } from '../ui/ActorBadge'
import { StatusChip } from '../ui/Chips'
import { verdictChip, verifierRule, verifyState, verifySummary } from './verifyModel'
import CopyButton from './CopyButton'
import { formatRelativeAge } from './approvalAge'
import '../styles/ApprovalQuorum.css'

/** Pips drawn; a larger quorum is shown as text only. */
const MAX_PIPS = 10

interface Props {
  verify: GateVerify
  pipeline?: string
  namespace?: string
  now?: number
}

function VerdictRow({ d, now, onInconclusive }: { d: GateVerdict; now: number; onInconclusive: string }) {
  const age = d.createdAt ? formatRelativeAge(d.createdAt, now) : ''
  const who = actorOfRef(d.verifierActor)
  const chip = verdictChip(d.result, onInconclusive)
  const findings = d.findings
    ? `${d.findings} ${d.findings === 1 ? 'finding' : 'findings'}${d.highFindings ? `, ${d.highFindings} high` : ''}`
    : ''
  return (
    <li className="approval-decision" data-decision={d.result} data-counted={d.counted}>
      {who ? <ActorBadge actor={who} className="approval-decision__user" /> : <span className="approval-decision__user">{d.verifier}</span>}
      <StatusChip compact tone={chip.tone} label={chip.label} glyph={chip.glyph} />
      {d.run ? <span className="approval-decision__age">run {d.run}, attempt {d.attempt ?? 1}</span> : null}
      {age && <span className="approval-decision__age">{age}</span>}
      {!d.counted && d.reason && <span className="approval-decision__why">{d.reason}</span>}
      {findings && <span className="approval-decision__why" data-high={d.highFindings ? true : undefined}>{findings}</span>}
      {d.evidenceURL && /^https?:\/\//.test(d.evidenceURL) && (
        <a className="approval-decision__why" href={d.evidenceURL} target="_blank" rel="noreferrer noopener"
          aria-label={`Evidence from ${who?.name ?? d.verifier} (opens in a new tab)`}>evidence</a>
      )}
      {d.summary && <q className="approval-decision__comment">{d.summary}</q>}
    </li>
  )
}

export function VerifyQuorum({ verify: v, pipeline, namespace, now = Date.now() }: Props) {
  const pips = v.required <= MAX_PIPS ? v.required : 0
  const state = verifyState(v)
  const ns = namespace && namespace !== 'default' ? ` -n ${namespace}` : ''
  const command = pipeline ? `kardinal verify ${pipeline} ${v.environment} --check ${v.check} --pass --summary "..."${ns}` : ''
  const verdicts = v.verdicts ?? []
  return (
    <div className="approval-quorum" data-state={state === 'verified' ? 'approved' : state === 'failed' ? 'rejected' : 'waiting'}>
      <div className="approval-quorum__head">
        <span
          className="approval-quorum__meter"
          role="meter"
          aria-label="Verdicts"
          aria-valuemin={0}
          aria-valuemax={v.required}
          aria-valuenow={Math.min(v.passed, v.required)}
          aria-valuetext={verifySummary(v)}
        >
          {Array.from({ length: pips }, (_, i) => (
            <span key={i} className="approval-quorum__pip" data-filled={i < v.passed || undefined} aria-hidden="true" />
          ))}
        </span>
        <span className="approval-quorum__summary">{verifySummary(v)}</span>
      </div>
      <div className="approval-quorum__rule">{verifierRule(v)}</div>
      {verdicts.length > 0 ? (
        <ul className="approval-quorum__decisions" aria-label="Verdicts">
          {verdicts.map(d => <VerdictRow key={d.name} d={d} now={now} onInconclusive={v.onInconclusive} />)}
        </ul>
      ) : (
        <div className="approval-quorum__none">No verdicts yet.</div>
      )}
      {command && state === 'waiting' && (
        <div className="approval-quorum__how">
          <span>Record a verdict from the CLI:</span>
          <code>{command}</code>
          <CopyButton text={command} title="Copy the verify command" />
        </div>
      )}
    </div>
  )
}
