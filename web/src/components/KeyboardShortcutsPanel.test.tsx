// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// KeyboardShortcutsPanel.test.tsx — Unit tests for the keyboard shortcuts modal (#746, #783).

import { render, screen, fireEvent, act } from '@testing-library/react'
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { KeyboardShortcutsPanel } from './KeyboardShortcutsPanel'

describe('KeyboardShortcutsPanel', () => {
  const onClose = vi.fn()

  beforeEach(() => {
    onClose.mockReset()
  })

  it('renders the modal with correct ARIA attributes', () => {
    render(<KeyboardShortcutsPanel onClose={onClose} />)
    const dialog = screen.getByRole('dialog')
    expect(dialog).toBeDefined()
    expect(dialog.getAttribute('aria-modal')).toBe('true')
    expect(dialog.getAttribute('aria-label')).toBe('Keyboard shortcuts')
  })

  it('renders all shortcut rows', () => {
    render(<KeyboardShortcutsPanel onClose={onClose} />)
    expect(screen.getByText('Focus pipeline search')).toBeDefined()
    expect(screen.getByText('Show / hide this help panel')).toBeDefined()
    expect(screen.getByText('Refresh data now')).toBeDefined()
    expect(screen.getByText('Close the open side panel')).toBeDefined()
  })

  it('calls onClose when close button is clicked', () => {
    render(<KeyboardShortcutsPanel onClose={onClose} />)
    const closeBtn = screen.getByLabelText('Close keyboard shortcuts')
    fireEvent.click(closeBtn)
    expect(onClose).toHaveBeenCalledOnce()
  })

  it('calls onClose when backdrop is clicked', () => {
    render(<KeyboardShortcutsPanel onClose={onClose} />)
    const dialog = screen.getByRole('dialog')
    // Simulate click on the backdrop (dialog root, not inner div)
    fireEvent.click(dialog, { target: dialog })
    expect(onClose).toHaveBeenCalledOnce()
  })

  it('focus trap: Tab from close button wraps to close button (single focusable)', () => {
    render(<KeyboardShortcutsPanel onClose={onClose} />)
    const closeBtn = screen.getByLabelText('Close keyboard shortcuts')
    // Focus is on close button (only focusable element)
    closeBtn.focus()
    // Tab from last focusable should wrap to first
    fireEvent.keyDown(document, { key: 'Tab', shiftKey: false })
    // With only one focusable, focus should still be on close button
    expect(document.activeElement).toBe(closeBtn)
  })

  it('focus trap: Shift+Tab from close button wraps backward', () => {
    render(<KeyboardShortcutsPanel onClose={onClose} />)
    const closeBtn = screen.getByLabelText('Close keyboard shortcuts')
    closeBtn.focus()
    fireEvent.keyDown(document, { key: 'Tab', shiftKey: true })
    // With only one focusable, focus should still be on close button
    expect(document.activeElement).toBe(closeBtn)
  })

  it('moves focus to close button on mount (#783 O1)', () => {
    // Create a trigger button that holds focus before modal opens
    const trigger = document.createElement('button')
    trigger.textContent = 'Trigger'
    document.body.appendChild(trigger)
    trigger.focus()
    expect(document.activeElement).toBe(trigger)

    render(<KeyboardShortcutsPanel onClose={onClose} />)

    // After mount, focus should be on close button
    const closeBtn = screen.getByLabelText('Close keyboard shortcuts')
    // useEffect runs synchronously in jsdom with act
    act(() => {})
    expect(document.activeElement).toBe(closeBtn)

    document.body.removeChild(trigger)
  })

  it('returns focus to previous element on unmount (#783 O4)', () => {
    const trigger = document.createElement('button')
    trigger.textContent = 'Trigger'
    document.body.appendChild(trigger)
    trigger.focus()

    const { unmount } = render(<KeyboardShortcutsPanel onClose={onClose} />)
    unmount()

    // Focus should return to the trigger element
    expect(document.activeElement).toBe(trigger)
    document.body.removeChild(trigger)
  })
})
