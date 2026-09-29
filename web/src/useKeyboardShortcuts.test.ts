// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// useKeyboardShortcuts.test.ts — unit tests for the global keyboard shortcut hook (#746, #800).
import { renderHook } from '@testing-library/react'
import { describe, it, expect, vi, beforeEach, type Mock } from 'vitest'
import { useKeyboardShortcuts } from './useKeyboardShortcuts'

type HandlerMock = Mock<() => void>

function pressKey(key: string) {
  const event = new KeyboardEvent('keydown', { key, bubbles: true })
  document.dispatchEvent(event)
}

describe('useKeyboardShortcuts', () => {
  let onHelp: HandlerMock
  let onRefresh: HandlerMock
  let onEscape: HandlerMock
  let onSearch: HandlerMock

  beforeEach(() => {
    onHelp = vi.fn()
    onRefresh = vi.fn()
    onEscape = vi.fn()
    onSearch = vi.fn()
  })

  function makeHandlers() {
    return {
      onHelp,
      onRefresh,
      onEscape,
      onSearch,
    }
  }

  it('calls onHelp when ? is pressed', () => {
    renderHook(() => useKeyboardShortcuts(makeHandlers()))
    pressKey('?')
    expect(onHelp).toHaveBeenCalledOnce()
  })

  it('calls onRefresh when r is pressed', () => {
    renderHook(() => useKeyboardShortcuts(makeHandlers()))
    pressKey('r')
    expect(onRefresh).toHaveBeenCalledOnce()
  })

  it('calls onRefresh when R is pressed (case-insensitive)', () => {
    renderHook(() => useKeyboardShortcuts(makeHandlers()))
    pressKey('R')
    expect(onRefresh).toHaveBeenCalledOnce()
  })

  it('calls onEscape when Escape is pressed', () => {
    renderHook(() => useKeyboardShortcuts(makeHandlers()))
    pressKey('Escape')
    expect(onEscape).toHaveBeenCalledOnce()
  })

  // #800: / shortcut tests
  it('calls onSearch when / is pressed', () => {
    renderHook(() => useKeyboardShortcuts(makeHandlers()))
    pressKey('/')
    expect(onSearch).toHaveBeenCalledOnce()
  })

  it('does not call any handler for / when onSearch is not provided', () => {
    const handlersNoSearch = {
      onHelp,
      onRefresh,
      onEscape,
      // onSearch omitted
    }
    renderHook(() => useKeyboardShortcuts(handlersNoSearch))
    // Should not throw
    pressKey('/')
    expect(onHelp).not.toHaveBeenCalled()
    expect(onRefresh).not.toHaveBeenCalled()
    expect(onEscape).not.toHaveBeenCalled()
  })

  it('suppresses / when an input element has focus (O2)', () => {
    renderHook(() => useKeyboardShortcuts(makeHandlers()))
    const input = document.createElement('input')
    document.body.appendChild(input)
    input.focus()
    const event = new KeyboardEvent('keydown', { key: '/', bubbles: true })
    input.dispatchEvent(event)
    expect(onSearch).not.toHaveBeenCalled()
    document.body.removeChild(input)
  })

  it('suppresses ? when an input element has focus', () => {
    renderHook(() => useKeyboardShortcuts(makeHandlers()))
    const input = document.createElement('input')
    document.body.appendChild(input)
    input.focus()
    // Dispatch keydown directly on the input so event.target is the input element.
    const event = new KeyboardEvent('keydown', { key: '?', bubbles: true })
    input.dispatchEvent(event)
    // onHelp must NOT be called when an input has focus.
    expect(onHelp).not.toHaveBeenCalled()
    document.body.removeChild(input)
  })

  it('suppresses r when a textarea has focus', () => {
    renderHook(() => useKeyboardShortcuts(makeHandlers()))
    const textarea = document.createElement('textarea')
    document.body.appendChild(textarea)
    textarea.focus()
    const event = new KeyboardEvent('keydown', { key: 'r', bubbles: true })
    textarea.dispatchEvent(event)
    expect(onRefresh).not.toHaveBeenCalled()
    document.body.removeChild(textarea)
  })

  it.each([
    { name: 'Ctrl+R (browser reload)', init: { key: 'r', ctrlKey: true } },
    { name: 'Cmd+R (browser reload)', init: { key: 'r', metaKey: true } },
    { name: 'Alt+R', init: { key: 'r', altKey: true } },
  ])('leaves $name to the browser', ({ init }) => {
    renderHook(() => useKeyboardShortcuts(makeHandlers()))
    const event = new KeyboardEvent('keydown', { ...init, bubbles: true, cancelable: true })
    document.dispatchEvent(event)
    expect(onRefresh).not.toHaveBeenCalled()
    expect(event.defaultPrevented).toBe(false)
  })

  it.each([
    { tag: 'select', key: 'r', handler: () => onRefresh },
    { tag: 'input', key: 'Escape', handler: () => onEscape },
  ])('suppresses $key when a $tag has focus', ({ tag, key, handler }) => {
    renderHook(() => useKeyboardShortcuts(makeHandlers()))
    const el = document.createElement(tag)
    document.body.appendChild(el)
    el.focus()
    el.dispatchEvent(new KeyboardEvent('keydown', { key, bubbles: true }))
    expect(handler()).not.toHaveBeenCalled()
    document.body.removeChild(el)
  })

  it.each(['/', 'r', '?', 'Escape'])('suppresses %s while a modal dialog is open', key => {
    renderHook(() => useKeyboardShortcuts(makeHandlers()))
    const dialog = document.createElement('div')
    dialog.setAttribute('aria-modal', 'true')
    document.body.appendChild(dialog)
    pressKey(key)
    expect(onSearch).not.toHaveBeenCalled()
    expect(onRefresh).not.toHaveBeenCalled()
    expect(onHelp).not.toHaveBeenCalled()
    expect(onEscape).not.toHaveBeenCalled()
    document.body.removeChild(dialog)
  })

  it('removes the keydown listener on unmount', () => {
    const { unmount } = renderHook(() => useKeyboardShortcuts(makeHandlers()))
    unmount()
    pressKey('?')
    expect(onHelp).not.toHaveBeenCalled()
  })
})
