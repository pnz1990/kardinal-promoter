// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// components/ApprovalActions.tsx — approve, reject or revoke a Bundle for an
// environment's approval gates from the UI (POST /api/v1/ui/approvals, the
// UI's kardinal approve). The decision is recorded for the user the UI
// authenticated (TokenReview mode); without it the server refuses and the
// message says to use the CLI. Reject asks first: it blocks the gate. When the
// person may not decide (restricted: no sign-in, or not the role), the buttons
// stay in place but inert, the reason their tooltip; the parent shows it once.

import { useId, useState } from 'react'
import { api } from '../api/client'
import { ConfirmDialog, actionErrorMessage } from './ConfirmDialog'

interface Props {
  bundle: string
  environment: string
  namespace: string
  /** Called after a decision is recorded, so the parent can refresh. */
  onDone?: () => void
  /** Why the person may not take a decision here (U7), or undefined when they may. */
  reasonFor?: (decision: Action) => string | undefined
}

export type Action = 'approve' | 'reject' | 'revoke'

export function ApprovalActions({ bundle, environment, namespace, onDone, reasonFor }: Props) {
  const [comment, setComment] = useState('')
  const [busy, setBusy] = useState(false)
  const [result, setResult] = useState<string>()
  const [error, setError] = useState<string>()
  const [confirmReject, setConfirmReject] = useState(false)
  const commentId = useId()
  const reasonId = useId()
  const reasons = { approve: reasonFor?.('approve'), reject: reasonFor?.('reject'), revoke: reasonFor?.('revoke') }
  // An inert button: focusable, does nothing, says why.
  const inert = (a: Action) => reasons[a]
    ? { 'aria-disabled': true, 'aria-describedby': `${reasonId}-${a}`, title: reasons[a] }
    : {}
  const act = (a: Action, f: () => void) => () => { if (!reasons[a]) f() }

  async function run(action: Action) {
    setBusy(true)
    setError(undefined)
    setResult(undefined)
    const req = action === 'revoke'
      ? { bundle, environment, namespace, revoke: true }
      : { bundle, environment, namespace, decision: action, comment: comment.trim() || undefined }
    try {
      const res = await api.recordApproval(req)
      setResult(res.message)
      onDone?.()
    } catch (err: unknown) {
      setError(actionErrorMessage(action === 'revoke' ? 'revoke your decision' : `${action} ${bundle} for ${environment}`, err))
    } finally {
      setBusy(false)
      setConfirmReject(false)
    }
  }

  return (
    <div className="approval-actions">
      <label htmlFor={commentId} className="approval-actions__label">Comment (optional)</label>
      <input id={commentId} className="approval-actions__comment" type="text" value={comment} maxLength={1024}
        disabled={busy || (!!reasons.approve && !!reasons.reject)} placeholder="e.g. checked the canary dashboards" onChange={e => setComment(e.target.value)} />
      <div className="approval-actions__buttons">
        <button type="button" className="approval-actions__approve" disabled={busy} {...inert('approve')} onClick={act('approve', () => { void run('approve') })}>
          Approve
        </button>
        <button type="button" className="approval-actions__reject" disabled={busy} {...inert('reject')} onClick={act('reject', () => setConfirmReject(true))}>
          Reject
        </button>
        <button type="button" className="approval-actions__revoke" disabled={busy} {...inert('revoke')} onClick={act('revoke', () => { void run('revoke') })}>
          Revoke mine
        </button>
      </div>
      {(['approve', 'reject', 'revoke'] as const).map(a => reasons[a] &&
        <span key={a} id={`${reasonId}-${a}`} className="sr-only">{reasons[a]}</span>)}
      {result && <div role="status" className="approval-actions__result">{result}</div>}
      {error && <div role="alert" className="approval-actions__error">{error}</div>}
      {confirmReject && (
        <ConfirmDialog
          title={`Reject ${bundle} for ${environment}?`}
          description={`Your rejection blocks the approval gates of ${environment} for this version until you revoke it.`}
          confirmLabel="Reject"
          danger
          loading={busy}
          onConfirm={() => { void run('reject') }}
          onCancel={() => setConfirmReject(false)}
        />
      )}
    </div>
  )
}
