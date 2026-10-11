// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// useUrlState.ts — the UI's routes, kept in the URL hash so every screen can be linked,
// survives a reload, and Back and Forward walk it (UX-AUDIT.md §4.3):
//
//   #/                                    Pipelines (the fleet)
//   #/p/<ns>/<pipeline>                   a Pipeline
//   #/p/<ns>/<pipeline>/env/<env>         … with an environment's details open
//   #/p/<ns>/<pipeline>/v/<bundle>        … showing that version (Bundle)
//   #/approvals                           approvals (reserved; shows the fleet for now)
//
// and the query after `?`: `tab=versions|activity|metrics` (the pipeline page's tab; the
// older `tab=history` reads as versions and `tab=gates` as the map), `view=ops`
// (the operations table), `filter=` (a fleet filter),
// `q=` (the pipeline search), `compare=<bundle>` (the comparison panel), `node=<id>` (a gate's
// details), `v=<bundle>` (the version shown while an environment is open), `section=` (the
// stage drawer section to scroll to). Links from before
// these routes (`#pipeline=…&ns=…&node=…&bundle=…`) are rewritten in place, so they keep
// working.
import { useCallback, useEffect, useRef, useState } from 'react'

/** The parsed route. Undefined means "not set". */
export interface UrlState {
  pipeline: string | undefined
  /** Namespace of the selected pipeline (unknown for a few old links). */
  ns: string | undefined
  /** The environment whose details are open. */
  env: string | undefined
  /** The version (Bundle) shown, when the user picked one. */
  version: string | undefined
  /** The Bundle compared with the one shown (opens the comparison panel). */
  compare: string | undefined
  /** A graph node whose details are open that is not an environment (a gate). */
  node: string | undefined
  /** 'ops': the operations table instead of the fleet board. */
  view: 'ops' | undefined
  /** The fleet filter (FleetHealthBar). */
  filter: string | undefined
  /** The pipeline search. */
  q: string | undefined
  /** The pipeline page's tab: versions, activity or metrics (the map when unset). */
  tab: PipelineTab | undefined
  /** #/approvals (reserved). */
  approvals: boolean | undefined
  /** The stage drawer section to scroll to (why, actions, evidence, history; #1701). */
  section: string | undefined
  /** How the Map tab shows the pipeline: as a table or a matrix (the station map when unset). */
  as: MapView | undefined
}

/** The Map tab's other views of the same pipeline (DESIGN-SYSTEM §3.5). */
export type MapView = 'table' | 'matrix'

export const EMPTY_URL_STATE: UrlState = {
  pipeline: undefined, ns: undefined, env: undefined, version: undefined, compare: undefined,
  node: undefined, view: undefined, filter: undefined, q: undefined, tab: undefined, approvals: undefined,
  section: undefined, as: undefined,
}

/** The namespace segment of a link that does not know it. */
const ANY_NS = '*'

const QUERY_KEYS = ['tab', 'view', 'filter', 'q', 'compare', 'node', 'section', 'as'] as const
export type PipelineTab = 'versions' | 'activity' | 'metrics'
const TABS: readonly string[] = ['versions', 'activity', 'metrics']
/** Tabs from before Map | Versions | Activity | Metrics: what each now opens (undefined: the map). */
const OLD_TABS: Record<string, PipelineTab | undefined> = { history: 'versions', gates: undefined }

/** Whether a hash uses the routes from before #/…: `#pipeline=…&ns=…`. */
export function isLegacyHash(hash: string): boolean {
  const h = hash.startsWith('#') ? hash.slice(1) : hash
  return h !== '' && !h.startsWith('/') && /(^|&)(pipeline|ns|node|bundle)=/.test(h)
}

const decode = (s: string) => {
  try { return decodeURIComponent(s) } catch { return s }
}

