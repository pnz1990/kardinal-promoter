// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

/// <reference types="node" />
// tokens.test.ts — the generated token files match web/tokens/tokens.json (drift test), and
// the docs' hand-written mapping uses tokens only.
import fs from 'node:fs'
import path from 'node:path'
import { describe, it, expect } from 'vitest'
import { DOCS_CSS, ROOT, WEB_CSS, WEB_TS, fontCopies, loadTokens, render } from '../../scripts/gen-tokens.mjs'

const read = (rel: string) => fs.readFileSync(path.join(ROOT, rel), 'utf8')

describe('design tokens', () => {
  const files = render(loadTokens())

  it.each([WEB_CSS, WEB_TS, DOCS_CSS])('%s is generated from tokens.json (run `make tokens`)', (rel) => {
    expect(read(rel)).toBe(files[rel])
  })

  it('the docs carry the same fonts and licenses as the UI', () => {
    const copies = fontCopies()
    expect(Object.keys(copies).filter((f) => f.endsWith('.woff2'))).toHaveLength(3)
    expect(Object.keys(copies).filter((f) => f.endsWith('-OFL.txt'))).toHaveLength(3)
    for (const [docs, web] of Object.entries(copies)) {
      expect(fs.readFileSync(path.join(ROOT, docs)).equals(fs.readFileSync(path.join(ROOT, web))), docs).toBe(true)
    }
  })

  it('every font the stylesheets load is vendored, with its license', () => {
    const css = files[WEB_CSS]
    const urls = [...css.matchAll(/url\("\.\.\/assets\/fonts\/([^"]+)"\)/g)].map((m) => m[1])
    expect(new Set(urls).size).toBe(3)
    for (const f of urls) {
      expect(fs.existsSync(path.join(ROOT, 'web/src/assets/fonts', f)), f).toBe(true)
      const license = f.replace(/-Variable\.woff2$/, '-OFL.txt')
      expect(read(path.join('web/src/assets/fonts', license))).toContain('SIL OPEN FONT LICENSE Version 1.1')
    }
  })

  it('the fonts stay within the 180 KB budget', () => {
    const dir = path.join(ROOT, 'web/src/assets/fonts')
    const total = fs.readdirSync(dir).filter((f) => f.endsWith('.woff2')).reduce((n, f) => n + fs.statSync(path.join(dir, f)).size, 0)
    expect(total).toBeLessThanOrEqual(180 * 1024)
  })

  it('docs/stylesheets/kardinal.css maps to tokens and holds no literal colour or length', () => {
    const css = read('docs/stylesheets/kardinal.css').replace(/\/\*[\s\S]*?\*\//g, '')
    expect(css).not.toMatch(/#[0-9a-fA-F]{3,8}\b/)
    expect(css).not.toMatch(/\b(rgba?|hsla?|oklch)\(/)
    expect(css).not.toMatch(/\d(px|rem)\b/)
    const defined = new Set([...files[DOCS_CSS].matchAll(/(--k-[\w-]+):/g)].map((m) => m[1]))
    for (const [, name] of css.matchAll(/var\((--k-[\w-]+)/g)) {
      expect(defined.has(name), `${name} is not a token`).toBe(true)
    }
  })

  it('every --k-* name the UI uses is a token', () => {
    const defined = new Set([...files[WEB_CSS].matchAll(/(--k-[\w-]+):/g)].map((m) => m[1]))
    const src = path.join(ROOT, 'web/src')
    const walk = (d: string): string[] =>
      fs.readdirSync(d, { withFileTypes: true }).flatMap((e) =>
        e.isDirectory() ? walk(path.join(d, e.name)) : /\.(css|tsx?)$/.test(e.name) ? [path.join(d, e.name)] : [])
    for (const f of walk(src)) {
      if (f.endsWith(path.join('styles', 'tokens.css'))) continue
      for (const [, name] of fs.readFileSync(f, 'utf8').matchAll(/var\((--k-[\w-]+)/g)) {
        expect(defined.has(name), `${path.relative(ROOT, f)} uses ${name}, which is not a token`).toBe(true)
      }
    }
  })
})
