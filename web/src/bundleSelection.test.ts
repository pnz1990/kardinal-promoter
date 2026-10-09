// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// bundleSelection.test.ts — the ordering and "which bundle is shown" rules.

import { describe, it, expect } from 'vitest'
import { pickDefaultBundle, resolveShownBundle, sortBundlesNewestFirst } from './bundleSelection'
import type { Bundle } from './types'

const b = (name: string, phase: string, createdAt?: string): Bundle => ({
  name, namespace: 'default', phase, type: 'image', pipeline: 'app', createdAt,
})

describe('sortBundlesNewestFirst', () => {
  it.each([
    {
      name: 'by createdAt, newest first',
      in: [b('a', 'Verified', '2026-01-01T00:00:00Z'), b('c', 'Verified', '2026-01-03T00:00:00Z'), b('b', 'Verified', '2026-01-02T00:00:00Z')],
      want: ['c', 'b', 'a'],
    },
    {
      name: 'undated bundles last, by name descending',
      in: [b('app-100', 'Verified'), b('x', 'Verified', '2026-01-01T00:00:00Z'), b('app-200', 'Verified')],
      want: ['x', 'app-200', 'app-100'],
    },
    {
      name: 'same createdAt falls back to the name',
      in: [b('app-1', 'Verified', '2026-01-01T00:00:00Z'), b('app-2', 'Verified', '2026-01-01T00:00:00Z')],
      want: ['app-2', 'app-1'],
    },
  ])('$name', ({ in: input, want }) => {
    const before = input.map(x => x.name)
    expect(sortBundlesNewestFirst(input).map(x => x.name)).toEqual(want)
    expect(input.map(x => x.name)).toEqual(before) // does not sort in place
  })
})

describe('pickDefaultBundle', () => {
  const old = b('app-1', 'Verified', '2026-01-01T00:00:00Z')
  const promoting = b('app-2', 'Promoting', '2026-01-02T00:00:00Z')
  const failed = b('app-3', 'Failed', '2026-01-03T00:00:00Z')
  const superseded = b('app-4', 'Superseded', '2026-01-04T00:00:00Z')

  it.each([
    { name: 'no bundles', bundles: [], active: undefined, want: undefined },
    { name: 'the pipeline active bundle wins', bundles: [old, promoting, failed], active: 'app-1', want: 'app-1' },
    { name: 'an active bundle that is not in the list is ignored', bundles: [old, promoting], active: 'gone', want: 'app-2' },
    { name: 'then the newest bundle that is not Superseded', bundles: [old, failed, superseded], active: undefined, want: 'app-3' },
    // E2E-R15: a newer Failed bundle must not hide behind an older Promoting
    // or Verified one; the phase does not matter, only the age.
    { name: 'a newer Failed bundle beats an older Promoting bundle', bundles: [old, promoting, failed, superseded], active: undefined, want: 'app-3' },
    { name: 'a newer Verified bundle beats an older Promoting bundle', bundles: [promoting, b('app-5', 'Verified', '2026-01-05T00:00:00Z')], active: undefined, want: 'app-5' },
    { name: 'an older Promoting bundle beats a newer Superseded bundle', bundles: [old, promoting, superseded], active: undefined, want: 'app-2' },
    { name: 'a newer Rejected bundle is skipped like a Superseded one', bundles: [old, promoting, b('app-5', 'Rejected', '2026-01-05T00:00:00Z')], active: undefined, want: 'app-2' },
    { name: 'then the newest bundle', bundles: [b('s1', 'Superseded', '2026-01-01T00:00:00Z'), b('s2', 'Superseded', '2026-01-02T00:00:00Z')], active: undefined, want: 's2' },
  ])('$name', ({ bundles, active, want }) => {
    expect(pickDefaultBundle(bundles, active)?.name).toBe(want)
  })
})

describe('resolveShownBundle', () => {
  const bundles = [b('app-1', 'Verified', '2026-01-01T00:00:00Z'), b('app-2', 'Promoting', '2026-01-02T00:00:00Z')]

  it.each([
    { name: 'keeps the user selection while it exists', selected: 'app-1', want: 'app-1' },
    { name: 'falls back to the default when the selection is gone', selected: 'deleted', want: 'app-2' },
    { name: 'uses the default when nothing is selected', selected: undefined, want: 'app-2' },
  ])('$name', ({ selected, want }) => {
    expect(resolveShownBundle(bundles, selected)?.name).toBe(want)
  })
})
