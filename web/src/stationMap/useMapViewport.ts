// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// stationMap/useMapViewport.ts — pan and zoom for the station map: drag to
// pan, wheel or pinch to zoom around the pointer, +/- and 0 (fit). The view
// is an SVG viewBox; no d3-zoom. Zoom crosses the semantic levels at
// GRAPH.zoom.overview and GRAPH.zoom.detail.

import { useCallback, useEffect, useRef, useState } from 'react'
import { GRAPH, levelAt, zoomFor, type Box, type ZoomLevel } from './geometry'

export interface Viewport {
  /** Top-left of the view in map units, and the zoom (screen px per map unit). */
  x: number
  y: number
  k: number
}

const clampK = (k: number) => Math.min(GRAPH.zoom.max, Math.max(GRAPH.zoom.min, k))

export interface MapViewport {
  view: Viewport
  level: ZoomLevel
  /** The viewBox of the SVG for an element of size w × h. */
  viewBox: string
  /** Zoom by a factor around a screen point (default: the centre). */
  zoomBy: (f: number, at?: { x: number; y: number }) => void
  /** Show a level: zoom to its factor, around the centre. */
  setLevel: (level: ZoomLevel) => void
  /** Fit bounds into the element. */
  fit: () => void
  /** Pan so a map point is in view (with a margin), keeping the zoom. */
  reveal: (x: number, y: number) => void
  /** Centre the view on a map point. */
  centre: (x: number, y: number) => void
  /** Handlers for the SVG element. */
  handlers: {
    onPointerDown: (e: React.PointerEvent<SVGSVGElement>) => void
    onPointerMove: (e: React.PointerEvent<SVGSVGElement>) => void
    onPointerUp: (e: React.PointerEvent<SVGSVGElement>) => void
  }
  /** The element size in px. */
  size: { w: number; h: number }
}

/**
 * useMapViewport tracks the view of an SVG of map bounds `bounds` inside the
 * element `ref`. initial is the level the map opens at: Detail and Map fit
 * the map when it is smaller than the view, Overview fits it whole.
 */
/** How far past Detail's zoom a small pipeline may be fitted. */
const DETAIL_FILL = 4 / 3

