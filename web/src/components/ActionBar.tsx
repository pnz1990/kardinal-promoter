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
// FR-506-02: Rollback from pipeline header.
// FR-506-03: Override gate with mandatory reason text.
// FR-506-04: Confirmation dialog for destructive actions.
// FR-506-05: Inline error display (not just a toast).
import { useState, useCallback, type FormEvent } from 'react'
import { api } from '../api/client'

// ─── Shared dialog primitives ─────────────────────────────────────────────────

interface ConfirmDialogProps {
  title: string
  description: string
  confirmLabel: string
  danger?: boolean
  onConfirm: () => void
  onCancel: () => void
  loading?: boolean
  error?: string
}

function ConfirmDialog({
  title,
  description,
  confirmLabel,
  danger,
  onConfirm,
  onCancel,
  loading,
  error,
}: ConfirmDialogProps) {
  return (
    <div
      role="dialog"
      aria-modal="true"
      aria-labelledby="confirm-title"
      style={{
        position: 'fixed', inset: 0,
        background: 'rgba(0,0,0,0.7)',
        display: 'flex', alignItems: 'center', justifyContent: 'center',
        zIndex: 1000,
      }}
    >
      <div style={{
        background: 'var(--color-bg)',
        border: '1px solid #1e293b',
        borderRadius: '8px',
        padding: '1.5rem',
        maxWidth: '400px',
        width: '90%',
      }}>
        <h2 id="confirm-title" style={{ margin: '0 0 0.5rem', fontSize: '1rem', fontWeight: 700, color: 'var(--color-text)' }}>
          {title}
        </h2>
        <p style={{ color: 'var(--color-text-muted)', fontSize: '0.875rem', margin: '0 0 1rem' }}>
          {description}
        </p>
        {error && (
          <div role="alert" style={{ color: '#ef4444', fontSize: '0.8rem', marginBottom: '0.75rem', background: '#1e0c0c', border: '1px solid #7f1d1d', borderRadius: '4px', padding: '0.5rem 0.75rem' }}>
            {error}
          </div>
        )}
        <div style={{ display: 'flex', gap: '0.5rem', justifyContent: 'flex-end' }}>
          <button
            onClick={onCancel}
            disabled={loading}
            aria-label="Cancel"
            style={{
              padding: '0.45rem 1rem',
              background: 'transparent',
              border: '1px solid #334155',
              borderRadius: '6px',
              color: 'var(--color-text-muted)',
              cursor: 'pointer',
              fontSize: '0.875rem',
            }}
          >
            Cancel
          </button>
          <button
            onClick={onConfirm}
            disabled={loading}
            aria-label={confirmLabel}
            style={{
              padding: '0.45rem 1rem',
              background: danger ? '#7f1d1d' : '#1e3a5f',
              border: `1px solid ${danger ? '#ef4444' : '#3b82f6'}`,
              borderRadius: '6px',
              color: danger ? '#fca5a5' : '#93c5fd',
              cursor: loading ? 'not-allowed' : 'pointer',
              fontSize: '0.875rem',
              fontWeight: 600,
              opacity: loading ? 0.6 : 1,
            }}
          >
            {loading ? 'Working…' : confirmLabel}
          </button>
        </div>
      </div>
    </div>
  )
}

// ─── Override gate dialog ─────────────────────────────────────────────────────

interface OverrideGateDialogProps {
  gateName: string
  gateNamespace: string
  onDone: () => void
  onCancel: () => void
}

