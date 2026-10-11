// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// stationMap/StationMap.tsx — the pipeline as a station map (DESIGN-SYSTEM §3):
// environments are dots with their labels, edges are octilinear ink lines,
// gates are mounted on the edge into the environment they guard, waves and
// fleets collapse into bands and platforms, and the Bundle being followed is
// one cardinal-red thread. Three semantic zoom levels (Overview, Map,
// Detail), a minimap, an attention strip and search.

import { useCallback, useEffect, useId, useMemo, useRef, useState } from 'react'
import type { GateMark, Group, MapNode, Station, StationMapModel, ThreadSegment } from './model'
import { gridOrder, groupSummary, stationLabel } from './model'
import {
  BAR, GATE_STEP, GRAPH, detailChars, dotOf, gridCells, gridText, groupPacing, groupSub, groupTitle, initialLevel, layout, pathOf, pointBeforeEnd, trimEnd,
  type Layout, type Placed, type ZoomLevel,
} from './geometry'
import { useMapViewport } from './useMapViewport'
import { ageOf } from '../fleetModel'
import { prNumberFromURL } from '../prLink'
import { Icon } from '../ui'
import '../styles/StationMap.css'

export interface StationMapProps {
  model: StationMapModel
  /** A second Bundle's model, drawn as an ink dashed thread (compare). */
  compare?: StationMapModel
  /** The environment whose stage drawer is open. */
  focused?: string
  /** Open the stage drawer of env (U3 contract, #1701). */
  onOpenStage: (env: string, section?: 'why') => void
  /** Groups the user expanded. */
  expanded: Set<string>
  onToggleGroup: (id: string) => void
  onSetExpanded: (ids: Set<string>) => void
  paused?: boolean
  /** "1.4.2" — what the followed Bundle ships, for the live region. */
  followedVersion?: string
  /** The quiet actions under a station at Detail (Promote, Roll back,
   *  Release hold): the lane's quick actions (DESIGN-SYSTEM §3.2). */
  actionsFor?: (env: string) => React.ReactNode
  /** Open a gate's own details (its graph node id); without it a gate opens
   *  its environment's stage drawer at "why" (U3, #1701). */
  onOpenGate?: (gateNodeId: string) => void
}

/** About how wide a character of a gate label is, and the label's line height (--k-text-xs). */
const GATE_CH = 6
const GATE_LABEL_H = 16
/** The minimap's width and its inset from the map's corner (--k-space-3). */
const MINIMAP_W = 160
const MINIMAP_INSET = 12
/** How long a gate's popover stays after the pointer leaves, so it can be reached (ms). */
const POPOVER_HIDE_MS = 150

/** The leader from a gate's label down to its bar. */
const GATE_LEADER = 8

const LEVELS: ZoomLevel[] = ['overview', 'map', 'detail']
const LEVEL_LABEL: Record<ZoomLevel, string> = { overview: 'Overview', map: 'Map', detail: 'Detail' }

