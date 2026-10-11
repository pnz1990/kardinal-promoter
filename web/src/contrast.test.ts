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

/** OKLab of a #RRGGBB colour (Björn Ottosson's matrices). */
export function oklab(hex: string): [number, number, number] {
  const m = /^#([0-9a-f]{6})$/i.exec(hex)
  if (!m) throw new Error(`not a #RRGGBB colour: ${hex}`)
  const [r, g, b] = [0, 2, 4].map((i) => {
    const c = parseInt(m[1].slice(i, i + 2), 16) / 255
    return c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4
  })
  const l = Math.cbrt(0.4122214708 * r + 0.5363325363 * g + 0.0514459929 * b)
  const mm = Math.cbrt(0.2119034982 * r + 0.6806995451 * g + 0.1073969566 * b)
  const s = Math.cbrt(0.0883024619 * r + 0.2817188376 * g + 0.6299787005 * b)
  return [
    0.2104542553 * l + 0.793617785 * mm - 0.0040720468 * s,
    1.9779984951 * l - 2.428592205 * mm + 0.4505937099 * s,
    0.0259040371 * l + 0.7827717662 * mm - 0.808675766 * s,
  ]
}

/** OKLab distance and hue angle (degrees) between two colours. */
export function separation(a: string, b: string): { distance: number; hue: number } {
  const [x, y] = [oklab(a), oklab(b)]
  const h = (c: number[]) => (Math.atan2(c[2], c[1]) * 180) / Math.PI
  return {
    distance: Math.hypot(x[0] - y[0], x[1] - y[1], x[2] - y[2]),
    hue: Math.abs(((h(x) - h(y) + 540) % 360) - 180),
  }
}

describe('failure is not the thread', () => {
  // DESIGN-SYSTEM §2.2: red means the change you follow; failure (garnet) must not read as
  // it, at button and chip size, in either theme.
  const tokens = loadTokens()
  it.each((['light', 'dark'] as const).flatMap((theme) =>
    ['color.thread.DEFAULT', 'color.thread.text'].map((thread) => ({ theme, thread }))))('garnet vs $thread ($theme)', ({ theme, thread }) => {
    const garnet = String(resolve(tokens, 'color.status.failed.fg', theme))
    const red = String(resolve(tokens, thread, theme))
    const { distance, hue } = separation(garnet, red)
    expect(hue, `${garnet} and ${red} are ${hue.toFixed(1)}° of hue apart`).toBeGreaterThanOrEqual(40)
    expect(distance, `${garnet} and ${red} are ${distance.toFixed(3)} apart in OKLab`).toBeGreaterThanOrEqual(0.12)
  })
})

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
