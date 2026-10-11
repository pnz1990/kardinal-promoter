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
import '../styles/ActionBar.css'

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
        className="action-bar"
      >
        {paused ? (
          <button
            onClick={() => { setActionError(undefined); setPendingAction('resume') }}
            aria-label="Resume pipeline"
            className="action-bar__button action-bar__button--resume"
          >
            ▶ Resume
          </button>
        ) : (
          <button
            onClick={() => { setActionError(undefined); setPendingAction('pause') }}
            aria-label="Pause pipeline"
            className="action-bar__button action-bar__button--pause"
          >
            ⏸ Pause
          </button>
        )}

        {/* While a dialog is open the error shows inside it; one alert, not two. */}
        {actionError && pendingAction === null && (
          <span
            role="alert"
            className="action-bar__error"
          >
            {actionError}
          </span>
        )}
      </div>

      {pendingAction === 'pause' && (
        <ConfirmDialog
          title="Pause pipeline?"
          description={`No new promotion step starts for "${pipelineName}", and steps that have not opened a PR yet stop before their next git step. Steps waiting for a PR merge or running health checks continue, and open PRs stay open.`}
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
          description={`Held promotion steps for "${pipelineName}" continue where they stopped.`}
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
