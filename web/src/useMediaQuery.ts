// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// useMediaQuery.ts — whether a media query matches, following changes. The layout's
// breakpoints are tokens (tokens.ts), and CSS cannot read custom properties in @media,
// so the app sets data attributes from here and the stylesheets key off those.
import { useEffect, useState } from 'react'
import { breakpoint } from './tokens'

export function useMediaQuery(query: string): boolean {
  const get = () => typeof window !== 'undefined' && !!window.matchMedia && window.matchMedia(query).matches
  const [matches, setMatches] = useState(get)
  useEffect(() => {
    if (!window.matchMedia) return
    const mql = window.matchMedia(query)
    const onChange = () => setMatches(mql.matches)
    onChange()
    mql.addEventListener('change', onChange)
    return () => mql.removeEventListener('change', onChange)
  }, [query])
  return matches
}

/** Narrower than the md breakpoint: the pipeline list becomes a drawer over the page. */
export const NARROW_QUERY = `(max-width: ${breakpoint.md - 0.02}px)`
