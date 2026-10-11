// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// stage/StageDialog.tsx — the confirmation dialog of every stage action. It
// follows the ConfirmDialog API of the U5 primitives (UI-PRIMITIVES.md):
// open, title, body, confirmLabel, tone, onConfirm, onCancel, busy. When U5
// lands this file becomes a re-export. Esc and Cancel close it, focus moves
// into it and returns to the trigger (useModalFocus), and the confirm button
// says exactly what happens.
import { useId, useRef, type ReactNode } from 'react'
import { useModalFocus } from '../useModalFocus'
import '../styles/StageDrawer.css'

export interface StageDialogProps {
  open: boolean
  title: string
  /** One sentence on what happens when the user confirms. */
  description: string
  confirmLabel: string
  tone?: 'default' | 'destructive'
  onConfirm: () => void
  onCancel: () => void
  busy?: boolean
  /** What failed and how to fix it. */
  error?: string
  /** Disables confirm (a required field is empty). */
  confirmDisabled?: boolean
  /** The form fields of the action. */
  children?: ReactNode
}

export function StageDialog(props: StageDialogProps) {
  if (!props.open) return null
  return <OpenDialog {...props} />
}

function OpenDialog({ title, description, confirmLabel, tone = 'default', onConfirm, onCancel, busy, error,
  confirmDisabled, children }: StageDialogProps) {
  const panelRef = useRef<HTMLFormElement>(null)
  const titleId = useId()
  const descId = useId()
  useModalFocus(panelRef, () => { if (!busy) onCancel() })
  return (
    <div className="stage-dialog__scrim" onMouseDown={e => { if (e.target === e.currentTarget && !busy) onCancel() }}>
      <form ref={panelRef} role="dialog" aria-modal="true" aria-labelledby={titleId} aria-describedby={descId}
        className="stage-dialog" onSubmit={e => { e.preventDefault(); if (!busy && !confirmDisabled) onConfirm() }}>
        <h2 id={titleId} className="stage-dialog__title">{title}</h2>
        <p id={descId} className="stage-dialog__description">{description}</p>
        {children}
        {error && <p role="alert" className="stage-dialog__error">{error}</p>}
        <div className="stage-dialog__buttons">
          <button type="button" className="stage-button stage-button--secondary" disabled={busy} onClick={onCancel}>Cancel</button>
          <button type="submit" className={`stage-button stage-button--${tone === 'destructive' ? 'destructive' : 'primary'}`}
            disabled={busy || confirmDisabled} aria-busy={busy || undefined}>
            {confirmLabel}
          </button>
        </div>
      </form>
    </div>
  )
}
