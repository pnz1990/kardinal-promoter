#!/usr/bin/env node
// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// check-size.mjs — fails when the built UI (web/dist) is over its budget, gzip sizes:
// JavaScript 145 KB, CSS 18 KB, and the woff2 fonts 180 KB (already compressed).
// Run after `npm run build`: `npm run size`.
import fs from 'node:fs'
import path from 'node:path'
import zlib from 'node:zlib'
import { fileURLToPath } from 'node:url'

const KB = 1024
const BUDGET = { js: 145 * KB, css: 18 * KB, font: 180 * KB }
const dist = path.join(path.dirname(fileURLToPath(import.meta.url)), '..', 'dist', 'assets')

const total = { js: 0, css: 0, font: 0 }
for (const f of fs.readdirSync(dist)) {
  const buf = fs.readFileSync(path.join(dist, f))
  if (f.endsWith('.js')) total.js += zlib.gzipSync(buf, { level: 9 }).length
  else if (f.endsWith('.css')) total.css += zlib.gzipSync(buf, { level: 9 }).length
  else if (f.endsWith('.woff2')) total.font += buf.length
}
let over = false
for (const k of Object.keys(BUDGET)) {
  const ok = total[k] <= BUDGET[k]
  over ||= !ok
  console.log(`${ok ? 'ok  ' : 'OVER'} ${k.padEnd(4)} ${(total[k] / KB).toFixed(1).padStart(6)} KB  (budget ${BUDGET[k] / KB} KB)`)
}
process.exit(over ? 1 : 0)
