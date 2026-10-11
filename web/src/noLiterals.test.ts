// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

/// <reference types="node" />
// noLiterals.test.ts — a ratchet on hard-coded colours and lengths in web/src. Colours,
// space, type sizes and radii come from the design tokens (var(--k-*) in CSS, tokens.ts in
// code). noLiterals.allow.json records how many literals each file had when the tokens
// landed; a file may only go down, and a file that is not listed must have none. When a
// count goes down, lower it in the allowlist (the test says so), so it cannot creep back.
//
// Counted, outside comments: colour literals (#hex, rgb(), hsl(), oklch()) anywhere, and
// px/rem lengths other than 0 — in CSS declarations, and in string literals in TS/TSX
// (inline styles). A px value inside an @container prelude is allowed when it is one of
// the container thresholds in tokens.json. Generated files and tests are not scanned.
import fs from 'node:fs'
import path from 'node:path'
import { describe, it, expect } from 'vitest'
import { container } from './tokens'
import allow from './noLiterals.allow.json'

const SRC = path.dirname(new URL(import.meta.url).pathname)
const GENERATED = new Set(['styles/tokens.css', 'tokens.ts'])

const COLOR = /#[0-9a-fA-F]{3,8}\b|\b(?:rgba?|hsla?|oklch)\(/g
const LENGTH = /(?<![\w.#-])-?(?:\d*\.)?\d+(?:px|rem)\b/g
const containerPx = new Set(Object.values(container).map((v) => `${v}px`))

function stripComments(s: string): string {
  return s.replace(/\/\*[\s\S]*?\*\//g, ' ').replace(/(^|[^:"'`\\])\/\/.*$/gm, '$1')
}

function lengths(found: RegExpMatchArray | null): number {
  return (found ?? []).filter((m) => Number.parseFloat(m) !== 0).length
}

/** The literal count of one file (see the header). */
export function countLiterals(file: string, text: string): number {
  const s = stripComments(text)
  let n = (s.match(COLOR) ?? []).length
  if (file.endsWith('.css')) {
    // @container preludes: only the token thresholds.
    for (const m of s.matchAll(/@container[^{]*\{/g)) {
      n += lengths(m[0].match(LENGTH)) - (m[0].match(LENGTH) ?? []).filter((x) => containerPx.has(x)).length
    }
    for (const m of s.matchAll(/[\w-]+\s*:\s*([^;{}]+)[;}]/g)) n += lengths(m[1].match(LENGTH))
  } else {
    for (const m of s.matchAll(/(['"`])((?:\\.|(?!\1).)*)\1/g)) n += lengths(m[2].match(LENGTH))
  }
  return n
}

function sources(dir: string): string[] {
  return fs.readdirSync(dir, { withFileTypes: true }).flatMap((e) => {
    const p = path.join(dir, e.name)
    if (e.isDirectory()) return sources(p)
    if (!/\.(css|tsx?)$/.test(e.name) || /\.test\.tsx?$/.test(e.name) || e.name === 'test-setup.ts' || e.name.endsWith('.d.ts')) return []
    return [p]
  })
}

export function currentCounts(): Record<string, number> {
  const out: Record<string, number> = {}
  for (const f of sources(SRC)) {
    const rel = path.relative(SRC, f).split(path.sep).join('/')
    if (GENERATED.has(rel)) continue
    const n = countLiterals(rel, fs.readFileSync(f, 'utf8'))
    if (n > 0) out[rel] = n
  }
  return out
}

describe('hard-coded colours and lengths (ratchet)', () => {
  const allowed = allow as Record<string, number>
  const counts = currentCounts()

  it('counts colours, lengths and container thresholds as described', () => {
    expect(countLiterals('a.css', '.a { color: #fff; margin: 0; padding: 4px 0.5rem; } /* 9px */')).toBe(3)
    expect(countLiterals('a.css', '@container map (min-width: 480px) { .a { gap: var(--k-space-2); } }')).toBe(0)
    expect(countLiterals('a.css', '@container map (min-width: 500px) { .a { gap: 0; } }')).toBe(1)
    expect(countLiterals('a.tsx', "const s = { padding: '4px', color: 'var(--k-ink)', top: 0 } // 2px")).toBe(1)
    expect(countLiterals('a.tsx', "const pr = `#42`; const id = '#main-content'")).toBe(0)
  })

  it.each(Object.keys({ ...allowed, ...counts }).sort())('%s', (file) => {
    const n = counts[file] ?? 0
    const max = allowed[file] ?? 0
    expect(n, `${file} has ${n} hard-coded colours/lengths, more than the ${max} allowed: use the --k-* tokens`).toBeLessThanOrEqual(max)
    expect(n, `${file} is down to ${n}: lower its entry in noLiterals.allow.json to ${n}${n === 0 ? ' (remove it)' : ''}`).toBe(max)
  })
})
