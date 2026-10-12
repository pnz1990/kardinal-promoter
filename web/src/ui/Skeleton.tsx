// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Skeleton.tsx — what a lazily loaded view shows while its code loads: quiet bars in
// the view's shape, named for screen readers ("Loading the table…"). It shimmers only
// when motion is allowed.
import { cx } from './cx'

export interface SkeletonProps {
  /** What is loading, e.g. "Loading the table…". */
  label: string
  /** How many bars. */
  rows?: number
  className?: string
}

export function Skeleton({ label, rows = 6, className }: SkeletonProps) {
  return (
    <div className={cx('k-skeleton', className)} role="status" aria-label={label}>
      {Array.from({ length: rows }, (_, i) => <span key={i} className="k-skeleton__bar" aria-hidden="true" />)}
    </div>
  )
}
