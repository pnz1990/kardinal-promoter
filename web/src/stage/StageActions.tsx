// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// stage/StageActions.tsx — the incident actions of one environment, each
// behind a confirmation dialog: approve or reject for an approval gate (with
// its quorum), override a gate, roll back to a chosen version (toBundle,
// optionally held), release a hold, pause or resume the pipeline, and reject
// the version on its way in. Only the actions that make sense now are shown
// (stageView). The button, the dialog's confirm button and the result
// message use the same verb.
import { useId, useState } from 'react'
import { api } from '../api/client'
import { actionErrorMessage } from '../components/ConfirmDialog'
import { maxOverrideMinutes } from './overrideLimits'
import type { StageActionKind, StageView } from './stageModel'
import { StageDialog } from './StageDialog'

interface Props {
  view: StageView
  pipeline: string
  namespace: string
  /** The action to open (from a "why" reason), or undefined. */
  active: StageActionKind | undefined
  onOpen: (kind: StageActionKind | undefined) => void
  /** Called after an action succeeds, so the page refreshes. */
  onChanged?: () => void
}

/** The button label of each action; the dialog and the result use the same verb. */
export const ACTION_LABEL: Record<StageActionKind, string> = {
  approve: 'Approve', reject: 'Reject', override: 'Override gate', promote: 'Promote', rollback: 'Roll back',
  release: 'Release hold', pause: 'Pause pipeline', resume: 'Resume pipeline', rejectVersion: 'Reject version',
  openPR: 'Open pull request',
}

const ORDER: StageActionKind[] = ['approve', 'reject', 'override', 'promote', 'openPR', 'rollback', 'release', 'rejectVersion', 'pause', 'resume']
const DESTRUCTIVE = new Set<StageActionKind>(['reject', 'rollback', 'rejectVersion', 'pause'])

export function StageActions({ view, pipeline, namespace, active, onOpen, onChanged }: Props) {
  const [result, setResult] = useState<string>()
  const kinds = ORDER.filter(k => view.actions.has(k))
  if (kinds.length === 0) return <p className="stage-muted">No action applies here right now.</p>
  const prURL = view.step?.prURL
  return (
    <>
      <div className="stage-actions">
        {kinds.map(k => k === 'openPR' && prURL
          ? <a key={k} data-action={k} className="stage-button stage-button--secondary" href={prURL} target="_blank" rel="noreferrer">
              {ACTION_LABEL[k]}
            </a>
          : <button key={k} type="button" data-action={k}
              className={`stage-button ${k === 'approve' || k === 'promote' ? 'stage-button--primary' : DESTRUCTIVE.has(k) ? 'stage-button--destructive' : 'stage-button--secondary'}`}
              onClick={() => { setResult(undefined); onOpen(k) }}>
              {ACTION_LABEL[k]}
            </button>)}
      </div>
      {result && <p role="status" className="stage-result">{result}</p>}
      {active && active !== 'openPR' && (
        <ActionDialog kind={active} view={view} pipeline={pipeline} namespace={namespace}
          onCancel={() => onOpen(undefined)}
          onDone={msg => { setResult(msg); onOpen(undefined); onChanged?.() }} />
      )}
    </>
  )
}

interface DialogProps {
  kind: Exclude<StageActionKind, 'openPR'>
  view: StageView
  pipeline: string
  namespace: string
  onCancel: () => void
  onDone: (message: string) => void
}

function ActionDialog({ kind, view, pipeline, namespace, onCancel, onDone }: DialogProps) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string>()
  const [text, setText] = useState('')
  const approvalGates = view.gates.filter(g => g.holding && g.approval)
  const plainGates = view.gates.filter(g => g.holding && !g.approval)
  const [gate, setGate] = useState(plainGates[0]?.name ?? '')
  const [minutes, setMinutes] = useState(60)
  const [toBundle, setToBundle] = useState(view.rollbackChoices[0]?.bundle ?? '')
  const [hold, setHold] = useState(false)
  const ids = { text: useId(), gate: useId(), minutes: useId(), to: useId(), hold: useId() }
  const env = view.env
  const incoming = view.incoming?.bundle ?? ''
  const run = async (what: string, call: () => Promise<{ message: string }>, done: string) => {
    setBusy(true)
    setError(undefined)
    try {
      const res = await call()
      onDone(`${done}. ${res.message}`)
    } catch (err) {
      setError(actionErrorMessage(what, err))
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
      const g = approvalGates[0]
      const a = g?.approval
      const approve = kind === 'approve'
      return (
        <StageDialog {...common} title={`${approve ? 'Approve' : 'Reject'} ${incoming} for ${env}?`}
          description={approve
            ? `Your approval counts toward ${g?.name ?? 'the approval gate'}${a ? `: ${a.approved} of ${a.required} so far` : ''}. ${env} starts once the quorum is met and the other gates pass.`
            : `Your reject holds ${g?.name ?? 'the approval gate'}: ${incoming} does not reach ${env} while it counts.`}
          confirmLabel={approve ? 'Approve' : 'Reject'} tone={approve ? 'default' : 'destructive'}
          onConfirm={() => void run(`${kind} ${incoming} for ${env}`, () => api.recordApproval({ bundle: incoming, environment: env, namespace,
            decision: kind, comment: text.trim() || undefined }), approve ? 'Approved' : 'Rejected')}>
          {reason('Comment', false, approve ? 'e.g. checked the canary dashboards' : 'e.g. error rate up in uat')}
        </StageDialog>
      )
    }
    case 'override':
      return (
        <StageDialog {...common} title={`Override ${gate || 'the gate'} in ${env}?`}
          description={`${env} passes ${gate || 'the gate'} for ${incoming || 'the version on its way in'} until the override ends. Your name and reason are recorded.`}
          confirmLabel="Override gate" confirmDisabled={!gate || text.trim() === ''}
          onConfirm={() => void run(`override ${gate} in ${env}`, () => api.overrideGate({ pipeline, namespace, gate, environment: env,
            reason: text.trim(), expiresInMinutes: minutes }), 'Overridden')}>
          {plainGates.length > 1 && (
            <div className="stage-field">
              <label htmlFor={ids.gate}>Gate</label>
              <select id={ids.gate} value={gate} disabled={busy} onChange={e => setGate(e.target.value)}>
                {plainGates.map(g => <option key={g.name} value={g.name}>{g.name}</option>)}
              </select>
            </div>
          )}
          {reason('Reason', true, 'e.g. P0 hotfix for incident 4521')}
          <div className="stage-field">
            <label htmlFor={ids.minutes}>For</label>
            <select id={ids.minutes} value={minutes} disabled={busy} onChange={e => setMinutes(Number(e.target.value))}>
              {[15, 60, 240, 1440].filter(m => m <= maxOverrideMinutes).map(m => <option key={m} value={m}>{durationLabel(m)}</option>)}
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
            ? `No new promotion starts in any environment of ${pipeline} until it is resumed. Promotions already running finish.`
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
  if (minutes < 1440) return `${minutes / 60} hours`
  return '24 hours'
}
