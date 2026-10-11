// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// stationMap/geometry.ts — where things go on the station map: dagre lays out
// the collapsed graph (a band or platform is one node), edges are drawn
// octilinear (horizontal runs and 45° chamfered bends, the logo's legs), and
// gate markers sit on the edge into the environment they guard.
//

import dagre from '@dagrejs/dagre'
import { graph } from '../tokens'
import type { MapEdge, MapNode, Station, Group } from './model'

/** The graph.* design tokens (DESIGN-SYSTEM §2.4). */
export const GRAPH = { ...graph, /** --k-text-2xs in px: no map label reads smaller on screen. */ labelMin: 11 } as const

/** A band's segmented bar, the thread's fill under it, and the minimap's thread hint: from the grid tokens. */
export const BAR = {
  seg: GRAPH.grid.cell - GRAPH.grid.gap,
  thread: GRAPH.grid.gap + GRAPH.grid.gap / 4,
  hint: GRAPH.grid.gap,
} as const

/** The semantic zoom levels (DESIGN-SYSTEM §3.2). */
export type ZoomLevel = 'overview' | 'map' | 'detail'

/** The level a zoom factor shows. */
export function levelAt(k: number): ZoomLevel {
  if (k < GRAPH.zoom.overview) return 'overview'
  if (k < GRAPH.zoom.detail) return 'map'
  return 'detail'
}

/** The zoom a level opens at. */
export function zoomFor(level: ZoomLevel): number {
  return level === 'overview' ? 0.45 : level === 'map' ? 0.8 : 1.2
}

/** The level a pipeline of n environments opens at: 3 → Detail, 30 → Map, 300 → Overview. */
export function initialLevel(n: number): ZoomLevel {
  if (n > GRAPH.overviewAt) return 'overview'
  if (n > 8) return 'map'
  return 'detail'
}

/** The width of a station's label column at a level, in map units. */
const LABEL_W: Record<ZoomLevel, number> = { overview: 44, map: 120, detail: 272 }
/** A station's height: the dot and its label lines. */
const STATION_H: Record<ZoomLevel, number> = { overview: 28, map: 44, detail: 100 + GRAPH.rankSep / 2 }
/** The dot sits this far below a station's top: its name goes above the line
 *  the edges run on, its version and state below (a metro map's labels). */
const DOT_TOP = 22

/** A band or platform's summary size; Overview is denser so 300 environments read at once. */
const GROUP_W: Record<ZoomLevel, number> = { overview: 180, map: 210, detail: 210 }
const GROUP_H: Record<ZoomLevel, number> = { overview: 40, map: 66, detail: 62 }
/** Rank spacing per level: Overview packs the columns. */
const RANK_SEP: Record<ZoomLevel, number> = { overview: GRAPH.rankSep / 2, map: GRAPH.rankSep, detail: GRAPH.rankSep }

/** Columns of an expanded group's grid: about a 3:1 aspect. */
export function gridColumns(n: number): number {
  return Math.max(4, Math.min(24, Math.ceil(Math.sqrt(n * 3))))
}

/** The space above an expanded grid's cells (the head) and below them (the footer). */
const GRID_HEAD = 28
const GRID_FOOT = 20
const GRID_PAD = 12
/** The space between two states' rows: room for a row label that is
 *  counter-scaled to stay readable at Map and Overview zoom. */
const GRID_BLOCK_GAP = 10

/** The rows of an expanded group's grid: each state starts a row, labelled
 *  with its count ("3 failed"); a state wider than the grid wraps. */
export function gridRows(g: Group): Array<{ label?: { tone: string; text: string }; start: number; count: number; y: number }> {
  const cols = gridColumns(g.members.length)
  const step = GRAPH.grid.cell + GRAPH.grid.gap
  const rows: Array<{ label?: { tone: string; text: string }; start: number; count: number; y: number }> = []
  let at = 0
  let y = 0
  g.states.forEach((st, b) => {
    if (b > 0) y += GRID_BLOCK_GAP
    for (let i = 0; i < st.count; i += cols) {
      rows.push({ y, start: at + i, count: Math.min(cols, st.count - i), label: i === 0 ? { tone: st.tone, text: `${st.count} ${lowerFirst(st.label)}` } : undefined })
      y += step
    }
    at += st.count
  })
  return rows
}

/** A character's width as a share of the font size (the map's small labels). */
export const CH_EM = 0.62

/** The font size, in map units, of a small label (--k-text-2xs) at a level's
 *  zoom: counter-scaled so it reads at least labelMin px on screen. */
