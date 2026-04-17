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
// Hash format: #pipeline=nginx-demo&node=prod-step
//
// Design constraints:
// - No React Router dependency (keeps the bundle small)
// - No server-side routing required (hash is client-only)
// - Consistent with the existing useState pattern in App.tsx

import { useCallback, useEffect, useState } from 'react'

/** Parsed URL hash state. Undefined means "not set". */
export interface UrlState {
  pipeline: string | undefined
  node: string | undefined
}

/** Parse the URL hash fragment into a UrlState. */
function parseHash(hash: string): UrlState {
  if (!hash || hash === '#') return { pipeline: undefined, node: undefined }
  const params = new URLSearchParams(hash.startsWith('#') ? hash.slice(1) : hash)
  return {
    pipeline: params.get('pipeline') ?? undefined,
    node: params.get('node') ?? undefined,
  }
}

/** Serialize a UrlState back to a hash fragment (with leading #). */
function serializeHash(state: UrlState): string {
  const params = new URLSearchParams()
  if (state.pipeline) params.set('pipeline', state.pipeline)
  if (state.node) params.set('node', state.node)
  const s = params.toString()
  return s ? `#${s}` : ''
}

/**
 * useUrlState synchronizes a UrlState with window.location.hash.
 *
 * Returns [state, setState] where:
 * - state reflects the current hash (updated on popstate events)
 * - setState pushes a new history entry and updates the hash
 */
export function useUrlState(): [UrlState, (next: Partial<UrlState>) => void] {
  const [state, setLocalState] = useState<UrlState>(() =>
    parseHash(window.location.hash)
  )

  // Listen for browser back/forward navigation
  useEffect(() => {
    const onPop = () => setLocalState(parseHash(window.location.hash))
    window.addEventListener('popstate', onPop)
    return () => window.removeEventListener('popstate', onPop)
  }, [])

  const setState = useCallback((next: Partial<UrlState>) => {
    setLocalState(prev => {
      const merged: UrlState = { ...prev, ...next }
      const hash = serializeHash(merged)
      // Use pushState so back/forward navigation works
      if (window.location.hash !== hash) {
        window.history.pushState(null, '', hash || window.location.pathname)
      }
      return merged
    })
  }, [])

  return [state, setState]
}
