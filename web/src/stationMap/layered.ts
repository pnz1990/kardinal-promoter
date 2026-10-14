// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// stationMap/layered.ts — a layered (Sugiyama) layout, left to right, for the
// station map's collapsed graph: a few dozen nodes at most, since waves are
// bands and fleets platforms. It replaces @dagrejs/dagre (about 16 KB gzip of
// the initial load) with what the map uses of it:
//   1. ranks: the longest path from the roots, roots pulled up to just before
//      their first successor, so every edge points right;
//   2. an edge longer than one rank gets a point in each rank it crosses, so
//      it keeps a lane of its own instead of running through a station;
//   3. order within a rank: barycenter sweeps from the model's order, keeping
//      the order with the fewest crossings;
//   4. positions: ranks are columns as wide as their widest node, rankSep
//      apart; within a rank nodes keep nodeSep between them and move toward
//      the mean of their neighbours, so a chain runs straight.

export interface LayeredNode {
  id: string
  w: number
  h: number
}

export interface LayeredOptions {
  nodeSep: number
  rankSep: number
  marginX: number
  marginY: number
}

interface Item {
  id: string
  w: number
  h: number
  rank: number
  order: number
  y: number
  /** The index of the node in the input, or of the long edge's source. */
  seq: number
}

const SWEEPS = 8

/** layered returns the centre of each node. Edges to unknown nodes and
 *  self-loops are ignored; a cycle is broken at the edges into nodes already
 *  ranked. */
