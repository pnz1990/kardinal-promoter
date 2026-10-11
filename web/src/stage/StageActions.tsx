// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// stage/StageActions.tsx — the incident actions of one environment, each
// behind a confirmation dialog: approve or decline for an approval gate (with
// its quorum), override a gate, promote, roll back to a chosen version
// (toBundle, optionally held) and release a hold; and, under "Whole pipeline",
// pause or resume the pipeline and reject the version on its way in. Only the
// actions that make sense now are shown (stageView), and one of them is the
// primary. The button, the dialog's confirm button and the result message
// use the same verb.
import { useId, useState } from 'react'
import { api } from '../api/client'
import { actionErrorMessage } from '../components/ConfirmDialog'
import type { PolicyGate } from '../types'
import { overrideChoices } from './overrideLimits'
import { gateLabel, type StageActionKind, type StageView } from './stageModel'
import { StageDialog } from './StageDialog'

interface Props {
  view: StageView
  pipeline: string
  namespace: string
  /** The action to open (from a "why" reason or a button), or undefined. */
  active: StageActionKind | undefined
  /** The gate the action was opened for (a reason's gate), if any. */
  activeGate?: PolicyGate
  onOpen: (kind: StageActionKind | undefined, gate?: PolicyGate) => void
  /** Called after an action succeeds, so the page refreshes. */
  onChanged?: () => void
  /** Actions already offered next to their reason: not repeated in the row. */
  inline?: Set<StageActionKind>
  /** Only this environment's actions (the Table's row): no "Whole pipeline" group, no
   *  "nothing to do" note. */
  envOnly?: boolean
}

/** The button label of each action; the dialog and the result use the same verb. */
export const ACTION_LABEL: Record<StageActionKind, string> = {
  approve: 'Approve', reject: 'Decline', override: 'Override gate', promote: 'Promote', rollback: 'Roll back',
  release: 'Release hold', pause: 'Pause pipeline', resume: 'Resume pipeline', rejectVersion: 'Reject version',
  openPR: 'Open pull request',
}

/** This environment's actions, in the order the row shows them. */
const ENV_ORDER: StageActionKind[] = ['approve', 'promote', 'release', 'openPR', 'override', 'rollback', 'reject']
/** Actions on the whole pipeline or the version everywhere: quiet buttons, garnet only in their dialog. */
const PIPELINE_ORDER: StageActionKind[] = ['resume', 'pause', 'rejectVersion']
/** Actions that undo or refuse something here: garnet outline, never the thread's red. */
export const DESTRUCTIVE = new Set<StageActionKind>(['reject', 'rollback'])

export function StageActions({ view, pipeline, namespace, active, activeGate, onOpen, onChanged, inline, envOnly }: Props) {
  const [result, setResult] = useState<string>()
  const envKinds = ENV_ORDER.filter(k => view.actions.has(k) && !inline?.has(k))
  const primary = view.primary && envKinds.includes(view.primary) ? view.primary : undefined
  const ordered = primary ? [primary, ...envKinds.filter(k => k !== primary)] : envKinds
  const pipelineKinds = envOnly ? [] : PIPELINE_ORDER.filter(k => view.actions.has(k))
  const prURL = view.step?.prURL
  const button = (k: StageActionKind, style: 'primary' | 'secondary' | 'destructive' | 'quiet') => k === 'openPR' && prURL
    ? <a key={k} data-action={k} className={`stage-button stage-button--${style}`} href={prURL} target="_blank" rel="noreferrer">
        {ACTION_LABEL[k]}
      </a>
    : <button key={k} type="button" data-action={k} className={`stage-button stage-button--${style}`}
        onClick={() => { setResult(undefined); onOpen(k) }}>
        {ACTION_LABEL[k]}
      </button>
  return (
    <>
      {ordered.length > 0
        ? <div className="stage-actions">{ordered.map(k => button(k, k === primary ? 'primary' : DESTRUCTIVE.has(k) ? 'destructive' : 'secondary'))}</div>
        : envOnly ? null : <p className="stage-muted">{inline && inline.size > 0
            ? 'Use the button next to each reason above.'
            : `Nothing to do in ${view.env} right now.`}</p>}
      {pipelineKinds.length > 0 && (
        <div className="stage-pipeline-actions">
          <p className="stage-eyebrow">Whole pipeline</p>
          <div className="stage-actions">{pipelineKinds.map(k => button(k, 'quiet'))}</div>
        </div>
      )}
      {result && <p role="status" className="stage-result">{result}</p>}
      {active && active !== 'openPR' && (
        <ActionDialog kind={active} view={view} pipeline={pipeline} namespace={namespace} gate={activeGate}
          onCancel={() => onOpen(undefined)}
          onDone={msg => { setResult(msg); onOpen(undefined); onChanged?.() }} />
      )}
    </>
  )
}

/**
 * What a failed action says: how to fix it. A 403 carries the controller's
 * text, which names the permission and the role that grants it; a 409 the
 * conflict as the server put it.
 */
