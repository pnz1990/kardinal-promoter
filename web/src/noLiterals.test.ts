// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

/// <reference types="node" />
// noLiterals.test.ts — a ratchet on hard-coded colours and lengths in web/src. Colours,
// space, type sizes and radii come from the design tokens (var(--k-*) in CSS, tokens.ts in
// code). noLiterals.allow.json records how many literals each file had when the tokens
// landed; a file may only go down, and a file that is not listed must have none. When a
// count goes down, lower it in the allowlist (the test says so), so it cannot creep back.
//
// Counted, outside comments:
//   - colour literals anywhere: #hex, rgb(), hsl(), hwb(), lab(), lch(), oklab(), oklch(), color();
//   - named colours (white, red, …) in CSS values, in style values in TS/TSX
//     (`color: 'white'`) and in SVG colour attributes (`fill="white"`);
//   - px/rem lengths other than 0: in CSS declarations, in @media preludes, and in
//     string literals in TS/TSX (inline styles);
//   - numbers on length keys in TS/TSX objects (`padding: 4`, `fontSize: 12`), which React
//     renders as px, and numeric size attributes in JSX (`rx={4}`, `width={180}`,
//     `strokeWidth={1.5}`): geometry comes from tokens.ts.
// A px value inside an @container prelude is allowed when it is one of the container
// thresholds in tokens.json. Generated files and tests are not scanned.
import fs from 'node:fs'
import path from 'node:path'
import { describe, it, expect } from 'vitest'
import { container } from './tokens'
import allow from './noLiterals.allow.json'

const SRC = path.dirname(new URL(import.meta.url).pathname)
const GENERATED = new Set(['styles/tokens.css', 'tokens.ts'])

const COLOR = /#[0-9a-fA-F]{3,8}\b|\b(?:rgba?|hsla?|hwb|lab|lch|oklab|oklch|color)\(/g
const LENGTH = /(?<![\w.#-])-?(?:\d*\.)?\d+(?:px|rem)\b/g
const containerPx = new Set(Object.values(container).map((v) => `${v}px`))

/** The CSS named colours (CSS Color 4), not transparent or currentColor. */
const NAMED = 'aliceblue antiquewhite aqua aquamarine azure beige bisque black blanchedalmond blue blueviolet brown burlywood cadetblue chartreuse chocolate coral cornflowerblue cornsilk crimson cyan darkblue darkcyan darkgoldenrod darkgray darkgreen darkgrey darkkhaki darkmagenta darkolivegreen darkorange darkorchid darkred darksalmon darkseagreen darkslateblue darkslategray darkslategrey darkturquoise darkviolet deeppink deepskyblue dimgray dimgrey dodgerblue firebrick floralwhite forestgreen fuchsia gainsboro ghostwhite gold goldenrod gray green greenyellow grey honeydew hotpink indianred indigo ivory khaki lavender lavenderblush lawngreen lemonchiffon lightblue lightcoral lightcyan lightgoldenrodyellow lightgray lightgreen lightgrey lightpink lightsalmon lightseagreen lightskyblue lightslategray lightslategrey lightsteelblue lightyellow lime limegreen linen magenta maroon mediumaquamarine mediumblue mediumorchid mediumpurple mediumseagreen mediumslateblue mediumspringgreen mediumturquoise mediumvioletred midnightblue mintcream mistyrose moccasin navajowhite navy oldlace olive olivedrab orange orangered orchid palegoldenrod palegreen paleturquoise palevioletred papayawhip peachpuff peru pink plum powderblue purple rebeccapurple red rosybrown royalblue saddlebrown salmon sandybrown seagreen seashell sienna silver skyblue slateblue slategray slategrey snow springgreen steelblue tan teal thistle tomato turquoise violet wheat white whitesmoke yellow yellowgreen'
const NAMED_RE = new RegExp(`(?<![\\w-])(?:${NAMED.split(' ').join('|')})(?![\\w-])`, 'gi')

/** Style keys that hold a colour (TS/TSX style objects). */
const COLOR_KEYS = 'color|background|backgroundColor|border|borderColor|borderTop|borderRight|borderBottom|borderLeft|borderTopColor|borderRightColor|borderBottomColor|borderLeftColor|outline|outlineColor|boxShadow|textShadow|fill|stroke|caretColor|accentColor|textDecorationColor|columnRuleColor'
const STYLE_COLOR = new RegExp(`\\b(?:${COLOR_KEYS})\\s*:\\s*(['"\`])((?:\\\\.|(?!\\1).)*)\\1`, 'g')
const ATTR_COLOR = /\b(?:fill|stroke|color|stopColor|floodColor|lightingColor)=(?:"([^"]*)"|'([^']*)'|\{\s*(['"`])([^'"`]*)\3\s*\})/g

