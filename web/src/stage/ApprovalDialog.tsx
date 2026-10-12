// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// stage/ApprovalDialog.tsx — the confirmation for an approval gate decision:
// Approve, or Decline (a reject decision on this gate in this environment,
// not a rejection of the version). The stage drawer and the approvals inbox
// open the same dialog, so the words are the same from both.
import { useId, useState } from 'react'
import { api } from '../api/client'
import type { PolicyGate } from '../types'
import { gateLabel } from './stageModel'
import { StageDialog } from './StageDialog'
import { stageErrorMessage } from './stageErrors'

export interface ApprovalDialogProps {
  decision: 'approve' | 'reject'
  /** The approval gate the decision counts toward. */
  gate?: PolicyGate
  /** The Bundle decided on, and its version as people know it. */
  bundle: string
  version: string
  env: string
  namespace: string
  onCancel: () => void
  /** Called with the result message ("Approved. …") once the decision is recorded. */
  onDone: (message: string) => void
}

export function ApprovalDialog({ decision, gate: g, bundle, version, env, namespace, onCancel, onDone }: ApprovalDialogProps) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string>()
  const [text, setText] = useState('')
  const id = useId()
  const a = g?.approval
  const approve = decision === 'approve'
  const confirm = async () => {
    setBusy(true)
    setError(undefined)
    try {
      const res = await api.recordApproval({ bundle, environment: env, namespace, decision, comment: text.trim() || undefined })
      onDone(`${approve ? 'Approved' : 'Declined'}. ${res.message}`)
    } catch (err) {
      setError(stageErrorMessage(`${approve ? 'approve' : 'decline'} ${version} for ${env}`, err))
    } finally {
      setBusy(false)
    }
  }
  return (
    <StageDialog open onCancel={onCancel} busy={busy} error={error}
      title={`${approve ? 'Approve' : 'Decline'} ${version} for ${env}?`}
      description={approve
        ? `Your approval counts toward ${g ? gateLabel(g) : 'the approval gate'}${a ? `: ${a.approved} of ${a.required} so far` : ''}. ${env} starts once the quorum is met and the other gates pass.`
        : `Your decline holds ${g ? gateLabel(g) : 'the approval gate'}: ${version} does not reach ${env} while it counts. It does not reject the version elsewhere.`}
      confirmLabel={approve ? 'Approve' : 'Decline'} tone={approve ? 'default' : 'destructive'}
      onConfirm={() => void confirm()}>
      <div className="stage-field">
        <label htmlFor={id}>Comment (optional)</label>
        <input id={id} type="text" value={text} maxLength={1024} placeholder={approve ? 'e.g. checked the canary dashboards' : 'e.g. error rate up in uat'}
          disabled={busy} onChange={e => setText(e.target.value)} />
      </div>
    </StageDialog>
  )
}
