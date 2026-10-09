// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// useRovingFocus.ts — one Tab stop for a row of buttons, arrow keys within
// it (WAI-ARIA roving tabindex). The row's last focused item, else its first
// (or the one marked data-roving-default), has tabIndex 0 and the others -1, so
// Tab moves past the row in one step and Left/Right, Home/End move inside it.
// Up/Down are left to the caller (the fleet board moves between rows).

import { useLayoutEffect, type RefObject, type KeyboardEvent, type FocusEvent } from 'react'

/** Whether a key event carries Alt, Ctrl or Meta (Shift does not count). */
export function hasCommandModifier(e: { altKey: boolean; ctrlKey: boolean; metaKey: boolean }): boolean {
  return e.altKey || e.ctrlKey || e.metaKey
}

/** The focusable items of a row. */
export function rovingItems(row: HTMLElement | null, selector: string): HTMLElement[] {
  return row ? Array.from(row.querySelectorAll<HTMLElement>(selector)) : []
}

/** Make item the row's Tab stop (and focus it when focus is true). */
export function rovingFocus(row: HTMLElement | null, selector: string, item: HTMLElement, focus = true) {
  for (const el of rovingItems(row, selector)) {
    el.tabIndex = el === item ? 0 : -1
    el.dataset.roving = el === item ? 'active' : ''
  }
  if (focus) item.focus()
}

/**
 * useRovingFocus keeps exactly one Tab stop in the row after every render
 * (items come and go with each poll) and returns the row's handlers: keydown
 * moves inside the row, focus makes the focused item the Tab stop.
 */
export function useRovingFocus(row: RefObject<HTMLElement | null>, selector: string) {
  useLayoutEffect(() => {
    const items = rovingItems(row.current, selector)
    if (items.length === 0) return
    const current = items.find(el => el.tabIndex === 0 && el.dataset.roving === 'active')
      ?? items.find(el => el.dataset.rovingDefault === 'true')
      ?? items[0]
    for (const el of items) {
      el.tabIndex = el === current ? 0 : -1
      el.dataset.roving = el === current ? 'active' : ''
    }
  })

  const onFocus = (e: FocusEvent<HTMLElement>) => {
    const items = rovingItems(row.current, selector)
    const item = items.find(el => el === e.target)
    if (item) rovingFocus(row.current, selector, item, false)
  }

  const onKeyDown = (e: KeyboardEvent<HTMLElement>) => {
    const items = rovingItems(row.current, selector)
    const i = items.indexOf(e.target as HTMLElement)
    // Alt/Ctrl/Meta + arrow belong to the browser and assistive technology
    // (history, word moves, screen reader commands): leave them alone.
    if (i < 0 || hasCommandModifier(e)) return
    let next = -1
    switch (e.key) {
      case 'ArrowRight': next = Math.min(i + 1, items.length - 1); break
      case 'ArrowLeft': next = Math.max(i - 1, 0); break
      case 'Home': next = 0; break
      case 'End': next = items.length - 1; break
      default: return
    }
    e.preventDefault()
    rovingFocus(row.current, selector, items[next])
  }

  return { onKeyDown, onFocus }
}