export function layered(nodes: LayeredNode[], edges: Array<{ from: string; to: string }>, opt: LayeredOptions): Map<string, { x: number; y: number }> {
  const items = new Map<string, Item>()
  nodes.forEach((n, i) => items.set(n.id, { id: n.id, w: n.w, h: n.h, rank: 0, order: 0, y: 0, seq: i }))
  const ok = edges.filter(e => e.from !== e.to && items.has(e.from) && items.has(e.to))

  // 1. Ranks, in topological order (Kahn); nodes left on a cycle follow in
  // input order, with the edges into them that would close it dropped.
  const preds = new Map<string, string[]>()
  const succs = new Map<string, string[]>()
  for (const id of items.keys()) {
    preds.set(id, [])
    succs.set(id, [])
  }
  for (const e of ok) {
    succs.get(e.from)!.push(e.to)
    preds.get(e.to)!.push(e.from)
  }
  const indeg = new Map([...items.keys()].map(id => [id, preds.get(id)!.length]))
  const topo: string[] = []
  const queue = [...items.keys()].filter(id => indeg.get(id) === 0)
  const done = new Set<string>()
  while (topo.length < items.size) {
    if (queue.length === 0) {
      const next = [...items.keys()].find(id => !done.has(id))!
      queue.push(next)
    }
    const id = queue.shift()!
    if (done.has(id)) continue
    done.add(id)
    topo.push(id)
    for (const s of succs.get(id)!) {
      indeg.set(s, indeg.get(s)! - 1)
      if (indeg.get(s) === 0) queue.push(s)
    }
  }
  const pos = new Map(topo.map((id, i) => [id, i]))
  const forward = ok.filter(e => pos.get(e.from)! < pos.get(e.to)!)
  const fwdPreds = new Map([...items.keys()].map(id => [id, forward.filter(e => e.to === id).map(e => e.from)]))
  const fwdSuccs = new Map([...items.keys()].map(id => [id, forward.filter(e => e.from === id).map(e => e.to)]))
  for (const id of topo) {
    const ps = fwdPreds.get(id)!
    items.get(id)!.rank = ps.length ? Math.max(...ps.map(p => items.get(p)!.rank + 1)) : 0
  }
  for (const id of [...topo].reverse()) {
    const it = items.get(id)!
    const ss = fwdSuccs.get(id)!
    if (fwdPreds.get(id)!.length === 0 && ss.length) it.rank = Math.min(...ss.map(s => items.get(s)!.rank)) - 1
  }
  const minRank = Math.min(...[...items.values()].map(i => i.rank))
  for (const it of items.values()) it.rank -= minRank

  // 2. Long edges become chains through dummy points.
  const all = new Map(items)
  const links: Array<[string, string]> = []
  for (const e of forward) {
    const a = items.get(e.from)!
    const b = items.get(e.to)!
    let prev = a.id
    for (let r = a.rank + 1; r < b.rank; r++) {
      const id = `\u0000${e.from}\u0000${e.to}\u0000${r}`
      all.set(id, { id, w: 0, h: 0, rank: r, order: 0, y: 0, seq: a.seq })
      links.push([prev, id])
      prev = id
    }
    links.push([prev, b.id])
  }
  const up = new Map([...all.keys()].map(id => [id, [] as string[]]))
  const down = new Map([...all.keys()].map(id => [id, [] as string[]]))
  for (const [a, b] of links) {
    down.get(a)!.push(b)
    up.get(b)!.push(a)
  }
  const ranks: Item[][] = []
  for (const it of [...all.values()].sort((x, y) => x.seq - y.seq)) (ranks[it.rank] ??= []).push(it)
  for (let r = 0; r < ranks.length; r++) ranks[r] ??= []
  const renumber = () => ranks.forEach(rank => rank.forEach((it, i) => { it.order = i }))
  renumber()

  // 3. Order: barycenter sweeps, keeping the best.
  const crossings = () => {
    let n = 0
    for (let r = 0; r + 1 < ranks.length; r++) {
      const segs: Array<[number, number]> = []
      for (const it of ranks[r]) for (const t of down.get(it.id)!) segs.push([it.order, all.get(t)!.order])
      for (let i = 0; i < segs.length; i++)
        for (let j = i + 1; j < segs.length; j++)
          if ((segs[i][0] - segs[j][0]) * (segs[i][1] - segs[j][1]) < 0) n++
    }
    return n
  }
  let best = ranks.map(rank => [...rank])
  let bestN = crossings()
  for (let s = 0; s < SWEEPS && bestN > 0; s++) {
    const downward = s % 2 === 0
    const seq = downward ? ranks.map((_, i) => i).slice(1) : ranks.map((_, i) => i).slice(0, -1).reverse()
    for (const r of seq) {
      const bary = new Map(ranks[r].map(it => {
        const ns = (downward ? up : down).get(it.id)!.map(n => all.get(n)!.order)
        return [it.id, ns.length ? ns.reduce((a, b) => a + b, 0) / ns.length : it.order]
      }))
      ranks[r].sort((a, b) => bary.get(a.id)! - bary.get(b.id)! || a.order - b.order)
      ranks[r].forEach((it, i) => { it.order = i })
    }
    const n = crossings()
    if (n < bestN) {
      bestN = n
      best = ranks.map(rank => [...rank])
    }
  }
  best.forEach((rank, r) => { ranks[r] = rank })
  renumber()

  // 4. Positions.
  const gap = (a: Item, b: Item) => (a.h + b.h) / 2 + opt.nodeSep
  const place = (rank: Item[], want: (it: Item) => number | undefined) => {
    if (rank.length === 0) return
    const desired = rank.map(want)
    let y = 0
    rank.forEach((it, i) => {
      const d = desired[i] ?? (i === 0 ? it.y : rank[i - 1].y + gap(rank[i - 1], it))
      y = i === 0 ? d : Math.max(d, y + gap(rank[i - 1], it))
      it.y = y
    })
    const off = rank.map((it, i) => (desired[i] === undefined ? undefined : desired[i]! - it.y)).filter((d): d is number => d !== undefined)
    if (off.length) {
      const shift = off.reduce((a, b) => a + b, 0) / off.length
      for (const it of rank) it.y += shift
    }
  }
  const mean = (ids: string[]) => (ids.length ? ids.reduce((a, id) => a + all.get(id)!.y, 0) / ids.length : undefined)
  for (const rank of ranks) {
    let y = 0
    rank.forEach((it, i) => {
      if (i > 0) y += gap(rank[i - 1], it)
      it.y = y
    })
  }
  for (let pass = 0; pass < 3; pass++) {
    for (let r = 1; r < ranks.length; r++) place(ranks[r], it => mean(up.get(it.id)!))
    for (let r = ranks.length - 2; r >= 0; r--) place(ranks[r], it => mean(down.get(it.id)!))
  }
  for (let r = 1; r < ranks.length; r++) place(ranks[r], it => mean(up.get(it.id)!))

  const top = Math.min(...[...items.values()].map(it => it.y - it.h / 2))
  const widths = ranks.map(rank => Math.max(0, ...rank.map(it => it.w)))
  const left: number[] = []
  let x = opt.marginX
  widths.forEach((w, r) => {
    left[r] = x
    x += w + opt.rankSep
  })
  const out = new Map<string, { x: number; y: number }>()
  for (const it of items.values()) out.set(it.id, { x: left[it.rank] + widths[it.rank] / 2, y: it.y - top + opt.marginY })
  return out
}
