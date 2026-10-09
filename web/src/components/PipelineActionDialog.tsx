// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// components/PipelineActionDialog.tsx — confirm, then run, a promote or a
// rollback for one environment. The stage lane and the node detail panel both
// use it, so the two entry points ask the same question, send the same request
// and report failures in the same words.
//
// A rollback can hold the environment (#1528): it stays on the rollback until
// someone releases the hold, and the rollback passes the gates that would
// block it, each pass recorded as EXEMPT. A hold needs a reason. release-hold
// confirms the release.

import { useId, useState } from 'react'
import { api } from '../api/client'
import { ConfirmDialog, actionErrorMessage } from './ConfirmDialog'
import '../styles/PipelineActionDialog.css'

export type PipelineActionKind = 'promote' | 'rollback' | 'release-hold'

interface Props {
  kind: PipelineActionKind
  pipelineName: string
  environment: string
  namespace: string
  /** Called with a status sentence once the request succeeds; the dialog closes. */
  onDone: (message: string) => void
  onCancel: () => void
}

export function PipelineActionDialog({ kind, pipelineName, environment, namespace, onDone, onCancel }: Props) {
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string>()
  const [hold, setHold] = useState(false)
  const [reason, setReason] = useState('')
  const holdId = useId()
  const reasonId = useId()

  function run() {
    if (kind === 'rollback' && hold && reason.trim() === '') {
      setError(`Say why ${environment} is held: the reason is shown on every gate the rollback passes.`)
      return
    }
    setLoading(true)
    setError(undefined)
    let call: Promise<string>
    switch (kind) {
      case 'promote':
        call = api.promote(pipelineName, environment, namespace).then(res => `Promotion started: bundle ${res.bundle}`)
        break
      case 'release-hold':
        call = api.releaseHold(pipelineName, environment, namespace).then(() => `Hold on ${environment} released`)
        break
      default:
        call = (hold
          ? api.rollback(pipelineName, environment, namespace, undefined, reason.trim())
          : api.rollback(pipelineName, environment, namespace))
          .then(res => hold
            ? `Rollback started: bundle ${res.bundle}; ${environment} is held on it`
            : `Rollback started: bundle ${res.bundle}`)
    }
    call.then(
      message => onDone(message),
      (err: unknown) => {
        setError(actionErrorMessage(
          kind === 'promote' ? `promote ${pipelineName} to ${environment}`
            : kind === 'release-hold' ? `release the hold on ${environment}` : `roll back ${environment}`,
          err,
        ))
        setLoading(false)
      },
    )
  }

  const title = kind === 'promote' ? `Promote ${pipelineName} to ${environment}?`
    : kind === 'release-hold' ? `Release the hold on ${environment}?` : `Roll back ${environment}?`
  const description = kind === 'promote'
    ? `Creates a new bundle for ${pipelineName} that targets ${environment}. Its progress shows in the bundle timeline.`
    : kind === 'release-hold'
      ? `The newest bundle held back from ${environment} then promotes there through its gates.`
      : `Creates a rollback bundle that returns ${environment} to the previous verified version of ${pipelineName}.`
  const confirmLabel = kind === 'promote' ? `Promote to ${environment}`
    : kind === 'release-hold' ? 'Release hold'
      : hold ? `Roll back and hold ${environment}` : `Roll back ${environment}`

  return (
    <ConfirmDialog
      title={title}
      description={description}
      confirmLabel={confirmLabel}
      danger={kind === 'rollback'}
      loading={loading}
      error={error}
      onConfirm={run}
      onCancel={onCancel}
    >
      {kind === 'rollback' && (
        <div className="hold-option">
          <label htmlFor={holdId} className="hold-option__toggle">
            <input id={holdId} type="checkbox" checked={hold} disabled={loading}
              onChange={e => setHold(e.target.checked)} />
            Hold {environment} on this rollback
          </label>
          {hold && (
            <>
              <p className="hold-option__note">
                No other bundle promotes into {environment} until the hold is released. Gates that
                would block the rollback pass as EXEMPT, each pass recorded with your name and reason.
              </p>
              <label htmlFor={reasonId} className="hold-option__label">Reason (required)</label>
              <input id={reasonId} type="text" className="hold-option__reason" value={reason}
                maxLength={1024} disabled={loading} required aria-required="true"
                placeholder="e.g. INC-4211: v2.3 leaks connections"
                onChange={e => setReason(e.target.value)} />
            </>
          )}
        </div>
      )}
    </ConfirmDialog>
  )
}
