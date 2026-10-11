#!/usr/bin/env node
// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// check-size.mjs — fails when the built UI (web/dist) is over its budget, gzip sizes:
//   initial JS  145 KB  the entry module and every chunk it imports statically (the import
//                       graph), plus the first view a route needs (StageTable on a narrow
//                       screen, StageDrawer on an /env/ link): the largest such load;
//   total JS    200 KB  every chunk, the lazy ones (Table, Matrix, comparison, palette) too;
//   initial CSS  18 KB  the stylesheets of that load (index.html's and those chunks');
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

// The initial load is the import graph, not index.html's hints: the entry module and every chunk it
// imports statically, followed transitively. A dynamic import() starts a lazy chunk, which is not
// initial — except the first view a route needs: the Table on a narrow screen (StageTable) and the
// stage drawer on an /env/ link (StageDrawer). Initial is the largest of those routes' loads.
const html = fs.readFileSync(path.join(root, 'index.html'), 'utf8')
const entry = [...html.matchAll(/<script[^>]*type="module"[^>]*src="[^"]*\/assets\/([^"]+\.js)"/g)].map((m) => m[1])
if (entry.length === 0) {
  console.error('index.html names no module script')
  process.exit(1)
}
const STATIC = /(?:^|[;}\s])(?:import|export)\s*(?:[\w*$\s{},]*?\bfrom)?\s*["']\.\/([^"']+\.js)["']/g
function closure(roots) {
  const seen = new Set()
  const queue = [...roots]
  while (queue.length) {
    const f = queue.shift()
    if (seen.has(f)) continue
    if (!fs.existsSync(path.join(dist, f))) {
      console.error(`${f} is imported but not in dist/assets`)
      process.exit(1)
    }
    seen.add(f)
    for (const m of fs.readFileSync(path.join(dist, f), 'utf8').matchAll(STATIC)) queue.push(m[1])
  }
  return seen
}
const files = fs.readdirSync(dist)
const chunk = (name) => files.filter((f) => f.startsWith(`${name}-`) && f.endsWith('.js'))
const ROUTE_VIEWS = { 'a pipeline (narrow: the Table)': 'StageTable', 'an /env/ link (the stage drawer)': 'StageDrawer' }
const routes = { 'the first page': closure(entry) }
for (const [route, view] of Object.entries(ROUTE_VIEWS)) routes[route] = closure([...entry, ...chunk(view)])
// A route's stylesheets: index.html's, and those of the chunks it loads (Vite names them alike).
const linked = [...html.matchAll(/<link[^>]*rel="stylesheet"[^>]*href="[^"]*\/assets\/([^"]+\.css)"/g)].map((m) => m[1])
const cssOf = (js) => {
  const prefixes = [...js].map((f) => f.replace(/-[^-]+\.js$/, '-'))
  return new Set([...linked, ...files.filter((f) => f.endsWith('.css') && prefixes.some((p) => !p.startsWith('index-') && f.startsWith(p)))])
}
const sum = (set) => [...set].reduce((n, f) => n + gz(f), 0)
let worst = { route: '', js: 0, css: 0 }
for (const [route, js] of Object.entries(routes)) {
  const n = { route, js: sum(js), css: sum(cssOf(js)) }
  if (n.js > worst.js) worst = { ...worst, route, js: n.js }
  if (n.css > worst.css) worst = { ...worst, css: n.css }
}

const total = { 'initial js': 0, 'total js': 0, 'initial css': 0, 'total css': 0, font: 0 }
for (const f of fs.readdirSync(dist)) {
  if (f.endsWith('.js')) total['total js'] += gz(f)
  else if (f.endsWith('.css')) total['total css'] += gz(f)
  else if (f.endsWith('.woff2')) total.font += fs.statSync(path.join(dist, f)).size
}
total['initial js'] = worst.js
total['initial css'] = worst.css
let over = false
for (const k of Object.keys(BUDGET)) {
  const ok = total[k] <= BUDGET[k]
  over ||= !ok
  console.log(`${ok ? 'ok  ' : 'OVER'} ${k.padEnd(11)} ${(total[k] / KB).toFixed(1).padStart(6)} KB  (budget ${BUDGET[k] / KB} KB)`)
}
console.log(`     (initial: the largest route load, ${worst.route})`)
process.exit(over ? 1 : 0)
