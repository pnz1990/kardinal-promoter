// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

import { describe, it, expect } from 'vitest'
import { versionName } from './versionName'

describe('versionName', () => {
  it.each<[string, string | undefined, string]>([
    ['kardinal-test-app-abc123', 'kardinal-test-app', 'abc123'],
    ['checkout-1-4-2', 'checkout', '1-4-2'],
    ['checkout-', 'checkout', 'checkout-'],
    ['other-abc', 'checkout', 'other-abc'],
    ['checkout-abc', undefined, 'checkout-abc'],
  ])('%s on pipeline %s reads %s', (name, pipeline, want) => {
    expect(versionName(name, pipeline)).toBe(want)
  })
})
