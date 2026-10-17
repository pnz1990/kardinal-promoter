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
// A gate that declares approval fields (spec.approval.fields) gets one input
// per field: text, a checkbox for a bool, a URL input, a select for an enum.
// Approve checks them first (fieldProblems); the server checks them again with
// the gate's own rule and refuses what it does not accept.

import { useId, useState } from 'react'
import { api } from '../api/client'
import type { ApprovalFieldDecl } from '../types'
import { ConfirmDialog, actionErrorMessage } from './ConfirmDialog'

interface Props {
  bundle: string
  environment: string
  namespace: string
  /** Called after a decision is recorded, so the parent can refresh. */
  onDone?: () => void
  /** Why the person may not take a decision here (U7), or undefined when they may. */
  reasonFor?: (decision: Action) => string | undefined
  /** The gate's declared approval fields: an approve carries them. */
  fields?: ApprovalFieldDecl[]
}

/** What is wrong with values under the declared fields, as the server's rule
 *  says it (lifecycle.ApprovalFieldProblems); empty when an approve may go. */
export function fieldProblems(fields: ApprovalFieldDecl[] = [], values: Record<string, string>): string[] {
  const out: string[] = []
  for (const f of fields) {
    const v = values[f.name] ?? ''
    if (v === '') {
      if (f.required) out.push(`${f.name} is required`)
      continue
    }
    switch (f.type) {
      case 'bool':
        if (v !== 'true' && v !== 'false') out.push(`${f.name} must be true or false`)
        break
      case 'int':
        if (!/^[-+]?[0-9]+$/.test(v)) out.push(`${f.name} must be an integer`)
        break
      case 'url': {
        let ok = false
        try { const u = new URL(v); ok = (u.protocol === 'https:' || u.protocol === 'http:') && u.host !== '' } catch { ok = false }
        if (!ok) out.push(`${f.name} must be an http or https URL`)
        break
      }
      case 'enum':
        if (!(f.values ?? []).includes(v)) out.push(`${f.name} must be one of ${(f.values ?? []).join(', ')}`)
        break
    }
    if (f.pattern) {
      try { if (!new RegExp(f.pattern).test(v)) out.push(`${f.name} must match ${f.pattern}`) } catch { /* the server says */ }
    }
    if (f.mustBe && v !== f.mustBe) out.push(`${f.name} must be ${f.mustBe}`)
  }
  return out
}

export type Action = 'approve' | 'reject' | 'revoke'

export function ApprovalActions({ bundle, environment, namespace, onDone, reasonFor, fields = [] }: Props) {
  const [comment, setComment] = useState('')
  const [values, setValues] = useState<Record<string, string>>({})
  const fieldId = useId()
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
    setError(undefined)
    setResult(undefined)
    const given = Object.fromEntries(Object.entries(values).filter(([, v]) => v !== ''))
    if (action === 'approve') {
      const problems = fieldProblems(fields, given)
      if (problems.length > 0) {
        setError(`Not approved: ${problems.join('; ')}`)
        return
      }
    }
    setBusy(true)
    const req = action === 'revoke'
      ? { bundle, environment, namespace, revoke: true }
      : { bundle, environment, namespace, decision: action, comment: comment.trim() || undefined,
          fields: action === 'approve' && Object.keys(given).length > 0 ? given : undefined }
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

  const set = (name: string, v: string) => setValues(prev => ({ ...prev, [name]: v }))
  const disabled = busy || (!!reasons.approve && !!reasons.reject)

  return (
    <div className="approval-actions">
      {fields.length > 0 && (
        <fieldset className="approval-actions__fields">
          <legend>What this approval records</legend>
          {fields.map(f => {
            const id = `${fieldId}-${f.name}`
            const label = `${f.name}${f.required ? '' : ' (optional)'}`
            if (f.type === 'bool') {
              return (
                <div key={f.name} className="approval-actions__field">
                  <input id={id} type="checkbox" disabled={disabled} checked={values[f.name] === 'true'}
                    onChange={e => set(f.name, e.target.checked ? 'true' : (f.required || f.mustBe ? '' : 'false'))} />
                  <label htmlFor={id}>{f.description || label}</label>
                </div>
              )
            }
            return (
              <div key={f.name} className="approval-actions__field">
                <label htmlFor={id}>{label}</label>
                {f.type === 'enum'
                  ? (
                      <select id={id} disabled={disabled} value={values[f.name] ?? ''} onChange={e => set(f.name, e.target.value)}>
                        <option value="">—</option>
                        {(f.values ?? []).map(v => <option key={v} value={v}>{v}</option>)}
                      </select>
                    )
                  : (
                      <input id={id} type={f.type === 'url' ? 'url' : f.type === 'int' ? 'number' : 'text'} disabled={disabled}
                        value={values[f.name] ?? ''} maxLength={1024} required={f.required} pattern={f.pattern}
                        placeholder={f.description} onChange={e => set(f.name, e.target.value)} />
                    )}
                {f.description && f.type !== 'bool' && <span className="approval-actions__hint">{f.description}</span>}
              </div>
            )
          })}
        </fieldset>
      )}
      <label htmlFor={commentId} className="approval-actions__label">Comment (optional)</label>
      <input id={commentId} className="approval-actions__comment" type="text" value={comment} maxLength={1024}
        disabled={disabled} placeholder="e.g. checked the canary dashboards" onChange={e => setComment(e.target.value)} />
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
