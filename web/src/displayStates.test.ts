// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

import { describe, expect, it } from 'vitest'
import { DISPLAY_STATES, displayState, heldByGate, held, words, type DisplayKind } from './displayStates.gen'

// P2: the UI's copy of pkg/display (generated; TestTypeScriptTable keeps it
// in step). These check the TypeScript half behaves like Go's.
describe('displayStates.gen', () => {
  it('labels every known state in words, never CamelCase', () => {
    for (const kind of Object.keys(DISPLAY_STATES) as DisplayKind[]) {
      for (const [value, st] of Object.entries(DISPLAY_STATES[kind])) {
        expect(st.value).toBe(value)
        expect(st.label).not.toMatch(/[a-z][A-Z]/)
        expect(st.tone).not.toBe('unknown')
      }
    }
  })

  it('names the states the UX audit names', () => {
    expect(displayState('step', 'WaitingForMerge').label).toBe('Waiting for PR merge')
    expect(displayState('step', 'HealthChecking').label).toBe('Checking health')
    expect(displayState('step', 'Verified').label).toBe('Live')
    expect(displayState('step', 'AbortedByAlarm').label).toBe('Stopped by alarm')
    expect(displayState('step', 'Superseded').label).toBe('Replaced by newer version')
    expect(displayState('bundle', 'Verified').label).toBe('Verified')
    expect(heldByGate('Pending', 'no-weekend').label).toBe('Held by gate no-weekend')
    expect(heldByGate('Pending', 'no-weekend').tone).toBe('blocked')
    expect(held('Verified').label).toBe('Held by rollback')
  })

  it('splits an unknown value into words, like Go display.Words', () => {
    expect(words('WaitingForMerge')).toBe('Waiting for merge')
    expect(words('PROpen')).toBe('PR open')
    expect(words('Waiting for PR merge')).toBe('Waiting for PR merge')
    const st = displayState('step', 'SomethingNewHere')
    expect(st).toEqual({ value: 'SomethingNewHere', label: 'Something new here', glyph: '?', ascii: '?', tone: 'unknown' })
    expect(displayState('bundle', '').label).toBe('Unknown')
  })
})