/** Parses a URL hash (either form) into a UrlState. */
export function parseHash(hash: string): UrlState {
  const h = hash.startsWith('#') ? hash.slice(1) : hash
  if (isLegacyHash(hash)) {
    const p = new URLSearchParams(h)
    return {
      ...EMPTY_URL_STATE,
      pipeline: p.get('pipeline') || undefined,
      ns: p.get('ns') || undefined,
      node: p.get('node') || undefined,
      // bundle= opened the comparison with that Bundle.
      compare: p.get('bundle') || undefined,
    }
  }
  const [path, query = ''] = h.split('?', 2)
  const seg = path.split('/').filter(Boolean).map(decode)
  const params = new URLSearchParams(query)
  const out: UrlState = { ...EMPTY_URL_STATE }
  for (const k of QUERY_KEYS) {
    const v = params.get(k)
    if (!v) continue
    if (k === 'view') out.view = v === 'ops' ? 'ops' : undefined
    else if (k === 'tab') out.tab = TABS.includes(v) ? (v as PipelineTab) : OLD_TABS[v]
    else if (k === 'as') out.as = v === 'table' || v === 'matrix' ? v : undefined
    else out[k] = v
  }
  if (seg[0] === 'p' && seg[2]) {
    out.ns = seg[1] === ANY_NS ? undefined : seg[1]
    out.pipeline = seg[2]
    if (seg[3] === 'env' && seg[4]) {
      out.env = seg[4]
      out.version = params.get('v') || undefined
    }
    if (seg[3] === 'v' && seg[4]) out.version = seg[4]
  } else if (seg[0] === 'approvals') {
    out.approvals = true
  }
  return out
}

/** Serializes a UrlState to a hash (with the leading #), in the #/… form. */
export function serializeHash(state: UrlState): string {
  const enc = encodeURIComponent
  let path = '/'
  const params = new URLSearchParams()
  if (state.pipeline) {
    path = `/p/${enc(state.ns ?? ANY_NS)}/${enc(state.pipeline)}`
    if (state.env) {
      path += `/env/${enc(state.env)}`
      if (state.version) params.set('v', state.version)
    } else if (state.version) {
      path += `/v/${enc(state.version)}`
    }
  } else if (state.approvals) {
    path = '/approvals'
  }
  for (const k of QUERY_KEYS) {
    const v = state[k]
    if (v) params.set(k, v)
  }
  const q = params.toString()
  return `#${path}${q ? `?${q}` : ''}`
}

export interface SetUrlState {
  (next: Partial<UrlState>, options?: { replace?: boolean }): void
}

/**
 * useUrlState synchronizes a UrlState with window.location.hash.
 *
 * Returns [state, setState] where state follows the hash (Back and Forward included) and
 * setState merges the given fields and pushes one history entry (or replaces the current
 * one with `{ replace: true }`), so a change that touches several fields is one Back press.
 * An old-style link is rewritten to its #/… form without a new history entry.
 */
export function useUrlState(): [UrlState, SetUrlState] {
  const [state, setLocalState] = useState<UrlState>(() => {
    const s = parseHash(window.location.hash)
    if (isLegacyHash(window.location.hash)) window.history.replaceState(window.history.state, '', serializeHash(s))
    return s
  })
  // The latest state, so consecutive setState calls in one event merge correctly
  // and the history push stays outside the React state updater.
  const stateRef = useRef(state)

  // Back, Forward, and a hash edited by hand.
  useEffect(() => {
    const onChange = () => {
      const hash = window.location.hash
      const parsed = parseHash(hash)
      if (isLegacyHash(hash)) window.history.replaceState(window.history.state, '', serializeHash(parsed))
      stateRef.current = parsed
      setLocalState(parsed)
    }
    window.addEventListener('popstate', onChange)
    window.addEventListener('hashchange', onChange)
    return () => {
      window.removeEventListener('popstate', onChange)
      window.removeEventListener('hashchange', onChange)
    }
  }, [])

  const setState = useCallback<SetUrlState>((next, options) => {
    const merged: UrlState = { ...stateRef.current, ...next }
    stateRef.current = merged
    const hash = serializeHash(merged)
    if (window.location.hash !== hash && !(hash === '#/' && window.location.hash === '')) {
      if (options?.replace) window.history.replaceState(window.history.state, '', hash)
      else window.history.pushState(null, '', hash)
    }
    setLocalState(merged)
  }, [])

  return [state, setState]
}
