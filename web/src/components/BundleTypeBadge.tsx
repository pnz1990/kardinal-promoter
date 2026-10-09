// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// components/BundleTypeBadge.tsx — what kind of change a Bundle carries:
// images, a config commit, both, or a Helm chart version. Image Bundles are
// the common case, so the compact form (timeline chips, the fleet rail) shows
// only the others; the full form (the Bundle card) shows every type.

import '../styles/BundleTypeBadge.css'

/** The label of each known Bundle type; an unknown type shows as itself. */
const LABELS: Record<string, string> = {
  image: 'image',
  config: 'config',
  mixed: 'image + config',
  chart: 'chart',
}

/** What a screen reader hears. */
const SPOKEN: Record<string, string> = {
  image: 'image Bundle',
  config: 'config Bundle',
  mixed: 'image and config Bundle',
  chart: 'Helm chart Bundle',
}

export function bundleTypeLabel(type: string | undefined): string {
  const t = (type || 'image').toLowerCase()
  return LABELS[t] ?? t
}

interface Props {
  type?: string
  /** Compact: nothing for image Bundles, a smaller tag otherwise. */
  compact?: boolean
}

export function BundleTypeBadge({ type, compact }: Props) {
  const t = (type || 'image').toLowerCase()
  if (compact && t === 'image') return null
  return (
    <span
      className={`bundle-type${compact ? ' bundle-type--compact' : ''}`}
      data-bundle-type={LABELS[t] ? t : 'other'}
      role="img"
      aria-label={SPOKEN[t] ?? `${t} Bundle`}
      title={SPOKEN[t] ?? `${t} Bundle`}
    >
      {bundleTypeLabel(t)}
    </span>
  )
}
