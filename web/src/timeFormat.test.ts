// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

import { describe, it, expect } from 'vitest'
import { ago, formatElapsedSince, humanDuration } from './timeFormat'

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
    // Days, not hours: the walkthrough's "live 96h 0m".
    { iso: '2026-09-25T12:00:00Z', want: '4d' },
  ])('$iso → "$want"', ({ iso, want }) => {
    expect(formatElapsedSince(iso, NOW)).toBe(want)
  })
})

describe('humanDuration', () => {
  it.each<[number, string]>([
    [42, '42s'], [252, '4m'], [3600, '1h'], [8100, '2h 15m'], [86400 + 3 * 3600, '1d 3h'], [96 * 3600, '4d'], [10 * 86400 + 5 * 3600, '10d'],
  ])('%is reads %s', (sec, want) => { expect(humanDuration(sec)).toBe(want) })
  it('keeps seconds under an hour when precise (a running timer)', () => {
    expect(humanDuration(252, { precise: true })).toBe('4m 12s')
    expect(humanDuration(180, { precise: true })).toBe('3m')
  })
})

describe('ago', () => {
  it.each<[string | undefined, string]>([
    [undefined, ''], ['2026-09-29T11:59:30Z', 'just now'], ['2026-09-29T09:45:00Z', '2h 15m ago'], ['2026-09-25T12:00:00Z', '4d ago'],
  ])('%s → %s', (iso, want) => { expect(ago(iso, NOW)).toBe(want) })
})