function OverrideGateDialog({ gateName, gateNamespace, onDone, onCancel }: OverrideGateDialogProps) {
  const [reason, setReason] = useState('')
  const [expiresInMinutes, setExpiresInMinutes] = useState(60)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | undefined>()

  const handleSubmit = useCallback(async (e: FormEvent) => {
    e.preventDefault()
    if (!reason.trim()) {
      setError('Reason is required.')
      return
    }
    setLoading(true)
    setError(undefined)
    try {
      await api.approveGate(gateName, gateNamespace, reason.trim(), expiresInMinutes)
      onDone()
    } catch (err) {
      setError(String(err))
    } finally {
      setLoading(false)
    }
  }, [reason, expiresInMinutes, gateName, gateNamespace, onDone])

  return (
    <div
      role="dialog"
      aria-modal="true"
      aria-labelledby="override-title"
      style={{
        position: 'fixed', inset: 0,
        background: 'rgba(0,0,0,0.7)',
        display: 'flex', alignItems: 'center', justifyContent: 'center',
        zIndex: 1000,
      }}
    >
      <form
        onSubmit={handleSubmit}
        style={{
          background: 'var(--color-bg)',
          border: '1px solid #1e293b',
          borderRadius: '8px',
          padding: '1.5rem',
          maxWidth: '420px',
          width: '90%',
        }}
      >
        <h2 id="override-title" style={{ margin: '0 0 0.25rem', fontSize: '1rem', fontWeight: 700, color: '#f59e0b' }}>
          Override gate: {gateName}
        </h2>
        <p style={{ color: 'var(--color-text-muted)', fontSize: '0.8rem', margin: '0 0 1rem' }}>
          This bypasses the policy gate. Provide a mandatory reason — it will be recorded in the PR evidence.
        </p>

        <label htmlFor="override-reason" style={{ display: 'block', fontSize: '0.75rem', color: 'var(--color-text-muted)', marginBottom: '0.25rem' }}>
          Reason <span style={{ color: '#ef4444' }}>*</span>
        </label>
        <textarea
          id="override-reason"
          value={reason}
          onChange={e => setReason(e.target.value)}
          rows={3}
          placeholder="e.g. Emergency hotfix — P0 outage in prod"
          required
          style={{
            width: '100%', boxSizing: 'border-box',
            background: 'var(--color-surface)', border: '1px solid #334155',
            borderRadius: '6px', padding: '0.5rem 0.75rem',
            color: 'var(--color-text)', fontSize: '0.875rem',
            resize: 'vertical', marginBottom: '0.75rem',
            fontFamily: 'inherit',
          }}
        />

        <label htmlFor="override-expires" style={{ display: 'block', fontSize: '0.75rem', color: 'var(--color-text-muted)', marginBottom: '0.25rem' }}>
          Override duration (minutes)
        </label>
        <input
          id="override-expires"
          type="number"
          min={5}
          max={1440}
          value={expiresInMinutes}
          onChange={e => setExpiresInMinutes(Number(e.target.value))}
          style={{
            background: 'var(--color-surface)', border: '1px solid #334155',
            borderRadius: '6px', padding: '0.4rem 0.75rem',
            color: 'var(--color-text)', fontSize: '0.875rem',
            width: '100px', marginBottom: '1rem',
          }}
        />

        {error && (
          <div role="alert" style={{ color: '#ef4444', fontSize: '0.8rem', marginBottom: '0.75rem', background: '#1e0c0c', border: '1px solid #7f1d1d', borderRadius: '4px', padding: '0.5rem 0.75rem' }}>
            {error}
          </div>
        )}

        <div style={{ display: 'flex', gap: '0.5rem', justifyContent: 'flex-end' }}>
          <button
            type="button"
            onClick={onCancel}
            disabled={loading}
            aria-label="Cancel gate override"
            style={{
              padding: '0.45rem 1rem',
              background: 'transparent',
              border: '1px solid #334155',
              borderRadius: '6px',
              color: 'var(--color-text-muted)',
              cursor: 'pointer',
              fontSize: '0.875rem',
            }}
          >
            Cancel
          </button>
          <button
            type="submit"
            disabled={loading || !reason.trim()}
            aria-label="Confirm gate override"
            style={{
              padding: '0.45rem 1rem',
              background: '#78350f',
              border: '1px solid #f59e0b',
              borderRadius: '6px',
              color: '#fde68a',
              cursor: loading || !reason.trim() ? 'not-allowed' : 'pointer',
              fontSize: '0.875rem',
              fontWeight: 600,
              opacity: loading || !reason.trim() ? 0.6 : 1,
            }}
          >
            {loading ? 'Overriding…' : 'Override gate'}
          </button>
        </div>
      </form>
    </div>
  )
}

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
      setActionError(String(err))
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
              background: '#052e16',
              border: '1px solid #166534',
              borderRadius: '6px',
              color: '#86efac',
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
              background: '#1c1507',
              border: '1px solid #92400e',
              borderRadius: '6px',
              color: '#fde68a',
              cursor: 'pointer',
              fontSize: '0.8rem',
              fontWeight: 600,
            }}
          >
            ⏸ Pause
          </button>
        )}

        {actionError && (
          <span
            role="alert"
            style={{ fontSize: '0.75rem', color: '#ef4444', background: '#1e0c0c', border: '1px solid #7f1d1d', borderRadius: '4px', padding: '0.25rem 0.5rem' }}
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

// ─── GateActionButton ─────────────────────────────────────────────────────────

interface GateActionButtonProps {
  /** PolicyGate CRD name. */
  gateName: string
  /** PolicyGate CRD namespace. */
  gateNamespace: string
  /** Called after override completes — triggers a re-poll. */
  onRefresh: () => void
}

/**
 * GateActionButton renders an "Override" button for a blocking PolicyGate.
 * Clicking opens the OverrideGateDialog which requires a mandatory reason.
 */
export function GateActionButton({ gateName, gateNamespace, onRefresh }: GateActionButtonProps) {
  const [showDialog, setShowDialog] = useState(false)

  return (
    <>
      <button
        onClick={() => setShowDialog(true)}
        aria-label={`Override gate ${gateName}`}
        style={{
          padding: '0.2rem 0.6rem',
          background: '#78350f',
          border: '1px solid #f59e0b',
          borderRadius: '4px',
          color: '#fde68a',
          cursor: 'pointer',
          fontSize: '0.7rem',
          fontWeight: 600,
        }}
      >
        Override
      </button>

      {showDialog && (
        <OverrideGateDialog
          gateName={gateName}
          gateNamespace={gateNamespace}
          onDone={() => {
            setShowDialog(false)
            onRefresh()
          }}
          onCancel={() => setShowDialog(false)}
        />
      )}
    </>
  )
}
