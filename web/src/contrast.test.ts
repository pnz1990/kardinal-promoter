// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

/// <reference types="node" />
// contrast.test.ts — every contrast pair tokens.json declares
// ($extensions.kardinal.contrast) meets its WCAG 2.1 minimum in the light and the dark
// theme: 4.5 for text, 3 for graphics and non-text UI. A token change that breaks AA
// fails here, before anyone sees it.
import { describe, it, expect } from 'vitest'
import { flatten, loadTokens, resolve } from '../../scripts/gen-tokens.mjs'

/** WCAG 2.1 relative luminance of a #RRGGBB colour. */
export function luminance(hex: string): number {
  const m = /^#([0-9a-f]{6})$/i.exec(hex)
  if (!m) throw new Error(`not a #RRGGBB colour: ${hex}`)
  const ch = [0, 2, 4].map((i) => parseInt(m[1].slice(i, i + 2), 16) / 255)
  const [r, g, b] = ch.map((c) => (c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4))
  return 0.2126 * r + 0.7152 * g + 0.0722 * b
}

/** WCAG 2.1 contrast ratio of two colours. */
export function contrast(a: string, b: string): number {
  const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x)
  return (hi + 0.05) / (lo + 0.05)
}

describe('WCAG contrast of the tokens', () => {
  it('computes the reference ratios', () => {
    expect(contrast('#000000', '#FFFFFF')).toBeCloseTo(21, 5)
    expect(contrast('#767676', '#FFFFFF')).toBeCloseTo(4.54, 2)
  })

  const tokens = loadTokens()
  const pairs = flatten(tokens).flatMap(({ path, token }) =>
    (token.$extensions?.kardinal?.contrast ?? []).flatMap(({ on, min }) =>
      (['light', 'dark'] as const).map((theme) => ({ fg: path.join('.'), on, min, theme }))))

  it('declares pairs for text, status and graphics', () => {
    expect(pairs.length).toBeGreaterThan(60)
  })

  it.each(pairs)('$fg on $on ($theme) ≥ $min', ({ fg, on, min, theme }) => {
    const a = String(resolve(tokens, fg, theme))
    const b = String(resolve(tokens, on, theme))
    expect(contrast(a, b), `${a} on ${b}`).toBeGreaterThanOrEqual(min)
  })
})
