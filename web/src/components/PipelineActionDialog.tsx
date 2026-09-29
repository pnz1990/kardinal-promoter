// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// components/PipelineActionDialog.tsx — confirm, then run, a promote or a
// rollback for one environment. The stage lane and the node detail panel both
// use it, so the two entry points ask the same question, send the same request
// and report failures in the same words.

import { useState } from 'react'
import { api } from '../api/client'
import { ConfirmDialog, actionErrorMessage } from './ConfirmDialog'

export type PipelineActionKind = 'promote' | 'rollback'

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

  function run() {
    setLoading(true)
    setError(undefined)
    const call = kind === 'promote'
      ? api.promote(pipelineName, environment, namespace)
      : api.rollback(pipelineName, environment, namespace)
    call.then(
      res => onDone(kind === 'promote'
        ? `Promotion started: bundle ${res.bundle}`
        : `Rollback started: bundle ${res.bundle}`),
      (err: unknown) => {
        setError(actionErrorMessage(
          kind === 'promote' ? `promote ${pipelineName} to ${environment}` : `roll back ${environment}`,
          err,
        ))
        setLoading(false)
      },
    )
  }

  return (
    <ConfirmDialog
      title={kind === 'promote' ? `Promote ${pipelineName} to ${environment}?` : `Roll back ${environment}?`}
      description={kind === 'promote'
        ? `Creates a new bundle for ${pipelineName} that targets ${environment}. Its progress shows in the bundle timeline.`
        : `Creates a rollback bundle that returns ${environment} to the previous verified version of ${pipelineName}.`}
      confirmLabel={kind === 'promote' ? `Promote to ${environment}` : `Roll back ${environment}`}
      danger={kind === 'rollback'}
      loading={loading}
      error={error}
      onConfirm={run}
      onCancel={onCancel}
    />
  )
}