export function labelUnits(level: ZoomLevel): number {
  // The smallest zoom a level is fitted at: Map down to Overview's threshold.
  const k = level === 'detail' ? GRAPH.zoom.detail : level === 'map' ? GRAPH.zoom.overview : zoomFor('overview')
  return GRAPH.labelMin * Math.max(1, 1 / k)
}

/** How wide n characters of a small label are at a level, in map units. */
export function textW(n: number, level: ZoomLevel): number {
  return n * CH_EM * labelUnits(level)
}

/** A band or platform's visible title: "WAVE 2 · 48", "EDGE · 12" (its accessible name says the rest). */
export function groupTitle(g: Group): string {
  return `${g.label} · ${g.members.length}`
}

/** A platform's pacing line: "2 at a time · stop at 2 failed". */
export function groupPacing(g: Group): string {
  return g.kind === 'platform'
    ? [g.maxConcurrent ? `${g.maxConcurrent} at a time` : '', g.maxUnavailable ? `stop at ${g.maxUnavailable} failed` : ''].filter(Boolean).join(' · ')
    : ''
}

/** A group's sub line: its pacing, or what most of it runs. */
export function groupSub(g: Group): string {
  return groupPacing(g) || (g.version ? `most run ${g.version}` : 'nothing deployed')
}

/** The width of an expanded grid's row label column at a level: its longest label. */
export function gridLabelW(g: Group, level: ZoomLevel): number {
  const longest = Math.max(0, ...gridRows(g).map(r => r.label?.text.length ?? 0))
  return textW(longest, level) + GRID_PAD
}

/** The size of an expanded group's grid, head, row labels and footer included:
 *  wide enough that no label is cut. */
export function gridSize(g: Group, level: ZoomLevel = 'map'): { w: number; h: number; cols: number } {
  const cols = gridColumns(g.members.length)
  const rows = gridRows(g)
  const step = GRAPH.grid.cell + GRAPH.grid.gap
  const height = rows.length ? rows[rows.length - 1].y + step : step
  const w = Math.max(GROUP_W.map, gridLabelW(g, level) + cols * step + GRID_PAD * 2,
    textW(groupTitle(g).length + 2, level) + GRID_PAD * 3, textW(groupSub(g).length, level) + GRID_PAD * 2)
  return { cols, w, h: GRID_HEAD + height + GRID_FOOT }
}

function lowerFirst(s: string): string {
  return s.length > 1 && s[1] === s[1].toLowerCase() ? s[0].toLowerCase() + s.slice(1) : s
}

export interface Box {
  x: number
  y: number
  w: number
  h: number
}

export interface Placed {
  node: MapNode
  /** Centre of the node. */
  x: number
  y: number
  w: number
  h: number
  /** Where edges attach: the dot of a station, the left/right of a group. */
  inX: number
  outX: number
  portY: number
}

export interface Layout {
  placed: Map<string, Placed>
  edges: Array<{ edge: MapEdge; points: Array<[number, number]> }>
  /** A short line into each station no edge reaches, where gates into a first environment sit. */
  leads: Map<string, Array<[number, number]>>
  bounds: Box
}

/** The length of a lead-in line: room for two gate marks before a first station. */
export const LEAD = GRAPH.gateTick * 4 + GRAPH.gateTick / 2 + 1

/** About how wide a character is at Detail: mono at --k-text-sm, the name in
 *  the display face at --k-text-lg. */
const MONO_CH = 7.9
const NAME_CH = 9.5

/** The longest label line of a station at Detail: its name, its incoming line, or its version line. */
function detailTextWidth(st: Station): number {
  // The incoming version and its state are two lines ("→ 1.4.2", "Held by gate x · #42").
  const lines = [
    st.env.length * NAME_CH,
    ((st.running?.version.length ?? 16) + 8) * MONO_CH,
    st.incoming ? (st.incoming.version.length + 2) * MONO_CH : 0,
    st.incoming ? (st.incoming.display.label.length + 10) * MONO_CH : 0,
  ]
  return Math.max(...lines)
}

/** How many mono characters fit in a station's label column at Detail. */
export function detailChars(p: { w: number }): number {
  return Math.floor((p.w - GRAPH.station.r * 2) / MONO_CH)
}

/** The straight run an edge keeps before a node with n gates: the gate marks
 *  sit on it, never on a chamfer. */