export default function StationMap(props: StationMapProps) {
  const { model, compare, focused, onOpenStage, expanded, onToggleGroup, onSetExpanded, paused, followedVersion, actionsFor, onOpenGate } = props
  const svgRef = useRef<SVGSVGElement | null>(null)
  const searchRef = useRef<HTMLInputElement | null>(null)
  const descId = useId()
  const total = model.stations.size
  const [query, setQuery] = useState('')
  const [hit, setHit] = useState(0)
  const [cursor, setCursor] = useState<string | undefined>(undefined)
  const [popover, setPopover] = useState<{ gate: GateMark; env: string; x: number; y: number } | null>(null)
  // The popover hides a moment after the pointer leaves the gate (or the popover), so it can be
  // reached. Every hover end replaces the pending timer: two in a row never orphan one.
  const hideTimer = useRef<ReturnType<typeof setTimeout> | null>(null)
  const cancelHide = useCallback(() => {
    if (hideTimer.current !== null) { clearTimeout(hideTimer.current); hideTimer.current = null }
  }, [])
  const showPopover = useCallback((p: { gate: GateMark; env: string; x: number; y: number }) => { cancelHide(); setPopover(p) }, [cancelHide])
  const hidePopover = useCallback(() => {
    cancelHide()
    hideTimer.current = setTimeout(() => { hideTimer.current = null; setPopover(null) }, POPOVER_HIDE_MS)
  }, [cancelHide])
  useEffect(() => cancelHide, [cancelHide])
  const patternId = useId().replace(/:/g, '')
  const [announce, setAnnounce] = useState('')

  // Layout over a level: computed before the viewport so the first fit has bounds.
  const [level0] = useState<ZoomLevel>(() => initialLevel(total))
  const [layoutLevel, setLayoutLevel] = useState<ZoomLevel>(level0)
  // Re-layout on a topology change; at Detail also when a label changes size.
  // An expanded grid's rows follow its states; an edge's straight run, its gates.
  const topoKey = useMemo(() => model.nodes.map(n => `${n.id}${n.kind === 'group' && n.expanded ? `+${n.group.states.map(st => st.count).join(',')}` : ''}` +
    `/${gatesOn(n, model).length}` +
    (layoutLevel === 'detail' && n.kind === 'station' ? `:${n.station.incoming?.display.label ?? ''}${n.station.running?.version ?? ''}` : '')).join('|') + '#' +
    model.edges.map(e => `${e.from}>${e.to}`).join('|'), [model, layoutLevel])
  const lay: Layout = useMemo(() => {
    const byId = new Map(model.nodes.map(n => [n.id, n]))
    return layout(model.nodes, model.edges, layoutLevel, id => { const n = byId.get(id); return n ? gatesOn(n, model).length : 0 })
  },
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [topoKey, layoutLevel])
  const vp = useMapViewport(svgRef, lay.bounds, level0)
  // Map text is counter-scaled by the zoom (StationMap.css reads --map-k).
  useEffect(() => { svgRef.current?.style.setProperty('--map-k', String(vp.view.k)) }, [vp.view.k])
  useEffect(() => {
    if (vp.level !== layoutLevel) {
      setLayoutLevel(vp.level)
      setAnnounce(`${LEVEL_LABEL[vp.level]} view`)
    }
  }, [vp.level, layoutLevel])
  const level = layoutLevel

  // Search: environment and target names, versions, bundles, gate names and expressions.
  const matches = useMemo(() => {
    const q = query.trim().toLowerCase()
    if (!q) return [] as string[]
    return model.order.filter(env => {
      const st = model.stations.get(env)!
      return env.toLowerCase().includes(q) || st.running?.version.toLowerCase().includes(q) ||
        st.running?.bundle.toLowerCase().includes(q) || st.incoming?.version.toLowerCase().includes(q) ||
        st.gates.some(g => g.name.toLowerCase().includes(q) || g.expression?.toLowerCase().includes(q))
    })
  }, [query, model])
  const matchSet = useMemo(() => new Set(matches), [matches])

  // Where an environment is drawn: its own node, or a cell of its group.
  const positionOf = useCallback((env: string): { x: number; y: number } | undefined => {
    const p = lay.placed.get(env)
    if (p) return dotOf(p)
    const gid = model.groupOf.get(env)
    if (!gid) return undefined
    const gp = lay.placed.get(gid)
    if (!gp) return undefined
    if (gp.node.kind === 'group' && gp.node.expanded) {
      const cell = gridCells(gp.node.group, gridOrder(gp.node.group, model.stations), boxOf(gp), layoutLevel).find(c => c.env === env)
      if (cell) return { x: cell.x + GRAPH.grid.cell / 2, y: cell.y + GRAPH.grid.cell / 2 }
    }
    return { x: gp.x, y: gp.y }
  }, [lay, model, layoutLevel])

  // Focusable things in order: stations, groups, and the cells of expanded groups.
  const focusables = useMemo(() => {
    const out: string[] = []
    for (const n of model.nodes) {
      if (n.kind === 'station') out.push(n.id)
      else {
        out.push(n.id)
        if (n.expanded) out.push(...gridOrder(n.group, model.stations))
      }
    }
    return out
  }, [model])
  const active = cursor && focusables.includes(cursor) ? cursor : focusables[0]

  const focusItem = useCallback((id: string) => {
    setCursor(id)
    const pos = model.stations.has(id) ? positionOf(id) : (() => { const p = lay.placed.get(id); return p ? { x: p.x, y: p.y } : undefined })()
    if (pos) vp.reveal(pos.x, pos.y)
    requestAnimationFrame(() => {
      svgRef.current?.querySelector<SVGElement>(`[data-focus-id="${cssEscape(id)}"]`)?.focus()
    })
  }, [model, positionOf, lay, vp])

  // Focus returns to the station whose drawer closed (U3 contract, #1701).
  const prevFocused = useRef(focused)
  useEffect(() => {
    if (prevFocused.current && !focused) focusItem(prevFocused.current)
    prevFocused.current = focused
  }, [focused, focusItem])

  // Step through search hits.
  useEffect(() => {
    if (matches.length === 0) return
    const env = matches[Math.min(hit, matches.length - 1)]
    const gid = model.groupOf.get(env)
    if (gid && !expanded.has(gid)) onSetExpanded(new Set([...expanded, gid]))
    const pos = positionOf(env)
    if (pos) vp.centre(pos.x, pos.y)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [hit, matches])

  // The followed Bundle's transitions, announced politely.
  const prevTones = useRef(new Map<string, string>())
  useEffect(() => {
    const changed: string[] = []
    for (const st of model.stations.values()) {
      const before = prevTones.current.get(st.env)
      if (before && before !== st.display.label && st.reached) {
        changed.push(`${followedVersion ?? 'The version'} ${st.display.label.toLowerCase()} in ${st.env}`)
      }
      prevTones.current.set(st.env, st.display.label)
    }
    if (changed.length > 0 && changed.length <= 3) setAnnounce(changed.join('. '))
  }, [model, followedVersion])

  // Upstream (feeds it) and downstream (it gates) of the focused station.
  const chain = useMemo(() => {
    const up = new Set<string>()
    const down = new Set<string>()
    if (!focused) return { up, down }
    const walk = (env: string, set: Set<string>, next: (s: Station) => string[]) => {
      for (const n of next(model.stations.get(env)!)) {
        if (!set.has(n) && model.stations.has(n)) { set.add(n); walk(n, set, next) }
      }
    }
    if (model.stations.has(focused)) {
      walk(focused, up, s => s.upstreams)
      walk(focused, down, s => [...model.stations.values()].filter(x => x.upstreams.includes(s.env)).map(x => x.env))
    }
    return { up, down }
  }, [focused, model])
  const layoutIdOf = (env: string) => model.groupOf.get(env) ?? env
  const upIds = new Set([...chain.up].map(layoutIdOf))
  const downIds = new Set([...chain.down].map(layoutIdOf))
  const focusedId = focused ? layoutIdOf(focused) : undefined

  // The cells of the expanded group an environment is in, with rows and columns.
  const cellsOf = (gid: string) => {
    const gp = lay.placed.get(gid)
    if (!gp || gp.node.kind !== 'group' || !gp.node.expanded) return undefined
    const g = model.groups.get(gid)!
    return gridCells(g, gridOrder(g, model.stations), boxOf(gp), level)
  }

  // A first Esc on the map was the map's; the next one is the page's.
  const escArmed = useRef(false)
  const onKeyDown = (e: React.KeyboardEvent<SVGSVGElement>) => {
    const id = active
    if (!id) return
    const st = model.stations.get(id)
    const node = lay.placed.get(id)?.node
    const go = (next: string | undefined) => { if (next) { e.preventDefault(); focusItem(next) } }
    const lid = layoutIdOf(id)
    // Inside an expanded grid the arrows move by row and column; past its
    // edges they leave the grid like the group itself.
    const gid = st ? model.groupOf.get(id) : undefined
    const cells = gid ? cellsOf(gid) : undefined
    const here = cells?.find(c => c.env === id)
    if (cells && here && e.key.startsWith('Arrow')) {
      const at = (row: number, col: number) => cells.find(c => c.row === row && c.col === col) ??
        [...cells].filter(c => c.row === row).sort((a, b) => b.col - a.col)[0]
      const rows = Math.max(...cells.map(c => c.row))
      if (e.key === 'ArrowRight' && cells.some(c => c.row === here.row && c.col === here.col + 1)) return go(at(here.row, here.col + 1)?.env)
      if (e.key === 'ArrowLeft' && here.col > 0) return go(at(here.row, here.col - 1)?.env)
      if (e.key === 'ArrowDown') { if (here.row < rows) go(at(here.row + 1, here.col)?.env); else e.preventDefault(); return }
      if (e.key === 'ArrowUp') return go(here.row > 0 ? at(here.row - 1, here.col)?.env : gid)
    }
    if (node?.kind === 'group' && node.expanded && e.key === 'ArrowDown') {
      const first = cellsOf(node.id)?.[0]
      if (first) return go(first.env)
    }
    switch (e.key) {
      case 'ArrowRight': {
        const outs = model.edges.filter(x => x.from === lid).map(x => x.to)
        go(outs[e.altKey ? 1 % Math.max(1, outs.length) : 0])
        break
      }
      case 'ArrowLeft': {
        const ins = model.edges.filter(x => x.to === lid).map(x => x.from)
        go(ins[e.altKey ? 1 % Math.max(1, ins.length) : 0])
        break
      }
      case 'ArrowDown':
      case 'ArrowUp': {
        const p = lay.placed.get(lid)
        if (!p) break
        const col = [...lay.placed.values()].filter(q => Math.abs(q.x - p.x) < 1).sort((a, b) => a.y - b.y).map(q => q.node.id)
        const i = col.indexOf(lid)
        go(col[i + (e.key === 'ArrowDown' ? 1 : -1)])
        break
      }
      case 'Home': go(focusables[0]); break
      case 'End': go(focusables[focusables.length - 1]); break
      case 'Enter':
        e.preventDefault()
        if (st) onOpenStage(st.env)
        else if (node?.kind === 'group') onToggleGroup(node.id)
        break
      case ' ':
        if (node?.kind === 'group') { e.preventDefault(); onToggleGroup(node.id) }
        else if (st && model.groupOf.has(st.env)) { e.preventDefault(); onToggleGroup(model.groupOf.get(st.env)!) }
        break
      case 'e':
      case 'E': {
        e.preventDefault()
        if (e.shiftKey) onSetExpanded(new Set())
        else {
          const urgent = [...model.groups.values()].filter(g => g.counts.some(c => ['failed', 'blocked', 'held'].includes(c.tone))).map(g => g.id)
          onSetExpanded(new Set([...expanded, ...urgent]))
        }
        break
      }
      case '+': case '=': e.preventDefault(); vp.zoomBy(1.25); break
      case '-': case '_': e.preventDefault(); vp.zoomBy(0.8); break
      case '0': e.preventDefault(); vp.fit(); break
      // The map's own search, not the page's: the event stops here (the page's
      // shortcuts listen on the document).
      case '/': e.preventDefault(); e.stopPropagation(); searchRef.current?.focus(); break
      case 'Escape':
        // Esc first clears what the map holds (its search, a popover, the zoom);
        // only the next Esc goes on to the page (it closes the stage drawer).
        if (query) { setQuery(''); e.preventDefault(); e.stopPropagation(); escArmed.current = true }
        else if (popover) { cancelHide(); setPopover(null); e.stopPropagation(); escArmed.current = true }
        else if (!escArmed.current) { vp.fit(); e.stopPropagation(); escArmed.current = true }
        else escArmed.current = false
        return
    }
    escArmed.current = false
  }

  const att = model.attention
  const attentionItems: Array<{ key: string; text: string; envs: string[] }> = []
  if (att.failed.length) attentionItems.push({ key: 'failed', text: `${att.failed.length} failed`, envs: att.failed })
  const byGate = new Map<string, string[]>()
  for (const b of att.blocked) byGate.set(b.gate, [...(byGate.get(b.gate) ?? []), b.env])
  for (const [g, envs] of byGate) attentionItems.push({ key: `blocked:${g}`, text: `${envs.length} blocked by ${g}`, envs })
  if (att.held.length) attentionItems.push({ key: 'held', text: `${att.held.length} held`, envs: att.held })
  if (att.waiting.length) attentionItems.push({ key: 'waiting', text: `${att.waiting.length} waiting`, envs: att.waiting })

  const showAttention = (envs: string[]) => {
    const gids = envs.map(env => model.groupOf.get(env)).filter((g): g is string => !!g)
    if (gids.length) onSetExpanded(new Set([...expanded, ...gids]))
    setQuery('')
    requestAnimationFrame(() => focusItem(envs[0]))
  }

  const threadEdge = (seg: ThreadSegment) => lay.edges.find(x => x.edge.from === seg.from && x.edge.to === seg.to)

  return (
    <div className={`station-map${paused ? ' station-map--paused' : ''}`} data-level={level}>
      <div className="station-map__bar">
        {attentionItems.length > 0 ? (
          <div className="station-map__attention" role="group" aria-label="Needs attention">
            <span className="station-map__eyebrow">Attention</span>
            {attentionItems.map(a => (
              <button key={a.key} type="button" className="station-map__attention-item" data-tone={a.key.split(':')[0]}
                onClick={() => showAttention(a.envs)}>{a.text}</button>
            ))}
          </div>
        ) : <div className="station-map__attention" />}
        <label className="station-map__search">
          <span className="sr-only">Search environments, versions and gates</span>
          <input ref={searchRef} type="search" value={query} placeholder="Search /"
            onChange={e => { setQuery(e.target.value); setHit(0) }}
            onKeyDown={e => {
              if (e.key === 'Enter') { e.preventDefault(); setHit(h => matches.length ? (h + (e.shiftKey ? matches.length - 1 : 1)) % matches.length : 0) }
              if (e.key === 'Escape') { setQuery(''); svgRef.current?.focus() }
            }} />
          {query && <span className="station-map__count" aria-live="polite">{matches.length} {matches.length === 1 ? 'match' : 'matches'}</span>}
        </label>
        <div className="station-map__levels" role="radiogroup" aria-label="Zoom level">
          {LEVELS.map(l => (
            <button key={l} type="button" role="radio" aria-checked={level === l} className="station-map__level"
              onClick={() => vp.setLevel(l)}>{LEVEL_LABEL[l]}</button>
          ))}
        </div>
      </div>
      {paused && <div className="station-map__paused-band" role="status">Paused · no new promotions</div>}
      <p id={descId} className="sr-only">
        Pipeline map with {total} environments. Arrow keys move between environments, Enter opens one,
        Space expands a wave or fleet, plus and minus zoom, slash searches.
      </p>
      <svg ref={svgRef} className="station-map__svg" viewBox={vp.viewBox} role="group" aria-roledescription="pipeline map"
        aria-describedby={descId} tabIndex={-1} onKeyDown={onKeyDown} {...vp.handlers}
>
        <defs>
          {/* A held cell is hatched: its state reads by shape as well as colour. */}
          <pattern id={`${patternId}-hatch`} width={GRAPH.grid.cell / 2} height={GRAPH.grid.cell / 2} patternUnits="userSpaceOnUse" patternTransform="rotate(45)">
            <line x1={0} y1={0} x2={0} y2={GRAPH.grid.cell / 2} className="cell__hatch" />
          </pattern>
        </defs>
        <g className="station-map__edges">
          {lay.edges.map(({ edge, points }) => {
            const tone = upIds.has(edge.from) && (upIds.has(edge.to) || edge.to === focusedId) ? 'up'
              : (downIds.has(edge.to) && (downIds.has(edge.from) || edge.from === focusedId)) ? 'down' : undefined
            return <path key={`${edge.from}>${edge.to}`} d={pathOf(points)} className="station-map__edge" data-chain={tone} />
          })}
          {[...lay.leads].filter(([to]) => (model.stations.get(to)?.gates.length ?? 0) > 0).map(([to, points]) => (
            <path key={`lead>${to}`} d={pathOf(points)} className="station-map__lead" />
          ))}
        </g>
        {compare && (
          <g className="station-map__thread station-map__thread--compare" aria-hidden="true">
            {compare.thread.map(seg => {
              const e = threadEdge(seg)
              return e ? <path key={`${seg.from}>${seg.to}`} d={pathOf(e.points)} transform="translate(0 3)" /> : null
            })}
          </g>
        )}
        <g className="station-map__thread" aria-hidden="true">
          {model.thread.map(seg => {
            const e = threadEdge(seg)
            if (!e) return null
            // A stopped thread ends before the holding gate's bar: past its comb offset, and a gap.
            const target = model.nodes.find(n => n.id === seg.to)
            const holdAt = target ? Math.max(0, gatesOn(target, model).findIndex(g => g.holding)) : 0
            const pts = seg.kind === 'stopped' ? trimEnd(e.points, gateOffset(holdAt) + GRAPH.gateTick)
              : seg.kind === 'snap' ? trimEnd(e.points, GRAPH.station.r + 5) : e.points
            const tip = pointBeforeEnd(pts, 0)
            return (
              <g key={`${seg.from}>${seg.to}`} data-kind={seg.kind}>
                <path d={pathOf(pts)} className={`station-map__thread-path station-map__thread-path--${seg.kind}`} />
                {seg.kind === 'snap' && (
                  <path className="station-map__fray" d={`M${tip.x} ${tip.y} l4 -4 M${tip.x} ${tip.y} l4 4`} />
                )}
              </g>
            )
          })}
        </g>
        <g className="station-map__nodes">
          {model.nodes.map(node => {
            // The layout gives positions only; the data is the current model's
            // (the layout is reused while the topology stays the same).
            const placed = lay.placed.get(node.id)
            return placed ? { ...placed, node } : undefined
          }).filter((p): p is Placed => !!p).map(p => p.node.kind === 'station'
            ? <StationGlyph key={p.node.id} p={p} st={p.node.station} level={level} model={model}
                differs={!!compare && differs(p.node.station, compare.stations.get(p.node.id))}
                focusable={active === p.node.id} focused={focusedId === p.node.id}
                dim={matches.length > 0 && !matchSet.has(p.node.id)}
                chain={upIds.has(p.node.id) ? 'up' : downIds.has(p.node.id) ? 'down' : undefined}
                onOpen={() => onOpenStage(p.node.id)} onFocus={() => setCursor(p.node.id)} />
            : <GroupGlyph key={p.node.id} p={p} g={p.node.group} expanded={p.node.expanded} model={model} level={level} hatch={`${patternId}-hatch`}
                active={active} focusedEnv={focused} matchSet={matchSet} searching={matches.length > 0}
                onToggle={() => onToggleGroup(p.node.id)} onOpen={onOpenStage} onFocusItem={setCursor} />)}
        </g>
        {/* Gates above the stations: a gate label that reaches over a station's label column still takes its own clicks. */}
        <g className="station-map__gates">
          {[...lay.edges.map(({ edge, points }) => ({ to: edge.to, points })), ...[...lay.leads].map(([to, points]) => ({ to, points }))].map(({ to, points }) => {
            const node = model.nodes.find(n => n.id === to)
            if (!node) return null
            const target = { node }
            const gates = gatesOn(node, model)
            if (gates.length === 0) return null
            return gates.map((g, i) => {
              const p = pointBeforeEnd(points, gateOffset(i))
              const blocking = g.holding
              const half = blocking ? GRAPH.gateTick : GRAPH.gateTick / 2
              // Perpendicular to the edge.
              const nx = -p.dy * half
              const ny = p.dx * half
              const env = target.node.kind === 'station' ? target.node.id : target.node.group.members[0]
              const labelled = level === 'detail' && i === 0
              const label = gates.length > 1 ? `${g.name} +${gates.length - 1}` : g.name
              const hit = GRAPH.gateTick + GRAPH.grid.gap
              const labelW = label.length * GATE_CH
              // The label stands above the edge, clear of the thread, with a hairline down to the bar.
              const labelY = p.y - half - GATE_LEADER - 4
              return (
                <g key={`${to}:${g.id}`} className="station-map__gate" data-tone={g.display.tone} data-gate={g.name}
                  tabIndex={0} role="button" aria-label={`gate ${g.name}, ${g.display.label}${g.approval ? `, ${g.approval.approved} of ${g.approval.required} approvals` : ''}`}
                  onPointerEnter={() => showPopover({ gate: g, env, x: p.x, y: p.y })}
                  onPointerLeave={hidePopover}
                  onFocus={() => showPopover({ gate: g, env, x: p.x, y: p.y })}
                  onBlur={() => { cancelHide(); setPopover(null) }}
                  onKeyDown={e => {
                    if (e.key === 'Enter' || e.key === ' ') {
                      e.preventDefault(); e.stopPropagation()
                      if (onOpenGate) onOpenGate(g.id); else onOpenStage(env, 'why')
                    } else if (e.key === 'Escape') { e.stopPropagation(); cancelHide(); setPopover(null) }
                  }}
                  onClick={e => { e.stopPropagation(); if (onOpenGate) onOpenGate(g.id); else onOpenStage(env, 'why') }}>
                  {/* The hit area covers the mark and its label, so a click anywhere on the gate lands. */}
                  <rect className="station-map__gate-hit" x={p.x - (labelled ? labelW : hit)} y={p.y - half - (labelled ? GATE_LABEL_H + GATE_LEADER : hit)}
                    width={(labelled ? labelW : hit) + hit} height={half + hit + (labelled ? GATE_LABEL_H + GATE_LEADER : hit)} />
                  <line className="station-map__gate-bar" x1={p.x - nx} y1={p.y - ny} x2={p.x + nx} y2={p.y + ny} />
                  {labelled && (
                    <>
                      <line className="station-map__gate-leader" x1={p.x} y1={labelY + 3} x2={p.x} y2={p.y - half - 1} />
                      <text x={p.x + 3} y={labelY} textAnchor="end" className="station-map__gate-label">{label}</text>
                    </>
                  )}
                  {/* An approval gate: a person and the quorum ("1/2") under the bar. */}
                  {g.approval && level !== 'overview' && (
                    <g className="station-map__gate-quorum" aria-hidden="true">
                      <circle cx={p.x - GRAPH.gateTick / 2} cy={p.y + half + GRAPH.grid.gap * 2} r={GRAPH.grid.gap} />
                      <path d={`M${p.x - GRAPH.gateTick} ${p.y + half + GRAPH.gateTick} a${GRAPH.gateTick / 2} ${GRAPH.gateTick / 2} 0 0 1 ${GRAPH.gateTick} 0`} />
                      <text x={p.x + GRAPH.grid.gap} y={p.y + half + GRAPH.gateTick}>{`${g.approval.approved}/${g.approval.required}`}</text>
                    </g>
                  )}
                </g>
              )
            })
          })}
        </g>
        {level === 'detail' && actionsFor && (
          <g className="station-map__actions">
            {model.nodes.map(node => {
              // Only the station in focus, or whose drawer is open, offers its actions.
              if (node.kind !== 'station' || (node.id !== cursor && node.id !== focused)) return null
              const placed = lay.placed.get(node.id)
              const content = placed && actionsFor(node.id)
              if (!placed || !content) return null
              const { x } = dotOf(placed)
              // Below the station's labels, as HTML buttons (foreignObject), so they are real buttons.
              return (
                <foreignObject key={node.id} x={x - GRAPH.station.r} y={labelsBottom(placed, level) + GRAPH.grid.gap}
                  width={placed.w} height={GRAPH.rankSep / 2} className="station-actions">
                  <div className="station-actions__row" onPointerDown={e => e.stopPropagation()}>{content}</div>
                </foreignObject>
              )
            })}
          </g>
        )}
      </svg>
      {popover && <GatePopover {...popover} view={vp.view} onPointerEnter={cancelHide} onPointerLeave={hidePopover} />}
      <MapLegend />
      <Minimap lay={lay} view={vp.view} size={vp.size} model={model} onCentre={vp.centre} show={level !== 'detail' || total > 8} />
      <div className="sr-only" aria-live="polite">{announce}</div>
    </div>
  )
}

/** The line height of a station's labels: Detail sets them a step larger (--k-text-sm). */
function lineOf(level: ZoomLevel): number {
  return level === 'detail' ? 17 : 14
}

/** Where a station's labels end (its click target's bottom); its actions go below. */
function labelsBottom(p: Placed, level: ZoomLevel): number {
  const { y } = dotOf(p)
  return y + GRAPH.station.r + lineOf(level) * (level === 'detail' ? 3 : 1) + GRAPH.grid.gap * 3
}

/** How far before the end of an edge the i-th gate mark sits. */
function gateOffset(i: number): number {
  return GRAPH.gateTick * 3 + i * GATE_STEP
}

function boxOf(p: Placed) {
  return { x: p.x - p.w / 2, y: p.y - p.h / 2, w: p.w, h: p.h }
}

/** The gates on the edge into a node: a station's, or a group's worst per name. */
function gatesOn(node: MapNode, model: StationMapModel): GateMark[] {
  if (node.kind === 'station') return node.station.gates
  const byName = new Map<string, GateMark>()
  const rank = (g: GateMark) => (g.holding ? 0 : g.display.tone === 'waiting' ? 1 : g.state === 'Pass' ? 3 : 2)
  for (const m of node.group.members) {
    for (const g of model.stations.get(m)!.gates) {
      const prev = byName.get(g.name)
      if (!prev || rank(g) < rank(prev)) byName.set(g.name, g)
    }
  }
  return [...byName.values()].sort((a, b) => rank(a) - rank(b))
}

/** s cut to n characters with an ellipsis. */
function clip(s: string, n: number): string {
  return s.length <= n ? s : `${s.slice(0, Math.max(1, n - 1))}…`
}

function cssEscape(s: string): string {
  return s.replace(/["\\]/g, '\\$&')
}

/** Whether the compared Bundle stands elsewhere in an environment than the followed one. */
function differs(a: Station, b: Station | undefined): boolean {
  if (!b) return false
  return a.reached !== b.reached || a.display.label !== b.display.label
}

interface StationGlyphProps {
  p: Placed
  st: Station
  /** The compared Bundle stands elsewhere here: a small ≠ (compare mode). */
  differs?: boolean
  level: ZoomLevel
  model: StationMapModel
  focusable: boolean
  focused: boolean
  dim: boolean
  chain?: 'up' | 'down'
  onOpen: () => void
  onFocus: () => void
}

function StationGlyph({ p, st, level, model, focusable, focused, dim, chain, onOpen, onFocus, differs: diff }: StationGlyphProps) {
  const { x, y } = dotOf(p)
  const r = focused ? GRAPH.station.rFocus : GRAPH.station.r
  const tone = st.display.tone
  const onThread = model.thread.some(t => t.to === st.id || t.from === st.id)
  const attention = tone === 'failed' || tone === 'blocked' || tone === 'held'
  const showLabel = level !== 'overview' || onThread || attention || focused
  // At Map zoom a version shows only where it differs from its upstream's.
  const upVersion = st.upstreams.length ? model.stations.get(st.upstreams[0])?.running?.version : undefined
  const showVersion = level === 'detail' || (level === 'map' && st.running?.version !== upVersion)
  const age = ageOf(st.running?.verifiedAt)
  const pr = st.incoming?.prURL ? prNumberFromURL(st.incoming.prURL) : null
  const line = lineOf(level)
  return (
    <g className="station" data-station={st.env} data-node-id={st.stepId} data-focus-id={st.env} data-tone={tone} data-dim={dim || undefined}
      data-chain={chain} data-focused={focused || undefined}
      role="button" tabIndex={focusable ? 0 : -1} aria-label={stationLabel(st, ageOf) + (diff ? ', the compared version differs here' : '')} aria-current={focused ? 'true' : undefined}
      onClick={onOpen} onFocus={onFocus}>
      {/* The whole station is the target: the dot and its labels. */}
      <rect className="station__area" x={p.x - p.w / 2 - 4} y={p.y - p.h / 2} width={p.w + 8} height={Math.min(p.h, labelsBottom(p, level) - (p.y - p.h / 2))} />
      <circle className="station__hit" cx={x} cy={y} r={r + 8} />
      {tone === 'held' && <circle className="station__ring2" cx={x} cy={y} r={r + 3} />}
      <circle className="station__dot" cx={x} cy={y} r={r} />
      {tone === 'failed' && <path className="station__notch" d={`M${x} ${y} L${x + r * 1.2} ${y - r * 0.7} L${x + r * 1.2} ${y + r * 0.7} Z`} />}
      {tone === 'promoting' && <circle className="station__arc" cx={x} cy={y} r={r + 3} pathLength={100} />}
      {diff && <text x={x + r + 3} y={y - r - 2} className="station__differs" aria-hidden="true">≠</text>}
      {showLabel && (
        <text x={x - r} y={y - r - 6} className="station__name">{st.env}</text>
      )}
      {showLabel && level !== 'overview' && (
        <text x={x - r} y={y + r + line} className="station__sub">
          {showVersion && st.running ? <tspan className="station__version">{st.running.version}</tspan> : null}
          {showVersion && st.running && age ? <tspan>{` · ${age}`}</tspan> : null}
          {!st.running && level === 'detail' ? <tspan>nothing deployed</tspan> : null}
        </text>
      )}
      {level === 'detail' && st.incoming && (
        <>
          <text x={x - r} y={y + r + line * 2} className="station__incoming" data-tone={tone}>{clip(`→ ${st.incoming.version}`, detailChars(p))}</text>
          <text x={x - r} y={y + r + line * 3} className="station__incoming" data-tone={tone}>
            {clip(`${st.display.label}${pr ? ` · PR ${pr}` : ''}`, detailChars(p))}
          </text>
        </>
      )}
      {level === 'detail' && !st.incoming && st.display.tone !== 'verified' && st.display.tone !== 'pending' && (
        <text x={x - r} y={y + r + line * 2} className="station__incoming" data-tone={tone}>{st.display.label}</text>
      )}
    </g>
  )
}

interface GroupGlyphProps {
  p: Placed
  g: Group
  expanded: boolean
  model: StationMapModel
  level: ZoomLevel
  active?: string
  focusedEnv?: string
  matchSet: Set<string>
  searching: boolean
  onToggle: () => void
  onOpen: (env: string) => void
  onFocusItem: (id: string) => void
  /** The hatch pattern's id, for held cells. */
  hatch: string
}

function GroupGlyph({ p, g, expanded, model, level, active, focusedEnv, matchSet, searching, onToggle, onOpen, onFocusItem, hatch }: GroupGlyphProps) {
  const box = boxOf(p)
  const total = g.members.length
  const pacing = groupPacing(g)
  // The accessible name says it in full; the head shows "EDGE · 12", sized to fit (no label is cut).
  const name = g.kind === 'platform' ? `${g.label} · ${total} targets` : `${g.label} · ${total}`
  const title = groupTitle(g)
  const summary = groupSummary(g)
  const barW = box.w - 24
  let at = 0
  const segments = g.counts.map(c => {
    const w = (c.count / total) * barW
    const seg = { tone: c.tone, x: box.x + 12 + at, w }
    at += w
    return seg
  })
  const head = (
    <g className="group__head" data-focus-id={g.id} role="button" tabIndex={active === g.id ? 0 : -1}
      aria-expanded={expanded} aria-label={`${name}${pacing ? `, ${pacing}` : ''}: ${summary}`}
      onClick={onToggle} onFocus={() => onFocusItem(g.id)}>
      <rect className="group__frame" x={box.x} y={box.y} width={box.w} height={box.h} />
      <text x={box.x + 12} y={box.y + 15} className="group__label">{title}</text>
      <text x={box.x + box.w - 12} y={box.y + 15} textAnchor="end" className="group__chevron" aria-hidden="true">{expanded ? '▾' : '▸'}</text>
      {/* A minority of failures is a mark on the band; the thread still fills by the share verified. */}
      {g.failed > 0 && !g.halted && !expanded && (
        <text x={box.x + box.w - 26} y={box.y + 15} textAnchor="end" className="group__fail" aria-hidden="true">{`✕ ${g.failed}`}</text>
      )}
      {!expanded && (
        <>
          {segments.map(s => <rect key={s.tone} className="group__seg" data-tone={s.tone} x={s.x} y={box.y + 24} width={Math.max(0, s.w - 1)} height={BAR.seg} />)}
          {g.verified > 0 && <rect className="group__thread" x={box.x + 12} y={box.y + 34} width={(g.verified / total) * barW} height={BAR.thread} />}
          {level !== 'overview' && (
            <text x={box.x + 12} y={box.y + box.h - 7} className="group__sub">
              {groupSub(g)}
            </text>
          )}
        </>
      )}
    </g>
  )
  if (!expanded) return <g className="group" data-group={g.id}>{head}</g>
  const order = gridOrder(g, model.stations)
  const cells = gridCells(g, order, box, level)
  const text = gridText(g, box)
  const foot = groupSub(g)
  const c = GRAPH.grid.cell
  return (
    <g className="group group--expanded" data-group={g.id}>
      {head}
      {text.rows.map(r => (
        <text key={`${r.y}`} x={r.x} y={r.y} className="group__row" data-tone={r.tone}>{r.text}</text>
      ))}
      {cells.map(cell => {
        const st = model.stations.get(cell.env)!
        const tone = st.display.tone
        return (
          <g key={cell.env} className="cell" data-station={cell.env} data-focus-id={cell.env} data-tone={tone}
            data-dim={(searching && !matchSet.has(cell.env)) || undefined} data-focused={focusedEnv === cell.env || undefined}
            role="button" tabIndex={active === cell.env ? 0 : -1} aria-label={stationLabel(st, ageOf)}
            onClick={() => onOpen(cell.env)} onFocus={() => onFocusItem(cell.env)}>
            <rect className="cell__box" x={cell.x} y={cell.y} width={c} height={c} />
            {/* State by shape as well as hue: ✕ failed, hatch held, a bar blocked, a dot promoting. */}
            {tone === 'failed' && <path className="cell__mark" d={`M${cell.x + 2} ${cell.y + 2} L${cell.x + c - 2} ${cell.y + c - 2} M${cell.x + c - 2} ${cell.y + 2} L${cell.x + 2} ${cell.y + c - 2}`} />}
            {tone === 'held' && <rect className="cell__held" x={cell.x} y={cell.y} width={c} height={c} fill={`url(#${hatch})`} />}
            {tone === 'blocked' && <path className="cell__mark" d={`M${cell.x + 2} ${cell.y + c / 2} L${cell.x + c - 2} ${cell.y + c / 2}`} />}
            {tone === 'promoting' && <circle className="cell__dot" cx={cell.x + c / 2} cy={cell.y + c / 2} r={c / 5} />}
          </g>
        )
      })}
      <text x={text.foot.x} y={text.foot.y} className="group__sub">{foot}</text>
    </g>
  )
}

function GatePopover({ gate, env, x, y, view, onPointerEnter, onPointerLeave }: {
  gate: GateMark; env: string; x: number; y: number; view: { x: number; y: number; k: number }
  onPointerEnter: () => void; onPointerLeave: () => void
}) {
  const left = (x - view.x) * view.k
  const top = (y - view.y) * view.k
  const age = gate.lastEvaluatedAt ? ageOf(gate.lastEvaluatedAt) : ''
  // Its place is computed from the zoom, so it is set on the element, not as an inline style.
  const place = useCallback((el: HTMLDivElement | null) => {
    if (!el) return
    el.style.setProperty('left', `${left}px`)
    el.style.setProperty('top', `${top}px`)
  }, [left, top])
  return (
    <div ref={place} className="gate-popover" role="tooltip" onPointerEnter={onPointerEnter} onPointerLeave={onPointerLeave}>
      <div className="gate-popover__name"><Icon name="gate" className="gate-popover__icon" /> {gate.name}</div>
      <div className="gate-popover__state" data-tone={gate.display.tone}>{gate.display.label} · into {env}</div>
      {gate.approval && <div className="gate-popover__quorum">{`${gate.approval.approved} of ${gate.approval.required} approvals`}</div>}
      {gate.expression && <code className="gate-popover__cel">{gate.expression}</code>}
      {gate.message && <div className="gate-popover__reason">{gate.message}</div>}
      {age && <div className="gate-popover__age">evaluated {age === 'now' ? 'just now' : `${age} ago`}</div>}
      <div className="gate-popover__hint">Click the gate to see why in the stage drawer</div>
    </div>
  )
}

/** What the map's marks mean (the DAG legend's wording, for the map). */
const LEGEND: Array<{ tone: string; label: string; desc: string }> = [
  { tone: 'verified', label: 'Live', desc: 'Deployed and healthy' },
  { tone: 'promoting', label: 'In progress', desc: 'Promoting, waiting for the PR to merge, or checking health' },
  { tone: 'failed', label: 'Failed', desc: 'Failed, or stopped by an alarm' },
  { tone: 'blocked', label: 'Held', desc: 'A policy gate or a rollback holds it' },
  { tone: 'pending', label: 'Not reached', desc: 'Not reached yet, or waiting its turn' },
]

function MapLegend() {
  return (
    <details className="station-map__legend">
      <summary>Legend</summary>
      <ul>
        <li><span className="legend__dot" data-tone="verified" aria-hidden="true" /> Environment</li>
        <li><Icon name="gate" className="legend__icon" /> Policy gate, on the edge into the environment it guards</li>
        <li><span className="legend__thread" aria-hidden="true" /> The version you follow</li>
        {LEGEND.map(l => (
          <li key={l.label} title={l.desc}><span className="legend__dot" data-tone={l.tone} aria-hidden="true" /> {l.label}: {l.desc.toLowerCase()}</li>
        ))}
      </ul>
    </details>
  )
}

function Minimap({ lay, view, size, model, onCentre, show }: {
  lay: Layout; view: { x: number; y: number; k: number }; size: { w: number; h: number }; model: StationMapModel
  onCentre: (x: number, y: number) => void; show: boolean
}) {
  const W = MINIMAP_W
  const s = W / Math.max(1, lay.bounds.w)
  const H = Math.max(40, Math.min(120, lay.bounds.h * s))
  const sy = H / Math.max(1, lay.bounds.h)
  const sc = Math.min(s, sy)
  // Inset in the map's corner; it hides while anything drawn is under it.
  const corner = { x: size.w - W - MINIMAP_INSET * 2, y: size.h - H - MINIMAP_INSET * 2 }
  const overlaps = [...lay.placed.values()].some(p => {
    const right = (p.x + p.w / 2 - view.x) * view.k
    const bottom = (p.y + p.h / 2 - view.y) * view.k
    const left = (p.x - p.w / 2 - view.x) * view.k
    const top = (p.y - p.h / 2 - view.y) * view.k
    return right > corner.x && bottom > corner.y && left < size.w && top < size.h
  })
  if (!show || overlaps) return null
  return (
    <svg className="station-map__minimap" width={W} height={H} aria-hidden="true"
      onPointerDown={e => {
        const r = e.currentTarget.getBoundingClientRect()
        onCentre((e.clientX - r.left) / sc, (e.clientY - r.top) / sc)
      }}>
      {lay.edges.map(({ edge, points }) => (
        <path key={`${edge.from}>${edge.to}`} d={pathOf(points.map(([px, py]) => [px * sc, py * sc]))} className="minimap__edge" />
      ))}
      {[...lay.placed.values()].map(p => (
        <rect key={p.node.id} x={(p.x - p.w / 2) * sc} y={(p.y - p.h / 2) * sc} width={Math.max(2, p.w * sc)} height={Math.max(2, p.h * sc)}
          className="minimap__node" data-tone={minimapTone(model, p.node.id)} />
      ))}
      {model.thread.length > 0 && <rect className="minimap__thread-hint" x={0} y={H - BAR.hint} width={W} height={BAR.hint} />}
      <rect className="minimap__view" x={view.x * sc} y={view.y * sc} width={(size.w / view.k) * sc} height={(size.h / view.k) * sc} />
    </svg>
  )
}

function minimapTone(model: StationMapModel, id: string): string | undefined {
  const st = model.stations.get(id)
  if (st) return st.display.tone
  // A group reads as what most of it is (its failures are marks on the map itself).
  const counts = model.groups.get(id)?.counts ?? []
  return [...counts].sort((a, b) => b.count - a.count)[0]?.tone
}
