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
export const GRAPH = graph

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
const STATION_H: Record<ZoomLevel, number> = { overview: 28, map: 44, detail: 70 + GRAPH.rankSep / 2 }
/** The dot sits this far below a station's top: its name goes above the line
 *  the edges run on, its version and state below (a metro map's labels). */
const DOT_TOP = 18

/** A band or platform's summary size; Overview is denser so 300 environments read at once. */
const GROUP_W: Record<ZoomLevel, number> = { overview: 150, map: 196, detail: 196 }
const GROUP_H: Record<ZoomLevel, number> = { overview: 40, map: 52, detail: 52 }
/** Rank spacing per level: Overview packs the columns. */
const RANK_SEP: Record<ZoomLevel, number> = { overview: GRAPH.rankSep / 2, map: GRAPH.rankSep, detail: GRAPH.rankSep }

/** Columns of an expanded group's grid: about a 3:1 aspect. */
export function gridColumns(n: number): number {
  return Math.max(4, Math.min(24, Math.ceil(Math.sqrt(n * 3))))
}

/** The size of an expanded group's grid, header and footer included. */
export function gridSize(n: number): { w: number; h: number; cols: number } {
  const cols = gridColumns(n)
  const rows = Math.ceil(n / cols)
  const step = GRAPH.grid.cell + GRAPH.grid.gap
  return { cols, w: Math.max(GROUP_W.map, cols * step + 24), h: rows * step + 44 }
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
export const LEAD = GRAPH.gateTick * 3 + GRAPH.gateTick / 2 + 1

/** About how wide a line of mono text of n characters is (--k-text-xs). */
const MONO_CH = 7.3

/** The longest label line of a station at Detail: its incoming line, or its version line. */
function detailTextWidth(st: Station): number {
  const lines = [
    st.env.length * 8,
    ((st.running?.version.length ?? 16) + 8) * MONO_CH,
    st.incoming ? (st.incoming.version.length + st.incoming.display.label.length + 12) * MONO_CH : 0,
  ]
  return Math.max(...lines)
}

/** The size dagre gives a node at a level. */
export function nodeSize(n: MapNode, level: ZoomLevel): { w: number; h: number } {
  if (n.kind === 'station') {
    const label = level === 'detail' ? Math.max(LABEL_W.map, Math.min(LABEL_W.detail + 80, detailTextWidth(n.station))) : LABEL_W[level]
    return { w: GRAPH.station.r * 2 + GRAPH.labelGap + label, h: STATION_H[level] }
  }
  if (n.expanded) {
    const s = gridSize(n.group.members.length)
    return { w: s.w, h: s.h }
  }
  return { w: GROUP_W[level], h: GROUP_H[level] }
}

/**
 * layout places the collapsed graph left to right. Ranks are fixed by the
 * model's depth so a station never jumps between columns as states change;
 * dagre orders nodes within a rank.
 */
export function layout(nodes: MapNode[], edges: MapEdge[], level: ZoomLevel): Layout {
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
    out.push({ edge: e, points: octilinear([a.outX, a.portY], [b.inX, b.portY], GRAPH.chamfer) })
  }
  const reached = new Set(out.map(e => e.edge.to))
  const leads = new Map<string, Array<[number, number]>>()
  for (const [id, p] of placed) {
    if (p.node.kind === 'station' && !reached.has(id)) leads.set(id, [[p.inX - LEAD, p.portY], [p.inX, p.portY]])
  }
  return { placed, edges: out, leads, bounds: { x: 0, y: 0, w: maxX + 24, h: maxY + 24 } }
}

/**
 * octilinear routes a → b with horizontal runs and 45° bends: out of a
 * horizontally, a diagonal that covers the vertical offset, then into b
 * horizontally. When the horizontal room is shorter than the offset, the
 * diagonal legs are chamfer long and a vertical run covers the rest.
 */
export function octilinear(a: [number, number], b: [number, number], chamfer: number): Array<[number, number]> {
  const [x1, y1] = a
  const [x2, y2] = b
  const dy = y2 - y1
  if (Math.abs(dy) < 0.5) return [a, b]
  const sign = dy > 0 ? 1 : -1
  const dx = x2 - x1
  const ady = Math.abs(dy)
  const lead = Math.min(chamfer, Math.max(0, dx / 4))
  if (dx - 2 * lead >= ady) {
    // Room for one diagonal: run, diagonal, run (the diagonal ends a lead before b).
    const xd = x2 - lead - ady
    return [a, [xd, y1], [xd + ady, y2], b]
  }
  // Steep: chamfer down, vertical, chamfer in.
  const leg = Math.max(0, Math.min(chamfer, (dx - 2 * lead) / 2, ady / 2))
  const xm = x1 + lead
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

/** Members of a group by grid cell, for an expanded group. */
export function gridCells(g: Group, order: string[], box: Box): Array<{ env: string; x: number; y: number }> {
  const { cols } = gridSize(g.members.length)
  const step = GRAPH.grid.cell + GRAPH.grid.gap
  return order.map((env, i) => ({
    env,
    x: box.x + 12 + (i % cols) * step,
    y: box.y + 28 + Math.floor(i / cols) * step,
  }))
}

/** A station's dot centre within its placed box. */
export function dotOf(p: Placed): { x: number; y: number } {
  return { x: p.inX + GRAPH.station.r, y: p.portY }
}

export type { Station }
