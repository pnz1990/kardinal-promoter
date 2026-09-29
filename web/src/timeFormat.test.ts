// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

import { describe, it, expect } from 'vitest'
import { formatElapsedSince } from './timeFormat'

const NOW = Date.parse('2026-09-29T12:00:00Z')

describe('formatElapsedSince', () => {
  it.each([
    { iso: undefined, want: '' },
    { iso: 'not a date', want: '' },
    { iso: '2026-09-29T12:00:10Z', want: '' },
    { iso: '2026-09-29T12:00:00Z', want: '0s' },
    { iso: '2026-09-29T11:59:18Z', want: '42s' },
    { iso: '2026-09-29T11:57:55Z', want: '2m 5s' },
    { iso: '2026-09-29T10:37:00Z', want: '1h 23m' },
  ])('$iso → "$want"', ({ iso, want }) => {
    expect(formatElapsedSince(iso, NOW)).toBe(want)
  })
})