export function useMapViewport(ref: React.RefObject<SVGSVGElement | null>, bounds: Box, initial: ZoomLevel): MapViewport {
  const [size, setSize] = useState({ w: 800, h: 480 })
  // The first fit waits for the element's measured size (jsdom never measures).
  const [measured, setMeasured] = useState(typeof ResizeObserver === 'undefined')
  const [view, setView] = useState<Viewport>({ x: 0, y: 0, k: zoomFor(initial) })
  const fitted = useRef(false)
  // The user zoomed or picked a level: from then on the zoom decides the level.
  const [zoomed, setZoomed] = useState(false)
  const drag = useRef<{ id: number; x: number; y: number; vx: number; vy: number; moved: boolean } | null>(null)
  const pinch = useRef(new Map<number, { x: number; y: number }>())

  useEffect(() => {
    const el = ref.current
    if (!el || typeof ResizeObserver === 'undefined') return
    const ro = new ResizeObserver(entries => {
      const r = entries[0].contentRect
      if (r.width > 0 && r.height > 0) {
        setSize({ w: r.width, h: r.height })
        setMeasured(true)
      }
    })
    ro.observe(el)
    return () => ro.disconnect()
  }, [ref])

  const fitView = useCallback((b: Box, s: { w: number; h: number }, cap: number): Viewport => {
    const k = clampK(Math.min(cap, s.w / Math.max(1, b.w), s.h / Math.max(1, b.h)))
    return { k, x: b.x + b.w / 2 - s.w / k / 2, y: b.y + b.h / 2 - s.h / k / 2 }
  }, [])

  // Fit once the element has a size, and again while it settles (the page
  // around the map loads), until the user pans or zooms: Overview fits whole;
  // Detail and Map fit only when the map is smaller than the view at their zoom.
  useEffect(() => {
    if (fitted.current || bounds.w <= 0 || !measured) return
    // Fit at most at the level's zoom: a small map is not blown up, a wide one
    // is shown whole (it opens a level lower if it must).
    // The level the map opens at is its size's (a collapsed 300-environment
    // graph is small); the zoom only fits it, at most at Detail's.
    // A small pipeline fills the canvas at Detail, up to a third above Detail's zoom.
    const k0 = initial === 'detail' ? zoomFor('detail') * DETAIL_FILL : GRAPH.zoom.detail
    setView(fitView(bounds, size, k0))
  }, [bounds, size, initial, fitView, measured])

  const zoomBy = useCallback((f: number, at?: { x: number; y: number }) => {
    fitted.current = true
    setZoomed(true)
    setView(v => {
      const k = clampK(v.k * f)
      const px = at?.x ?? size.w / 2
      const py = at?.y ?? size.h / 2
      // Keep the map point under (px, py) fixed.
      const mx = v.x + px / v.k
      const my = v.y + py / v.k
      return { k, x: mx - px / k, y: my - py / k }
    })
  }, [size])

  const setLevel = useCallback((level: ZoomLevel) => {
    fitted.current = true
    setZoomed(true)
    setView(v => {
      const k = zoomFor(level)
      const cx = v.x + size.w / v.k / 2
      const cy = v.y + size.h / v.k / 2
      return { k, x: cx - size.w / k / 2, y: cy - size.h / k / 2 }
    })
  }, [size])

  const fit = useCallback(() => { fitted.current = true; setView(fitView(bounds, size, GRAPH.zoom.max)) }, [bounds, size, fitView])

  const reveal = useCallback((x: number, y: number) => {
    setView(v => {
      const margin = 48 / v.k
      const w = size.w / v.k
      const h = size.h / v.k
      let nx = v.x
      let ny = v.y
      if (x < v.x + margin) nx = x - margin
      else if (x > v.x + w - margin) nx = x - w + margin
      if (y < v.y + margin) ny = y - margin
      else if (y > v.y + h - margin) ny = y - h + margin
      return nx === v.x && ny === v.y ? v : { ...v, x: nx, y: ny }
    })
  }, [size])

  const centre = useCallback((x: number, y: number) => {
    fitted.current = true
    setView(v => ({ ...v, x: x - size.w / v.k / 2, y: y - size.h / v.k / 2 }))
  }, [size])

  // Wheel zoom needs a non-passive listener to prevent the page scroll.
  useEffect(() => {
    const el = ref.current
    if (!el) return
    const onWheel = (e: WheelEvent) => {
      e.preventDefault()
      const r = el.getBoundingClientRect()
      const f = Math.exp(-e.deltaY * (e.ctrlKey ? 0.01 : 0.0015))
      zoomBy(f, { x: e.clientX - r.left, y: e.clientY - r.top })
    }
    el.addEventListener('wheel', onWheel, { passive: false })
    return () => el.removeEventListener('wheel', onWheel)
  }, [ref, zoomBy])

  const handlers = {
    onPointerDown: (e: React.PointerEvent<SVGSVGElement>) => {
      pinch.current.set(e.pointerId, { x: e.clientX, y: e.clientY })
      if (pinch.current.size === 1) {
        drag.current = { id: e.pointerId, x: e.clientX, y: e.clientY, vx: view.x, vy: view.y, moved: false }
      }
    },
    onPointerMove: (e: React.PointerEvent<SVGSVGElement>) => {
      const prev = pinch.current.get(e.pointerId)
      if (!prev) return
      if (pinch.current.size === 2) {
        const pts = [...pinch.current.entries()]
        const other = pts.find(([id]) => id !== e.pointerId)![1]
        const before = Math.hypot(prev.x - other.x, prev.y - other.y)
        const after = Math.hypot(e.clientX - other.x, e.clientY - other.y)
        pinch.current.set(e.pointerId, { x: e.clientX, y: e.clientY })
        const r = e.currentTarget.getBoundingClientRect()
        if (before > 0) zoomBy(after / before, { x: (e.clientX + other.x) / 2 - r.left, y: (e.clientY + other.y) / 2 - r.top })
        return
      }
      pinch.current.set(e.pointerId, { x: e.clientX, y: e.clientY })
      const d = drag.current
      if (!d || d.id !== e.pointerId) return
      const dx = e.clientX - d.x
      const dy = e.clientY - d.y
      if (!d.moved && Math.hypot(dx, dy) < 4) return
      if (!d.moved) {
        d.moved = true
        fitted.current = true
        e.currentTarget.setPointerCapture(e.pointerId)
      }
      setView(v => ({ ...v, x: d.vx - dx / v.k, y: d.vy - dy / v.k }))
    },
    onPointerUp: (e: React.PointerEvent<SVGSVGElement>) => {
      pinch.current.delete(e.pointerId)
      if (drag.current?.id === e.pointerId) {
        if (drag.current.moved) {
          // A drag is not a click on what is under the pointer.
          const stop = (ev: Event) => { ev.stopPropagation(); ev.preventDefault() }
          window.addEventListener('click', stop, { capture: true, once: true })
          setTimeout(() => window.removeEventListener('click', stop, { capture: true }), 0)
        }
        drag.current = null
      }
    },
  }

  return {
    view,
    // Until the user zooms, the level is the one the map opened at.
    level: zoomed ? levelAt(view.k) : initial,
    viewBox: `${view.x} ${view.y} ${size.w / view.k} ${size.h / view.k}`,
    zoomBy, setLevel, fit, reveal, centre, handlers, size,
  }
}