export function gateRun(n: number): number {
  return n > 0 ? GRAPH.gateTick * 4 + (n - 1) * GATE_STEP : 0
}

/** The spacing of gate marks on one edge (a comb). */
export const GATE_STEP = 6

/** The size dagre gives a node at a level. */
export function nodeSize(n: MapNode, level: ZoomLevel): { w: number; h: number } {
  if (n.kind === 'station') {
    const label = level === 'detail' ? Math.max(LABEL_W.map, Math.min(LABEL_W.detail + 80, detailTextWidth(n.station))) : LABEL_W[level]
    return { w: GRAPH.station.r * 2 + GRAPH.labelGap + label, h: STATION_H[level] }
  }
  if (n.expanded) {
    const s = gridSize(n.group, level)
    return { w: s.w, h: s.h }
  }
  // Wide enough for its title (and the failure mark) and its sub line: no label is cut.
  const g = n.group
  const title = textW(groupTitle(g).length + 2 + (g.failed > 0 ? String(g.failed).length + 2 : 0), level) + GRID_PAD * 3
  const sub = level === 'overview' ? 0 : textW(groupSub(g).length, level) + GRID_PAD * 2
  return { w: Math.max(GROUP_W[level], title, sub), h: GROUP_H[level] }
}

/**
 * layout places the collapsed graph left to right. Ranks are fixed by the
 * model's depth so a station never jumps between columns as states change;
 * dagre orders nodes within a rank.
 */
export function layout(nodes: MapNode[], edges: MapEdge[], level: ZoomLevel, gatesInto: (id: string) => number = () => 0): Layout {
  const g = new dagre.graphlib.Graph()
  g.setGraph({ rankdir: 'LR', nodesep: GRAPH.nodeSep, ranksep: RANK_SEP[level], marginx: 24 + LEAD, marginy: 24 })
  g.setDefaultEdgeLabel(() => ({}))
  for (const n of nodes) {
    const { w, h } = nodeSize(n, level)
    g.setNode(n.id, { width: w, height: h })
  }
  for (const e of edges) g.setEdge(e.from, e.to)
  dagre.layout(g)

  const placed = new Map<string, Placed>()
  let maxX = 0
  let maxY = 0
  for (const n of nodes) {
    const d = g.node(n.id)
    const { w, h } = nodeSize(n, level)
    const left = d.x - w / 2
    const isStation = n.kind === 'station'
    // A station's edges meet its dot, at the left of the node; a group's meet
    // its frame on both sides.
    const dotX = left + GRAPH.station.r
    const portY = isStation ? d.y - h / 2 + DOT_TOP : d.y
    placed.set(n.id, {
      node: n, x: d.x, y: d.y, w, h,
      inX: isStation ? dotX - GRAPH.station.r : left,
      outX: isStation ? dotX + GRAPH.station.r : left + w,
      portY,
    })
    maxX = Math.max(maxX, d.x + w / 2)
    maxY = Math.max(maxY, d.y + h / 2)
  }
  const out: Layout['edges'] = []
  for (const e of edges) {
    const a = placed.get(e.from)
    const b = placed.get(e.to)
    if (!a || !b) continue
    out.push({ edge: e, points: octilinear([a.outX, a.portY], [b.inX, b.portY], GRAPH.chamfer, gateRun(gatesInto(e.to))) })
  }
  const reached = new Set(out.map(e => e.edge.to))
  const leads = new Map<string, Array<[number, number]>>()
  for (const [id, p] of placed) {
    if (p.node.kind === 'station' && !reached.has(id)) {
      const len = Math.max(LEAD, gateRun(gatesInto(id)) + GRAPH.gateTick / 2)
      leads.set(id, [[p.inX - len, p.portY], [p.inX, p.portY]])
    }
  }
  return { placed, edges: out, leads, bounds: { x: 0, y: 0, w: maxX + 24, h: maxY + 24 } }
}

/**
 * octilinear routes a → b with horizontal runs and 45° bends: out of a
 * horizontally, a diagonal that covers the vertical offset, then into b
 * horizontally. When the horizontal room is shorter than the offset, the
 * diagonal legs are chamfer long and a vertical run covers the rest.
 */
