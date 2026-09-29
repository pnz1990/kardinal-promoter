// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// components/ActionBar.tsx — Pipeline-level action buttons (#464).
// FR-506-01: Pause / Resume pipeline.
// FR-506-04: Confirmation dialog before the action runs.
// FR-506-05: Inline error display (not just a toast).
// Rollback lives on the lane and in NodeDetail, next to the environment it acts on.
import { useState, useCallback } from 'react'
import { api } from '../api/client'
import { ConfirmDialog, actionErrorMessage } from './ConfirmDialog'

// ─── ActionBar ────────────────────────────────────────────────────────────────

interface ActionBarProps {
  /** Pipeline name. */
  pipelineName: string
  /** Pipeline namespace. */
  namespace: string
  /** Whether the pipeline is currently paused. */
  paused: boolean
  /** Called after a successful pause/resume — triggers a re-poll. */
  onRefresh: () => void
}

type PendingAction = 'pause' | 'resume' | null

/**
 * ActionBar renders pipeline-level action buttons: Pause / Resume.
 * Confirmation dialogs are shown for destructive actions.
 * Inline error messages are shown next to the button on failure.
 */
export function ActionBar({ pipelineName, namespace, paused, onRefresh }: ActionBarProps) {
  const [pendingAction, setPendingAction] = useState<PendingAction>(null)
  const [actionLoading, setActionLoading] = useState(false)
  const [actionError, setActionError] = useState<string | undefined>()

  const handleConfirm = useCallback(async () => {
    if (!pendingAction) return
    setActionLoading(true)
    setActionError(undefined)
    try {
      if (pendingAction === 'pause') {
        await api.pause(pipelineName, namespace)
      } else {
        await api.resume(pipelineName, namespace)
      }
      setPendingAction(null)
      onRefresh()
    } catch (err) {
      setActionError(actionErrorMessage(`${pendingAction} ${pipelineName}`, err))
    } finally {
      setActionLoading(false)
    }
  }, [pendingAction, pipelineName, namespace, onRefresh])

  return (
    <>
      <div
        role="toolbar"
        aria-label="Pipeline actions"
        style={{
          display: 'flex',
          alignItems: 'center',
          gap: '0.5rem',
          marginBottom: '0.75rem',
          flexWrap: 'wrap',
        }}
      >
        {paused ? (
          <button
            onClick={() => { setActionError(undefined); setPendingAction('resume') }}
            aria-label="Resume pipeline"
            style={{
              padding: '0.35rem 0.85rem',
              background: 'var(--color-success-bg)',
              border: '1px solid var(--color-success)',
              borderRadius: '6px',
              color: 'var(--color-success)',
              cursor: 'pointer',
              fontSize: '0.8rem',
              fontWeight: 600,
            }}
          >
            ▶ Resume
          </button>
        ) : (
          <button
            onClick={() => { setActionError(undefined); setPendingAction('pause') }}
            aria-label="Pause pipeline"
            style={{
              padding: '0.35rem 0.85rem',
              background: 'var(--color-warning-bg)',
              border: '1px solid var(--color-warning)',
              borderRadius: '6px',
              color: 'var(--color-warning)',
              cursor: 'pointer',
              fontSize: '0.8rem',
              fontWeight: 600,
            }}
          >
            ⏸ Pause
          </button>
        )}

        {/* While a dialog is open the error shows inside it; one alert, not two. */}
        {actionError && pendingAction === null && (
          <span
            role="alert"
            style={{ fontSize: '0.75rem', color: 'var(--color-error)', background: 'var(--color-error-bg)', border: '1px solid var(--color-error)', borderRadius: '4px', padding: '0.25rem 0.5rem' }}
          >
            {actionError}
          </span>
        )}
      </div>

      {pendingAction === 'pause' && (
        <ConfirmDialog
          title="Pause pipeline?"
          description={`This will stop all in-flight promotions for "${pipelineName}". Existing open PRs remain open.`}
          confirmLabel="Pause pipeline"
          danger
          onConfirm={handleConfirm}
          onCancel={() => setPendingAction(null)}
          loading={actionLoading}
          error={actionError}
        />
      )}
      {pendingAction === 'resume' && (
        <ConfirmDialog
          title="Resume pipeline?"
          description={`Promotion will restart for "${pipelineName}". All queued bundles will continue.`}
          confirmLabel="Resume pipeline"
          onConfirm={handleConfirm}
          onCancel={() => setPendingAction(null)}
          loading={actionLoading}
          error={actionError}
        />
      )}
    </>
  )
}
