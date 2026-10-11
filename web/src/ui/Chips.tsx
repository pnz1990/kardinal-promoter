// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Chips.tsx — StatusChip (a state, by its display-state label and tone), Badge (a count) and
// Tag (a Bundle type). Hue is never the only cue: a chip carries its label, and a glyph.
import { forwardRef, type HTMLAttributes } from 'react'
import type { Tone } from './tone'
import { cx } from './cx'

export interface StatusChipProps extends HTMLAttributes<HTMLSpanElement> {
  tone: Tone
  /** The display-state label (displayState(kind, value).label). */
  label: string
  /** The display-state glyph, shown before the label. */
  glyph?: string
  compact?: boolean
}

export const StatusChip = forwardRef<HTMLSpanElement, StatusChipProps>(function StatusChip(
  { tone, label, glyph, compact, className, ...rest }, ref,
) {
  return (
    <span ref={ref} className={cx('k-chip', `k-chip--${tone}`, compact && 'k-chip--compact', className)} {...rest}>
      {glyph && <span className="k-chip__glyph" aria-hidden="true">{glyph}</span>}
      {label}
    </span>
  )
})

export interface BadgeProps extends HTMLAttributes<HTMLSpanElement> {
  count: number
  /** Above max the badge reads "99+". */
  max?: number
  tone?: 'neutral' | Tone
}

export const Badge = forwardRef<HTMLSpanElement, BadgeProps>(function Badge(
  { count, max = 99, tone = 'neutral', className, ...rest }, ref,
) {
  return (
    <span ref={ref} className={cx('k-badge', `k-badge--${tone}`, className)} {...rest}>
      {count > max ? `${max}+` : count}
    </span>
  )
})

export type TagKind = 'image' | 'config' | 'chart' | 'mixed'

const TAG: Record<TagKind, { glyph: string; text: string; name: string }> = {
  image: { glyph: '■', text: 'IMG', name: 'image' },
  config: { glyph: '◆', text: 'CFG', name: 'config' },
  chart: { glyph: '▲', text: 'CHART', name: 'chart' },
  mixed: { glyph: '■◆', text: 'MIX', name: 'image and config' },
}

export interface TagProps extends HTMLAttributes<HTMLSpanElement> {
  kind: TagKind
}

/** A Bundle type: a monochrome tag, named for screen readers as "<type> Bundle". */
export const Tag = forwardRef<HTMLSpanElement, TagProps>(function Tag({ kind, className, ...rest }, ref) {
  const t = TAG[kind]
  return (
    <span ref={ref} role="img" aria-label={`${t.name} Bundle`} className={cx('k-tag', className)} {...rest}>
      <span aria-hidden="true">{t.glyph} {t.text}</span>
    </span>
  )
})
