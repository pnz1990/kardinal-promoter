// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// useUrlState.ts — Hash-based URL state for pipeline/node selection (#740).
//
// Uses the URL hash fragment to persist selection state so that:
// - Page reload restores the selected pipeline and node
// - Back/forward navigation works as expected
// - Users can share deep links
//
// Hash format: #pipeline=nginx-demo&ns=default&node=prod-step
// (ns tells apart same-named pipelines in different namespaces).
//
// Design constraints:
// - No React Router dependency (keeps the bundle small)
// - No server-side routing required (hash is client-only)
// - Consistent with the existing useState pattern in App.tsx

import { useCallback, useEffect, useRef, useState } from 'react'

/** Parsed URL hash state. Undefined means "not set". */
export interface UrlState {
  pipeline: string | undefined
  /** Namespace of the selected pipeline. */
  ns: string | undefined
  node: string | undefined
  /** Name of the bundle selected for diff comparison (opens BundleDiffPanel). */
  bundle: string | undefined
}

/** Parse the URL hash fragment into a UrlState. */
function parseHash(hash: string): UrlState {
  if (!hash || hash === '#') return { pipeline: undefined, ns: undefined, node: undefined, bundle: undefined }
  const params = new URLSearchParams(hash.startsWith('#') ? hash.slice(1) : hash)
  return {
    pipeline: params.get('pipeline') ?? undefined,
    ns: params.get('ns') ?? undefined,
    node: params.get('node') ?? undefined,
    bundle: params.get('bundle') ?? undefined,
  }
}

/** Serialize a UrlState back to a hash fragment (with leading #). */
function serializeHash(state: UrlState): string {
  const params = new URLSearchParams()
  if (state.pipeline) params.set('pipeline', state.pipeline)
  if (state.ns) params.set('ns', state.ns)
  if (state.node) params.set('node', state.node)
  if (state.bundle) params.set('bundle', state.bundle)
  const s = params.toString()
  return s ? `#${s}` : ''
}

/**
 * useUrlState synchronizes a UrlState with window.location.hash.
 *
 * Returns [state, setState] where:
 * - state reflects the current hash (updated on popstate events)
 * - setState merges the given fields and pushes at most one history entry,
 *   so a change that touches several fields is a single Back press.
 */
export function useUrlState(): [UrlState, (next: Partial<UrlState>) => void] {
  const [state, setLocalState] = useState<UrlState>(() =>
    parseHash(window.location.hash)
  )
  // The latest state, so consecutive setState calls in one event merge correctly
  // and the history push stays outside the React state updater.
  const stateRef = useRef(state)

  // Listen for browser back/forward navigation
  useEffect(() => {
    const onPop = () => {
      const parsed = parseHash(window.location.hash)
      stateRef.current = parsed
      setLocalState(parsed)
    }
    window.addEventListener('popstate', onPop)
    return () => window.removeEventListener('popstate', onPop)
  }, [])

  const setState = useCallback((next: Partial<UrlState>) => {
    const merged: UrlState = { ...stateRef.current, ...next }
    stateRef.current = merged
    const hash = serializeHash(merged)
    // Use pushState so back/forward navigation works
    if (window.location.hash !== hash) {
      window.history.pushState(null, '', hash || window.location.pathname)
    }
    setLocalState(merged)
  }, [])

  return [state, setState]
}
