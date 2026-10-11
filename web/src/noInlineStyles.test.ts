// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

/// <reference types="node" />
// noInlineStyles.test.ts — a ratchet on React inline styles (`style={…}`) in web/src. Styling
// lives in CSS with the design tokens; an inline style is left only for a value computed at
// runtime (a position, a width from data, a custom property). noInlineStyles.allow.json holds
// the count each file may have; it may only go down, a file that is not listed has none, and
// the total stays at or below 60.
import fs from 'node:fs'
import path from 'node:path'
import { describe, it, expect } from 'vitest'
import allow from './noInlineStyles.allow.json'

const SRC = path.dirname(new URL(import.meta.url).pathname)
const MAX_TOTAL = 60

function stripComments(s: string): string {
  return s.replace(/\/\*[\s\S]*?\*\//g, ' ').replace(/(^|[^:"'`\\])\/\/.*$/gm, '$1')
}

/** The `style=` props in a TSX source, outside comments. */
export function countInlineStyles(text: string): number {
  return (stripComments(text).match(/\sstyle=\{/g) ?? []).length
}

function sources(dir: string): string[] {
  return fs.readdirSync(dir, { withFileTypes: true }).flatMap((e) => {
    const p = path.join(dir, e.name)
    if (e.isDirectory()) return sources(p)
    return e.name.endsWith('.tsx') && !e.name.endsWith('.test.tsx') ? [p] : []
  })
}

export function currentInlineStyles(): Record<string, number> {
  const out: Record<string, number> = {}
  for (const f of sources(SRC)) {
    const n = countInlineStyles(fs.readFileSync(f, 'utf8'))
    if (n > 0) out[path.relative(SRC, f).split(path.sep).join('/')] = n
  }
  return out
}

describe('inline styles (ratchet)', () => {
  const allowed = allow as Record<string, number>
  const counts = currentInlineStyles()

  it('counts style props, not comments or the word in text', () => {
    expect(countInlineStyles('<div style={{ left: x }} /> {/* style={{ a }} */} <p>style={x}</p> <i\n  style={s} />')).toBe(2)
  })

  it(`stays at or below ${MAX_TOTAL} in total`, () => {
    const total = Object.values(counts).reduce((a, b) => a + b, 0)
    expect(total).toBeLessThanOrEqual(MAX_TOTAL)
  })

  it.each(Object.keys({ ...allowed, ...counts }).sort())('%s', (file) => {
    const n = counts[file] ?? 0
    const max = allowed[file] ?? 0
    expect(n, `${file} has ${n} inline styles, more than the ${max} allowed: move them to CSS with --k-* tokens`).toBeLessThanOrEqual(max)
    expect(n, `${file} is down to ${n}: lower its entry in noInlineStyles.allow.json to ${n}${n === 0 ? ' (remove it)' : ''}`).toBe(max)
  })
})
