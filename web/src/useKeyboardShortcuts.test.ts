// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// useKeyboardShortcuts.test.ts — unit tests for the global keyboard shortcut hook (#746).
import { renderHook } from '@testing-library/react'
import { describe, it, expect, vi, beforeEach, type MockInstance } from 'vitest'
import { useKeyboardShortcuts } from './useKeyboardShortcuts'

// eslint-disable-next-line @typescript-eslint/no-explicit-any
type AnyMock = MockInstance<any>

function pressKey(key: string) {
  const event = new KeyboardEvent('keydown', { key, bubbles: true })
  document.dispatchEvent(event)
}

describe('useKeyboardShortcuts', () => {
  let onHelp: AnyMock
  let onRefresh: AnyMock
  let onEscape: AnyMock

  beforeEach(() => {
    onHelp = vi.fn()
    onRefresh = vi.fn()
    onEscape = vi.fn()
  })

  function makeHandlers() {
    return {
      onHelp: onHelp as unknown as () => void,
      onRefresh: onRefresh as unknown as () => void,
      onEscape: onEscape as unknown as () => void,
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

  it('removes the keydown listener on unmount', () => {
    const { unmount } = renderHook(() => useKeyboardShortcuts(makeHandlers()))
    unmount()
    pressKey('?')
    expect(onHelp).not.toHaveBeenCalled()
  })
})
