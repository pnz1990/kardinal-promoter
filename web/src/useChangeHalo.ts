// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// useChangeHalo.ts — the change highlight (U7, DESIGN-SYSTEM.md §3.6): when a
// polled value changes, its card gets one halo (--k-dur-slow). With reduced
// motion the halo is an outline that stays for one poll. The first value a
// card shows ("" until it has one) is not a change.
import { useEffect, useRef, useState } from 'react'
import './styles/ChangeHalo.css'

/** How long a changed card stays marked: one poll. */
export const CHANGE_MARK_MS = 5000

/**
 * useChangeHalo returns true while value has just changed (for
 * CHANGE_MARK_MS), so the caller sets data-changed on its card. value is
 * what the card shows, as a string (state, version, count). A change of
 * scope (the card now shows something else, such as another environment) is
 * not a change of value.
 */
export function useChangeHalo(value: string, scope = ''): boolean {
  const prev = useRef({ value, scope })
  const [changed, setChanged] = useState(false)
  useEffect(() => {
    const p = prev.current
    prev.current = { value, scope }
    if (p.scope !== scope) { setChanged(false); return }
    // "" is no value yet: the first value a card shows is not a change.
    if (p.value === value || p.value === '') return
    setChanged(true)
    const t = window.setTimeout(() => setChanged(false), CHANGE_MARK_MS)
    return () => window.clearTimeout(t)
  }, [value, scope])
  return changed
}
