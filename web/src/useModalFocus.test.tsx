// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// useModalFocus.test.tsx — focus handling shared by all aria-modal dialogs.

import { describe, it, expect, vi } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import { useRef, useState } from 'react'
import { useModalFocus } from './useModalFocus'

function Dialog({ onClose }: { onClose: () => void }) {
  const ref = useRef<HTMLDivElement>(null)
  useModalFocus(ref, onClose)
  return (
    <div role="dialog" aria-modal="true" ref={ref}>
      <button>first</button>
      <input aria-label="middle" />
      <button disabled>disabled</button>
      <button>last</button>
    </div>
  )
}

function Harness({ onClose = () => {} }: { onClose?: () => void }) {
  const [open, setOpen] = useState(false)
  return (
    <>
      <button onClick={() => setOpen(true)}>open</button>
      {open && <Dialog onClose={() => { onClose(); setOpen(false) }} />}
    </>
  )
}

function openDialog(onClose?: () => void) {
  render(<Harness onClose={onClose} />)
  const opener = screen.getByRole('button', { name: 'open' })
  opener.focus()
  fireEvent.click(opener)
  return opener
}

describe('useModalFocus', () => {
  it('moves focus to the first control when the dialog opens', () => {
    openDialog()
    expect(screen.getByRole('button', { name: 'first' })).toHaveFocus()
  })

  it.each([
    { name: 'Tab on the last control wraps to the first', from: 'last', shiftKey: false, to: 'first' },
    { name: 'Shift+Tab on the first control wraps to the last', from: 'first', shiftKey: true, to: 'last' },
  ])('$name', ({ from, shiftKey, to }) => {
    openDialog()
    screen.getByRole('button', { name: from }).focus()
    fireEvent.keyDown(document.activeElement!, { key: 'Tab', shiftKey })
    expect(screen.getByRole('button', { name: to })).toHaveFocus()
  })

  it('leaves Tab alone between controls in the middle', () => {
    openDialog()
    screen.getByRole('button', { name: 'first' }).focus()
    const ev = new KeyboardEvent('keydown', { key: 'Tab', bubbles: true, cancelable: true })
    document.activeElement!.dispatchEvent(ev)
    expect(ev.defaultPrevented).toBe(false)
  })

  it('pulls focus back in when it has escaped the dialog', () => {
    const opener = openDialog()
    opener.focus()
    fireEvent.keyDown(opener, { key: 'Tab' })
    expect(screen.getByRole('button', { name: 'first' })).toHaveFocus()
  })

  it('closes on Escape and returns focus to the opener', () => {
    const onClose = vi.fn()
    const opener = openDialog(onClose)
    fireEvent.keyDown(document.activeElement!, { key: 'Escape' })
    expect(onClose).toHaveBeenCalledOnce()
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(opener).toHaveFocus()
  })

  it('stops Escape from reaching handlers outside the dialog', () => {
    const outside = vi.fn()
    document.addEventListener('keydown', outside)
    openDialog()
    fireEvent.keyDown(document.activeElement!, { key: 'Escape' })
    expect(outside).not.toHaveBeenCalled()
    document.removeEventListener('keydown', outside)
  })
})