export function octilinear(a: [number, number], b: [number, number], chamfer: number, tail = 0): Array<[number, number]> {
  const [x1, y1] = a
  const [x2, y2] = b
  const dy = y2 - y1
  if (Math.abs(dy) < 0.5) return [a, b]
  const sign = dy > 0 ? 1 : -1
  const dx = x2 - x1
  const ady = Math.abs(dy)
  const lead = Math.min(chamfer, Math.max(0, dx / 4))
  // The run into b: at least tail long (its gates sit there) when there is room.
  const end = Math.max(lead, Math.min(tail, dx - lead - 2 * Math.min(chamfer, ady / 2)))
  if (dx - lead - end >= ady) {
    // Room for one diagonal: run, diagonal, run (the diagonal ends `end` before b).
    const xd = x2 - end - ady
    return [a, [xd, y1], [xd + ady, y2], b]
  }
  // Steep: run, chamfer, vertical, chamfer, then the run into b.
  const leg = Math.max(0, Math.min(chamfer, (dx - lead - end) / 2, ady / 2))
  const xm = x2 - end - 2 * leg
  return [a, [xm, y1], [xm + leg, y1 + sign * leg], [xm + leg, y2 - sign * leg], [xm + 2 * leg, y2], b]
}

/** An SVG path through points. */
export function pathOf(points: Array<[number, number]>): string {
  return points.map(([x, y], i) => `${i === 0 ? 'M' : 'L'}${round(x)} ${round(y)}`).join(' ')
}

/** The length of a polyline. */
export function lengthOf(points: Array<[number, number]>): number {
  let len = 0
  for (let i = 1; i < points.length; i++) {
    len += Math.hypot(points[i][0] - points[i - 1][0], points[i][1] - points[i - 1][1])
  }
  return len
}

/** The point at distance d from the end of a polyline, and the direction there. */
export function pointBeforeEnd(points: Array<[number, number]>, d: number): { x: number; y: number; dx: number; dy: number } {
  let left = d
  for (let i = points.length - 1; i > 0; i--) {
    const [x2, y2] = points[i]
    const [x1, y1] = points[i - 1]
    const seg = Math.hypot(x2 - x1, y2 - y1)
    if (seg === 0) continue
    const ux = (x2 - x1) / seg
    const uy = (y2 - y1) / seg
    if (left <= seg) return { x: x2 - ux * left, y: y2 - uy * left, dx: ux, dy: uy }
    left -= seg
  }
  const [x, y] = points[0]
  return { x, y, dx: 1, dy: 0 }
}

/** The polyline cut d before its end (the thread stops before a gate bar). */
export function trimEnd(points: Array<[number, number]>, d: number): Array<[number, number]> {
  const p = pointBeforeEnd(points, d)
  const out: Array<[number, number]> = []
  let left = lengthOf(points) - d
  out.push(points[0])
  for (let i = 1; i < points.length && left > 0; i++) {
    const seg = Math.hypot(points[i][0] - points[i - 1][0], points[i][1] - points[i - 1][1])
    if (seg >= left) { out.push([p.x, p.y]); break }
    out.push(points[i])
    left -= seg
  }
  return out
}

function round(n: number): number {
  return Math.round(n * 10) / 10
}

/** Members of a group by grid cell, for an expanded group: rows by state
 *  (gridRows), with the row and column of each cell for the arrow keys. */
export function gridCells(g: Group, order: string[], box: Box, level: ZoomLevel = 'map'): Array<{ env: string; x: number; y: number; row: number; col: number }> {
  const step = GRAPH.grid.cell + GRAPH.grid.gap
  const labelW = gridLabelW(g, level)
  const out: Array<{ env: string; x: number; y: number; row: number; col: number }> = []
  gridRows(g).forEach((r, row) => {
    for (let col = 0; col < r.count; col++) {
      const env = order[r.start + col]
      if (env) out.push({ env, row, col, x: box.x + GRID_PAD + labelW + col * step, y: box.y + GRID_HEAD + r.y })
    }
  })
  return out
}

/** Where an expanded grid's row labels and footer go. */
export function gridText(g: Group, box: Box): { rows: Array<{ x: number; y: number; tone: string; text: string }>; foot: { x: number; y: number; w: number } } {
  const rows = gridRows(g).flatMap(r => r.label ? [{ ...r.label, x: box.x + GRID_PAD, y: box.y + GRID_HEAD + r.y + GRAPH.grid.cell - 1 }] : [])
  return { rows, foot: { x: box.x + GRID_PAD, y: box.y + box.h - GRID_FOOT / 2 + 3, w: box.w - GRID_PAD * 2 } }
}

/** A station's dot centre within its placed box. */
export function dotOf(p: Placed): { x: number; y: number } {
  return { x: p.inX + GRAPH.station.r, y: p.portY }
}

export type { Station }
