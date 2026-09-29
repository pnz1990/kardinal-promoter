// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// ConfirmDialog.test.tsx — the shared confirmation dialog.

import { describe, it, expect, vi } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import { ConfirmDialog, actionErrorMessage } from './ConfirmDialog'

function renderDialog(extra: Partial<Parameters<typeof ConfirmDialog>[0]> = {}) {
  const onConfirm = vi.fn()
  const onCancel = vi.fn()
  render(
    <ConfirmDialog
      title="Promote app-1 to prod?"
      description="Opens a pull request that deploys app-1 to prod."
      confirmLabel="Promote to prod"
      onConfirm={onConfirm}
      onCancel={onCancel}
      {...extra}
    />,
  )
  return { onConfirm, onCancel }
}

describe('ConfirmDialog', () => {
  it('is a labelled, described modal with focus on Cancel', () => {
    renderDialog()
    const dialog = screen.getByRole('dialog', { name: 'Promote app-1 to prod?' })
    expect(dialog).toHaveAttribute('aria-modal', 'true')
    expect(dialog).toHaveAccessibleDescription('Opens a pull request that deploys app-1 to prod.')
    expect(screen.getByRole('button', { name: 'Cancel' })).toHaveFocus()
  })

  it.each([
    { name: 'confirm button', act: () => fireEvent.click(screen.getByRole('button', { name: 'Promote to prod' })), confirm: 1, cancel: 0 },
    { name: 'Cancel button', act: () => fireEvent.click(screen.getByRole('button', { name: 'Cancel' })), confirm: 0, cancel: 1 },
    { name: 'Escape', act: () => fireEvent.keyDown(document.activeElement!, { key: 'Escape' }), confirm: 0, cancel: 1 },
    { name: 'backdrop click', act: () => fireEvent.click(screen.getByRole('dialog')), confirm: 0, cancel: 1 },
  ])('$name', ({ act, confirm, cancel }) => {
    const { onConfirm, onCancel } = renderDialog()
    act()
    expect(onConfirm).toHaveBeenCalledTimes(confirm)
    expect(onCancel).toHaveBeenCalledTimes(cancel)
  })

  it('cannot be dismissed while the request runs', () => {
    const { onCancel } = renderDialog({ loading: true })
    fireEvent.keyDown(document, { key: 'Escape' })
    fireEvent.click(screen.getByRole('dialog'))
    expect(onCancel).not.toHaveBeenCalled()
    expect(screen.getByRole('button', { name: 'Promote to prod' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Promote to prod' })).toHaveTextContent('Working…')
  })

  it('shows the error once and renders details', () => {
    renderDialog({ error: 'Could not promote: API error 409: already promoting', children: <p>ghcr.io/x/app:1.2</p> })
    expect(screen.getAllByRole('alert')).toHaveLength(1)
    expect(screen.getByRole('alert')).toHaveTextContent('already promoting')
    expect(screen.getByText('ghcr.io/x/app:1.2')).toBeInTheDocument()
  })
})

describe('actionErrorMessage', () => {
  it.each([
    { err: new Error('API error 404: pipeline not found'), want: 'Could not promote app: API error 404: pipeline not found' },
    { err: 'boom', want: 'Could not promote app: boom' },
  ])('$want', ({ err, want }) => {
    expect(actionErrorMessage('promote app', err)).toBe(want)
  })
})
