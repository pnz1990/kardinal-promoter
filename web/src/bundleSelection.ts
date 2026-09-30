// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// bundleSelection.ts — one ordering and one "which bundle is shown" rule for the
// whole UI. App (poll, pipeline select, render), BundleTimeline and
// ReleaseMetricsBar all use these, so the DAG, the timeline highlight and the
// header can never disagree about the bundle on screen.

import type { Bundle } from './types'

/**
 * Compare two bundles newest-first.
 * Bundles with a createdAt sort before bundles without one; ties (and bundles
 * without createdAt) fall back to the name, descending, because generated
 * bundle names end in a timestamp. Equal names compare as 0.
 */
export function compareBundlesNewestFirst(a: Bundle, b: Bundle): number {
  const ta = a.createdAt ?? ''
  const tb = b.createdAt ?? ''
  if (ta !== tb) {
    if (!ta) return 1
    if (!tb) return -1
    return ta > tb ? -1 : 1
  }
  return b.name.localeCompare(a.name)
}

/** Return a new array of bundles sorted newest-first. */
export function sortBundlesNewestFirst(bundles: Bundle[]): Bundle[] {
  return [...bundles].sort(compareBundlesNewestFirst)
}

/**
 * The bundle shown when the user has not picked one:
 *  1. the pipeline's activeBundleName (chosen by the API), when it is in the list;
 *  2. otherwise the newest bundle that is not Superseded, whatever its phase;
 *  3. otherwise the newest bundle.
 * Steps 2 and 3 are the API's activeBundleName rule (handlePipelines in
 * cmd/kardinal-controller/ui_api.go), so both pick the same bundle. The phase
 * does not rank bundles: a newer Failed bundle is shown over an older Verified
 * or Promoting one, so a failure is never hidden (E2E-R15).
 */
export function pickDefaultBundle(bundles: Bundle[], activeBundleName?: string): Bundle | undefined {
  if (bundles.length === 0) return undefined
  if (activeBundleName) {
    const active = bundles.find(b => b.name === activeBundleName)
    if (active) return active
  }
  const sorted = sortBundlesNewestFirst(bundles)
  return sorted.find(b => b.phase !== 'Superseded') ?? sorted[0]
}

/**
 * The bundle shown on screen: the user's selection while it still exists,
 * otherwise the default.
 */
export function resolveShownBundle(
  bundles: Bundle[],
  selectedName: string | undefined,
  activeBundleName?: string,
): Bundle | undefined {
  if (selectedName) {
    const selected = bundles.find(b => b.name === selectedName)
    if (selected) return selected
  }
  return pickDefaultBundle(bundles, activeBundleName)
}
