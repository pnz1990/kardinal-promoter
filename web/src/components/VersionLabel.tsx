// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// components/VersionLabel.tsx — a version as people know it: what it ships (status.version,
// its image tag, its config commit; stage/stageModel bundleVersion), with the Bundle name
// after it as quiet monospace text when it says something more. The map, the stage drawer
// and the fleet board name versions the same way.
import type { Bundle } from '../types'
import { bundleVersion } from '../stage/stageModel'
import '../styles/VersionLabel.css'

interface Props {
  bundle: Bundle
  /** Leave the Bundle name out (it goes in a title instead), where space is short. */
  compact?: boolean
  className?: string
}

export function VersionLabel({ bundle, compact, className }: Props) {
  const tag = bundleVersion(bundle)
  const showName = !compact && tag !== bundle.name
  return (
    <span className={['version-label', className].filter(Boolean).join(' ')} title={compact && tag !== bundle.name ? bundle.name : undefined}>
      <span className="version-label__tag">{tag}</span>
      {showName && <span className="version-label__name">{bundle.name}</span>}
    </span>
  )
}