export function stageErrorMessage(action: string, err: unknown): string {
  const text = err instanceof Error ? err.message : String(err)
  const m = /^API error (\d+): ([\s\S]*)$/.exec(text)
  if (!m) return actionErrorMessage(action, err)
  const [, code, body] = m
  const detail = body.trim()
  switch (code) {
    case '403':
      return /role/.test(detail)
        ? `You are not allowed to ${action}: ${detail}`
        : `You are not allowed to ${action}${detail ? ` (${detail})` : ''}. Ask an admin for the kardinal role that grants it (promoter, approver or admin).`
    case '409':
      return `Could not ${action}: ${detail}. Refresh to see the current state, then try again.`
    case '401':
      return `Could not ${action}: your session ended. Sign in again, then retry.`
    default:
      return `Could not ${action}: ${detail}`
  }
}

interface DialogProps {
  kind: Exclude<StageActionKind, 'openPR'>
  view: StageView
  pipeline: string
  namespace: string
  /** The gate the action was opened for, if any. */
  gate?: PolicyGate
  onCancel: () => void
  onDone: (message: string) => void
}

function ActionDialog({ kind, view, pipeline, namespace, gate: openedFor, onCancel, onDone }: DialogProps) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string>()
  const [text, setText] = useState('')
  const approvalGates = view.gates.filter(g => g.holding && g.approval)
  const plainGates = view.gates.filter(g => g.holding && !g.approval)
  const [gate, setGate] = useState((openedFor && !openedFor.approval ? openedFor : plainGates[0])?.name ?? '')
  const [minutes, setMinutes] = useState(60)
  const [toBundle, setToBundle] = useState(view.rollbackChoices[0]?.bundle ?? '')
  const [hold, setHold] = useState(false)
  const ids = { text: useId(), gate: useId(), minutes: useId(), to: useId(), hold: useId() }
  const env = view.env
  const incoming = view.incoming?.bundle ?? ''
  const gateShown = plainGates.find(g => g.name === gate)
  const gateName = gateShown ? gateLabel(gateShown) : ''
  const durations = overrideChoices(gateShown)
  const duration = durations.includes(minutes) ? minutes : durations[0]
  const shown = view.incoming?.version ?? incoming
  const run = async (what: string, call: () => Promise<{ message: string }>, done: string) => {
    setBusy(true)
    setError(undefined)
    try {
      const res = await call()
      onDone(`${done}. ${res.message}`)
    } catch (err) {
      setError(stageErrorMessage(what, err))
    } finally {
      setBusy(false)
    }
  }
  const common = { open: true, onCancel, busy, error }
  const reason = (label: string, required: boolean, placeholder: string) => (
    <div className="stage-field">
      <label htmlFor={ids.text}>{label}{required ? '' : ' (optional)'}</label>
      <input id={ids.text} type="text" value={text} maxLength={1024} required={required} placeholder={placeholder}
        disabled={busy} onChange={e => setText(e.target.value)} />
    </div>
  )
  switch (kind) {
    case 'approve':
    case 'reject': {
      const g = openedFor?.approval ? openedFor : approvalGates[0]
      const a = g?.approval
      const approve = kind === 'approve'
      return (
        <StageDialog {...common} title={`${approve ? 'Approve' : 'Decline'} ${shown} for ${env}?`}
          description={approve
            ? `Your approval counts toward ${g ? gateLabel(g) : 'the approval gate'}${a ? `: ${a.approved} of ${a.required} so far` : ''}. ${env} starts once the quorum is met and the other gates pass.`
            : `Your decline holds ${g ? gateLabel(g) : 'the approval gate'}: ${shown} does not reach ${env} while it counts. It does not reject the version elsewhere.`}
          confirmLabel={approve ? 'Approve' : 'Decline'} tone={approve ? 'default' : 'destructive'}
          onConfirm={() => void run(`${approve ? 'approve' : 'decline'} ${shown} for ${env}`, () => api.recordApproval({ bundle: incoming, environment: env, namespace,
            decision: kind, comment: text.trim() || undefined }), approve ? 'Approved' : 'Declined')}>
          {reason('Comment', false, approve ? 'e.g. checked the canary dashboards' : 'e.g. error rate up in uat')}
        </StageDialog>
      )
    }
    case 'override':
      return (
        <StageDialog {...common} title={`Override ${gateName || 'the gate'} in ${env}?`}
          description={`${env} passes ${gateName || 'the gate'} for ${shown || 'the version on its way in'} until the override ends. Your name and reason are recorded.`}
          confirmLabel="Override gate" confirmDisabled={!gate || text.trim() === ''}
          onConfirm={() => void run(`override ${gateName} in ${env}`, () => api.overrideGate({ pipeline, namespace, gate, environment: env,
            reason: text.trim(), expiresInMinutes: duration }), 'Overridden')}>
          {plainGates.length > 1 && (
            <div className="stage-field">
              <label htmlFor={ids.gate}>Gate</label>
              <select id={ids.gate} value={gate} disabled={busy} onChange={e => setGate(e.target.value)}>
                {plainGates.map(g => <option key={g.name} value={g.name}>{gateLabel(g)}</option>)}
              </select>
            </div>
          )}
          {reason('Reason', true, 'e.g. P0 hotfix for incident 4521')}
          <div className="stage-field">
            <label htmlFor={ids.minutes}>For</label>
            <select id={ids.minutes} value={duration} disabled={busy} onChange={e => setMinutes(Number(e.target.value))}>
              {durations.map(m => <option key={m} value={m}>{durationLabel(m)}</option>)}
            </select>
          </div>
        </StageDialog>
      )
    case 'promote': {
      const from = view.spec?.upstreams?.join(', ') ?? ''
      return (
        <StageDialog {...common} title={`Promote to ${env}?`}
          description={`Creates a new promotion of the version live in ${from} into ${env}. Its gates still apply.`}
          confirmLabel="Promote"
          onConfirm={() => void run(`promote to ${env}`, () => api.promote(pipeline, env, namespace), 'Promoted')} />
      )
    }
    case 'rollback': {
      const choice = view.rollbackChoices.find(c => c.bundle === toBundle)
      return (
        <StageDialog {...common} tone="destructive" title={`Roll ${env} back${choice ? ` to ${choice.version}` : ''}?`}
          description={`A new promotion deploys ${choice ? `${choice.version} (${toBundle})` : 'the version Verified before the one running'} to ${env}${hold ? ' and holds it there until you release it' : ''}.`}
          confirmLabel="Roll back" confirmDisabled={hold && text.trim() === ''}
          onConfirm={() => void run(`roll ${env} back`, () => api.rollback(pipeline, env, namespace, toBundle || undefined,
            hold ? text.trim() : undefined), 'Rolled back')}>
          {view.rollbackChoices.length > 0 && (
            <div className="stage-field">
              <label htmlFor={ids.to}>Version</label>
              <select id={ids.to} value={toBundle} disabled={busy} onChange={e => setToBundle(e.target.value)}>
                {view.rollbackChoices.map(c => <option key={c.bundle} value={c.bundle}>{c.version} ({c.bundle})</option>)}
              </select>
            </div>
          )}
          <div className="stage-field stage-field--inline">
            <input id={ids.hold} type="checkbox" checked={hold} disabled={busy} onChange={e => setHold(e.target.checked)} />
            <label htmlFor={ids.hold}>Hold {env} on this version until released</label>
          </div>
          {hold && reason('Why hold', true, 'e.g. 1.5.0 corrupts sessions')}
        </StageDialog>
      )
    }
    case 'release':
      return (
        <StageDialog {...common} title={`Release the hold on ${env}?`}
          description={`Newer versions promote into ${env} again, through its gates.`} confirmLabel="Release hold"
          onConfirm={() => void run(`release the hold on ${env}`, () => api.releaseHold(pipeline, env, namespace), 'Released')} />
      )
    case 'pause':
    case 'resume':
      return (
        <StageDialog {...common} tone={kind === 'pause' ? 'destructive' : 'default'}
          title={`${kind === 'pause' ? 'Pause' : 'Resume'} ${pipeline}?`}
          description={kind === 'pause'
            ? `Versions in flight stop before their next environment, in every environment of ${pipeline}, until it is resumed.`
            : `Promotions of ${pipeline} start again where they stopped.`}
          confirmLabel={kind === 'pause' ? 'Pause pipeline' : 'Resume pipeline'}
          onConfirm={() => void run(`${kind} ${pipeline}`, () => kind === 'pause' ? api.pause(pipeline, namespace) : api.resume(pipeline, namespace),
            kind === 'pause' ? 'Paused' : 'Resumed')} />
      )
    case 'rejectVersion':
      return (
        <StageDialog {...common} tone="destructive" title={`Reject ${view.incoming?.version ?? incoming}?`}
          description={`${incoming} is never promoted again, in any environment, and rollback never picks it. Environments that already run it keep it until rolled back. This cannot be undone.`}
          confirmLabel="Reject version" confirmDisabled={text.trim() === ''}
          onConfirm={() => void run(`reject ${incoming}`, () => api.rejectBundle(incoming, text.trim(), namespace), 'Rejected')}>
          {reason('Reason', true, 'e.g. CVE-2026-1234 in the base image')}
        </StageDialog>
      )
  }
}

function durationLabel(minutes: number): string {
  if (minutes < 60) return `${minutes} minutes`
  if (minutes === 60) return '1 hour'
  if (minutes % 1440 === 0) return minutes === 1440 ? '24 hours' : `${minutes / 1440} days`
  if (minutes % 60 === 0) return `${minutes / 60} hours`
  return `${minutes} minutes`
}
