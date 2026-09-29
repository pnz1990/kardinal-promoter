// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// components/ConfirmDialog.tsx — the one confirmation dialog for actions that
// change a pipeline (pause, resume, promote, rollback). Focus starts on Cancel,
// Tab stays inside, Esc cancels, and focus returns to the button that opened it.

import { useId, useRef, type ReactNode } from 'react'
import { useModalFocus } from '../useModalFocus'

export interface ConfirmDialogProps {
  /** The question, e.g. "Promote app-abc to prod?". */
  title: string
  /** One sentence on what happens when the user confirms. */
  description: string
  /** Label of the confirm button; says exactly what it does. */
  confirmLabel: string
  /** Red confirm button for actions that stop or undo work. */
  danger?: boolean
  onConfirm: () => void
  onCancel: () => void
  /** True while the request is running; both buttons are disabled. */
  loading?: boolean
  /** What failed and how to fix it; shown above the buttons. */
  error?: string
  /** Optional details, e.g. the images a promotion will deploy. */
  children?: ReactNode
}

export function ConfirmDialog({
  title,
  description,
  confirmLabel,
  danger,
  onConfirm,
  onCancel,
  loading,
  error,
  children,
}: ConfirmDialogProps) {
  const panelRef = useRef<HTMLDivElement>(null)
  const cancelRef = useRef<HTMLButtonElement>(null)
  const titleId = useId()
  const descId = useId()
  useModalFocus(panelRef, () => { if (!loading) onCancel() }, cancelRef)

  return (
    <div
      role="dialog"
      aria-modal="true"
      aria-labelledby={titleId}
      aria-describedby={descId}
      style={{
        position: 'fixed', inset: 0,
        background: 'rgba(0,0,0,0.6)',
        display: 'flex', alignItems: 'center', justifyContent: 'center',
        zIndex: 1000,
      }}
      onClick={e => { if (e.target === e.currentTarget && !loading) onCancel() }}
    >
      <div
        ref={panelRef}
        style={{
          background: 'var(--color-surface)',
          border: '1px solid var(--color-border)',
          borderRadius: '8px',
          padding: '1.5rem',
          maxWidth: '420px',
          width: '90%',
          boxShadow: '0 8px 32px rgba(0,0,0,0.25)',
        }}
      >
        <h2 id={titleId} style={{ margin: '0 0 0.5rem', fontSize: '1rem', fontWeight: 700, color: 'var(--color-text)', overflowWrap: 'anywhere' }}>
          {title}
        </h2>
        <p id={descId} style={{ color: 'var(--color-text-muted)', fontSize: '0.875rem', margin: '0 0 1rem' }}>
          {description}
        </p>
        {children}
        {error && (
          <div
            role="alert"
            style={{
              color: 'var(--color-error)',
              background: 'var(--color-error-bg)',
              border: '1px solid var(--color-error)',
              borderRadius: '4px',
              fontSize: '0.8rem',
              padding: '0.5rem 0.75rem',
              marginBottom: '0.75rem',
              overflowWrap: 'anywhere',
            }}
          >
            {error}
          </div>
        )}
        <div style={{ display: 'flex', gap: '0.5rem', justifyContent: 'flex-end' }}>
          <button
            ref={cancelRef}
            type="button"
            onClick={onCancel}
            disabled={loading}
            style={{
              padding: '0.45rem 1rem',
              background: 'transparent',
              border: '1px solid var(--color-border)',
              borderRadius: '6px',
              color: 'var(--color-text-muted)',
              cursor: loading ? 'not-allowed' : 'pointer',
              fontSize: '0.875rem',
            }}
          >
            Cancel
          </button>
          <button
            type="button"
            onClick={onConfirm}
            disabled={loading}
            aria-label={confirmLabel}
            style={{
              padding: '0.45rem 1rem',
              background: danger ? 'var(--color-error-bg)' : 'var(--color-accent-bg)',
              border: `1px solid ${danger ? 'var(--color-error)' : 'var(--color-accent)'}`,
              borderRadius: '6px',
              color: danger ? 'var(--color-error)' : 'var(--color-accent)',
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

/** Turn an API error into a sentence that says what failed. */
export function actionErrorMessage(action: string, err: unknown): string {
  const detail = err instanceof Error ? err.message : String(err)
  return `Could not ${action}: ${detail}`
}
