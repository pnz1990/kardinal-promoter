// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

/// <reference types="node" />
// tokens.test.ts — the generated token files match web/tokens/tokens.json (drift test), and
// the docs' hand-written mapping uses tokens only.
import fs from 'node:fs'
import path from 'node:path'
import { describe, it, expect } from 'vitest'
import zlib from 'node:zlib'
import { DOCS_CSS, PUBLIC_FONTS, ROOT, WEB_CSS, WEB_TS, fontCopies, loadTokens, render } from '../../scripts/gen-tokens.mjs'

const read = (rel: string) => fs.readFileSync(path.join(ROOT, rel), 'utf8')

/** The name records (nameID → text) of a WOFF2 font: the table directory, then the brotli
 * stream that holds every table (W3C WOFF2 §5–6). */
export function woff2Names(buf: Buffer): Map<number, string[]> {
  const numTables = buf.readUInt16BE(12)
  const compressedLength = buf.readUInt32BE(20)
  let off = 48
  const base128 = () => {
    let v = 0
    for (let i = 0; i < 5; i++) {
      const b = buf[off++]
      v = v * 128 + (b & 0x7f)
      if (!(b & 0x80)) return v
    }
    throw new Error('bad UIntBase128')
  }
  const known = ['cmap', 'head', 'hhea', 'hmtx', 'maxp', 'name', 'OS/2', 'post', 'cvt ', 'fpgm', 'glyf', 'loca', 'prep', 'CFF ', 'VORG', 'EBDT', 'EBLC', 'gasp', 'hdmx', 'kern', 'LTSH', 'PCLT', 'VDMX', 'vhea', 'vmtx', 'BASE', 'GDEF', 'GPOS', 'GSUB', 'EBSC', 'JSTF', 'MATH', 'CBDT', 'CBLC', 'COLR', 'CPAL', 'SVG ', 'sbix', 'acnt', 'avar', 'bdat', 'bloc', 'bsln', 'cvar', 'fdsc', 'feat', 'fmtx', 'fvar', 'gvar', 'hsty', 'just', 'lcar', 'mort', 'morx', 'opbd', 'prop', 'trak', 'Zapf', 'Silf', 'Glat', 'Gloc', 'Feat', 'Sill']
  const tables: { tag: string; length: number }[] = []
  for (let i = 0; i < numTables; i++) {
    const flags = buf[off++]
    let tag = known[flags & 0x3f]
    if ((flags & 0x3f) === 0x3f) { tag = buf.toString('latin1', off, off + 4); off += 4 }
    const orig = base128()
    const version = flags >> 6
    const transformed = tag === 'glyf' || tag === 'loca' ? version === 0 : version !== 0
    tables.push({ tag, length: transformed ? base128() : orig })
  }
  const data = zlib.brotliDecompressSync(buf.subarray(off, off + compressedLength))
  let pos = 0
  let name: Buffer | undefined
  for (const t of tables) {
    if (t.tag === 'name') name = data.subarray(pos, pos + t.length)
    pos += t.length
  }
  if (!name) throw new Error('no name table')
  const count = name.readUInt16BE(2)
  const strings = name.readUInt16BE(4)
  const out = new Map<number, string[]>()
  for (let i = 0; i < count; i++) {
    const r = 6 + i * 12
    const platform = name.readUInt16BE(r)
    const id = name.readUInt16BE(r + 6)
    const len = name.readUInt16BE(r + 8)
    const at = strings + name.readUInt16BE(r + 10)
    const raw = name.subarray(at, at + len)
    const text = platform === 3 || platform === 0 ? Buffer.from(raw).swap16().toString('utf16le') : raw.toString('latin1')
    out.set(id, [...(out.get(id) ?? []), text])
  }
  return out
}

describe('design tokens', () => {
  const files = render(loadTokens())

  it.each([WEB_CSS, WEB_TS, DOCS_CSS])('%s is generated from tokens.json (run `make tokens`)', (rel) => {
    expect(read(rel)).toBe(files[rel])
  })

  it('the docs carry the same fonts and licenses as the UI, and web/public/fonts the licenses', () => {
    const copies = fontCopies()
    expect(Object.keys(copies).filter((f) => f.endsWith('.woff2'))).toHaveLength(3)
    expect(Object.keys(copies).filter((f) => f.startsWith('docs/') && f.endsWith('-OFL.txt'))).toHaveLength(3)
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

  it.each(['Archivo', 'AtkinsonHyperlegibleNext', 'AtkinsonHyperlegibleMono'])('%s keeps its copyright and OFL name records', (font) => {
    const names = woff2Names(fs.readFileSync(path.join(ROOT, 'web/src/assets/fonts', `${font}-Variable.woff2`)))
    expect(names.get(0)?.join(' ')).toMatch(/Copyright/)
    expect(names.get(13)?.join(' ')).toMatch(/SIL Open Font License, Version 1\.1/)
    expect(names.get(14)?.join(' ')).toMatch(/scripts\.sil\.org\/OFL|openfontlicense\.org/)
  })

  it('the licenses also ship in the built UI (web/public/fonts → web/dist)', () => {
    for (const f of ['Archivo-OFL.txt', 'AtkinsonHyperlegibleNext-OFL.txt', 'AtkinsonHyperlegibleMono-OFL.txt']) {
      expect(read(path.join(PUBLIC_FONTS, f))).toBe(read(path.join('web/src/assets/fonts', f)))
    }
  })

  it('the fonts stay within the 180 KB budget', () => {
    const dir = path.join(ROOT, 'web/src/assets/fonts')
    const total = fs.readdirSync(dir).filter((f) => f.endsWith('.woff2')).reduce((n, f) => n + fs.statSync(path.join(dir, f)).size, 0)
    expect(total).toBeLessThanOrEqual(180 * 1024)
  })

  it.each(fs.readdirSync(path.join(ROOT, 'docs/stylesheets')).filter((f) => f.endsWith('.css') && f !== 'tokens.css'))('docs/stylesheets/%s maps to tokens and holds no literal colour or length', (f) => {
    const css = read(path.join('docs/stylesheets', f)).replace(/\/\*[\s\S]*?\*\//g, '')
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
