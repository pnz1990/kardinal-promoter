// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// components/ConfirmDialog.tsx — the one confirmation dialog for actions that
// change a pipeline (pause, resume, promote, rollback). Focus starts on Cancel,
// Tab stays inside, Esc cancels, and focus returns to the button that opened it.

import type { ReactNode } from 'react'
import { ConfirmDialog as UIConfirmDialog } from '../ui'

export interface ConfirmDialogProps {
  /** The question, e.g. "Promote app-abc to prod?". */
  title: string
  /** One sentence on what happens when the user confirms. */
  description: string
  /** Label of the confirm button; says exactly what it does. */
  confirmLabel: string
  /** A destructive (garnet outline) confirm button for actions that stop or undo work. */
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

/** The confirmation dialog, on the Dialog primitive (src/ui). Rendered only while open. */
export function ConfirmDialog({ title, description, confirmLabel, danger, onConfirm, onCancel, loading, error, children }: ConfirmDialogProps) {
  return (
    <UIConfirmDialog
      open
      title={title}
      body={description}
      confirmLabel={confirmLabel}
      tone={danger ? 'destructive' : 'default'}
      onConfirm={onConfirm}
      onCancel={onCancel}
      busy={loading}
      error={error}
    >
      {children}
    </UIConfirmDialog>
  )
}

/** Turn an API error into a sentence that says what failed. */
export function actionErrorMessage(action: string, err: unknown): string {
  const detail = err instanceof Error ? err.message : String(err)
  return `Could not ${action}: ${detail}`
}