/** Style keys that hold a length: React renders a bare number as px. */
const LENGTH_KEYS = 'width|height|minWidth|maxWidth|minHeight|maxHeight|top|right|bottom|left|inset|insetBlock|insetInline|margin|marginTop|marginRight|marginBottom|marginLeft|marginBlock|marginInline|padding|paddingTop|paddingRight|paddingBottom|paddingLeft|paddingBlock|paddingInline|gap|rowGap|columnGap|fontSize|letterSpacing|borderRadius|borderWidth|borderTopWidth|borderBottomWidth|borderLeftWidth|borderRightWidth|outlineWidth|outlineOffset|flexBasis|blockSize|inlineSize|minBlockSize|minInlineSize|maxBlockSize|maxInlineSize|textIndent|wordSpacing'
const STYLE_NUMBER = new RegExp(`\\b(?:${LENGTH_KEYS})\\s*:\\s*(-?(?:\\d*\\.)?\\d+)(?![\\w.%'"])`, 'g')

/** SVG and element size attributes given as literal numbers. */
const ATTR_NUMBER = /\b(?:rx|ry|r|width|height|strokeWidth|fontSize|x|y|cx|cy|dx|dy|x1|x2|y1|y2|strokeDasharray|strokeDashoffset)=(?:\{\s*(-?(?:\d*\.)?\d+)\s*\}|"(-?(?:\d*\.)?\d+)")/g

function stripComments(s: string): string {
  return s.replace(/\/\*[\s\S]*?\*\//g, ' ').replace(/(^|[^:"'`\\])\/\/.*$/gm, '$1')
}

function lengths(found: RegExpMatchArray | null): number {
  return (found ?? []).filter((m) => Number.parseFloat(m) !== 0).length
}

const nonZero = (v: string | undefined) => v !== undefined && Number.parseFloat(v) !== 0

/** The literal count of one file (see the header). */
export function countLiterals(file: string, text: string): number {
  const s = stripComments(text)
  let n = (s.match(COLOR) ?? []).length
  if (file.endsWith('.css')) {
    // @container preludes: only the token thresholds.
    for (const m of s.matchAll(/@container[^{]*\{/g)) {
      n += lengths(m[0].match(LENGTH)) - (m[0].match(LENGTH) ?? []).filter((x) => containerPx.has(x)).length
    }
    for (const m of s.matchAll(/@media[^{]*\{/g)) n += lengths(m[0].match(LENGTH))
    for (const m of s.matchAll(/[\w-]+\s*:\s*([^;{}]+)[;}]/g)) {
      n += lengths(m[1].match(LENGTH))
      n += (m[1].match(NAMED_RE) ?? []).length
    }
  } else {
    for (const m of s.matchAll(/(['"`])((?:\\.|(?!\1).)*)\1/g)) n += lengths(m[2].match(LENGTH))
    for (const m of s.matchAll(STYLE_COLOR)) n += (m[2].match(NAMED_RE) ?? []).length
    for (const m of s.matchAll(ATTR_COLOR)) n += ((m[1] ?? m[2] ?? m[4] ?? '').match(NAMED_RE) ?? []).length
    for (const m of s.matchAll(STYLE_NUMBER)) if (nonZero(m[1])) n++
    for (const m of s.matchAll(ATTR_NUMBER)) if (nonZero(m[1] ?? m[2])) n++
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

  it('counts the other colour functions and named colours', () => {
    expect(countLiterals('a.css', '.a { color: hwb(0 0% 0%); background: lab(50% 0 0); border-color: lch(50 0 0); outline-color: color(srgb 1 0 0); }')).toBe(4)
    expect(countLiterals('a.css', '.a { color: white; border: 1px solid black; background: transparent; fill: currentColor; }')).toBe(3)
    expect(countLiterals('a.css', '.label-red, .tan { color: var(--k-ink); }')).toBe(0)
    expect(countLiterals('a.tsx', "const s = { color: 'white', background: 'var(--k-ink)' }; const t = 'Red means motion'")).toBe(1)
    expect(countLiterals('a.tsx', '<path fill="white" stroke={"black"} /><g fill="none" stroke="currentColor" />')).toBe(2)
    expect(countLiterals('a.tsx', 'const lab = label(x); const c = collab(y)')).toBe(0)
  })

  it('counts px in @media preludes', () => {
    expect(countLiterals('a.css', '@media (max-width: 768px) { .a { gap: var(--k-space-2); } }')).toBe(1)
    expect(countLiterals('a.css', '@media (prefers-reduced-motion: reduce) { .a { animation: none; } }')).toBe(0)
  })

  it('counts numbers on length keys and numeric size attributes', () => {
    expect(countLiterals('a.tsx', 'const s = { padding: 4, fontSize: 12, top: 0, lineHeight: 1.4, opacity: 0.5, zIndex: 3, flex: 1 }')).toBe(2)
    expect(countLiterals('a.tsx', '<rect rx={4} width={180} height={h} x={0} strokeWidth={1.5} /><circle r="6" />')).toBe(4)
    expect(countLiterals('a.tsx', '<svg width={size} height={graph.station.r * 2} />')).toBe(0)
  })

  it.each(Object.keys({ ...allowed, ...counts }).sort())('%s', (file) => {
    const n = counts[file] ?? 0
    const max = allowed[file] ?? 0
    expect(n, `${file} has ${n} hard-coded colours/lengths, more than the ${max} allowed: use the --k-* tokens`).toBeLessThanOrEqual(max)
    expect(n, `${file} is down to ${n}: lower its entry in noLiterals.allow.json to ${n}${n === 0 ? ' (remove it)' : ''}`).toBe(max)
  })
})
