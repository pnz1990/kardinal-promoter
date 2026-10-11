#!/usr/bin/env node
// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// check-size.mjs — fails when the built UI (web/dist) is over its budget, gzip sizes:
//   initial JS  145 KB  the entry chunk and the chunks it imports statically (what the
//                       first page load waits for: index.html's module script and its
//                       modulepreload links);
//   total JS    200 KB  every chunk, the lazy ones (Table, Matrix, comparison, palette) too;
//   initial CSS  18 KB  the stylesheets index.html links (what the first paint waits for);
//   total CSS    22 KB  every stylesheet, the lazy views' too;
//   fonts       180 KB  the woff2 fonts (already compressed).
// Run after `npm run build`: `npm run size`.
import fs from 'node:fs'
import path from 'node:path'
import zlib from 'node:zlib'
import { fileURLToPath } from 'node:url'

const KB = 1024
const BUDGET = { 'initial js': 145 * KB, 'total js': 200 * KB, 'initial css': 18 * KB, 'total css': 22 * KB, font: 180 * KB }
const root = path.join(path.dirname(fileURLToPath(import.meta.url)), '..', 'dist')
const dist = path.join(root, 'assets')
const gz = (f) => zlib.gzipSync(fs.readFileSync(path.join(dist, f)), { level: 9 }).length

// The initial load: the module script and the modulepreload links of index.html.
const html = fs.readFileSync(path.join(root, 'index.html'), 'utf8')
const initial = new Set(
  [...html.matchAll(/<(?:script[^>]*type="module"[^>]*src|link[^>]*rel="modulepreload"[^>]*href)="[^"]*\/assets\/([^"]+\.js)"/g)].map((m) => m[1]),
)
const initialCSS = new Set([...html.matchAll(/<link[^>]*rel="stylesheet"[^>]*href="[^"]*\/assets\/([^"]+\.css)"/g)].map((m) => m[1]))
if (initial.size === 0) {
  console.error('index.html names no module script')
  process.exit(1)
}

const total = { 'initial js': 0, 'total js': 0, 'initial css': 0, 'total css': 0, font: 0 }
for (const f of fs.readdirSync(dist)) {
  if (f.endsWith('.js')) {
    const n = gz(f)
    total['total js'] += n
    if (initial.has(f)) total['initial js'] += n
  } else if (f.endsWith('.css')) {
    const n = gz(f)
    total['total css'] += n
    if (initialCSS.has(f)) total['initial css'] += n
  }
  else if (f.endsWith('.woff2')) total.font += fs.statSync(path.join(dist, f)).size
}
let over = false
for (const k of Object.keys(BUDGET)) {
  const ok = total[k] <= BUDGET[k]
  over ||= !ok
  console.log(`${ok ? 'ok  ' : 'OVER'} ${k.padEnd(11)} ${(total[k] / KB).toFixed(1).padStart(6)} KB  (budget ${BUDGET[k] / KB} KB)`)
}
process.exit(over ? 1 : 0)
