// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

import { describe, it, expect } from 'vitest'
import { lowerFirst, stateWords } from './sentence'

describe('sentence', () => {
  it.each<[string, string]>([
    ['Live', 'live'],
    ['Waiting for PR merge', 'waiting for PR merge'],
    ['PR open', 'PR open'],
    ['Held by gate no-weekend', 'held by gate no-weekend'],
    ['', ''],
  ])('lowerFirst(%j) is %j, as Go Summarize writes it', (label, want) => {
    expect(lowerFirst(label)).toBe(want)
  })

  it('reads states through their display labels', () => {
    expect(stateWords('step', 'Verified')).toBe('live')
    expect(stateWords('step', 'WaitingForMerge')).toBe('waiting for PR merge')
    expect(stateWords('step', 'HealthChecking')).toBe('checking health')
  })

})
