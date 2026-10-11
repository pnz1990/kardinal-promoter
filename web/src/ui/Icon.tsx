// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Icon.tsx — the UI's icon set: inline SVG in currentColor, drawn like the logo (straight
// strokes, 45° bends, round dots). Decorative (aria-hidden) unless it has a label.
import type { SVGProps } from 'react'

export type IconName =
  | 'close' | 'chevron' | 'check' | 'cross' | 'clock' | 'gate' | 'pin' | 'pause' | 'play'
  | 'rollback' | 'promote' | 'copy' | 'external' | 'search' | 'person' | 'pr' | 'warning'

/** 16×16 drawings; strokes unless noted. */
const PATHS: Record<IconName, string[]> = {
  close: ['M4 4l8 8', 'M12 4l-8 8'],
  chevron: ['M6 4l4 4-4 4'],
  check: ['M3 8.5l3 3 7-7'],
  cross: ['M4.5 4.5l7 7', 'M11.5 4.5l-7 7'],
  clock: ['M8 2.5a5.5 5.5 0 1 0 0 11 5.5 5.5 0 1 0 0-11z', 'M8 5v3.2l2.2 1.3'],
  gate: ['M3 2.5v11', 'M13 2.5v11', 'M3 8h10'],
  pin: ['M8 14v-4', 'M5 2.5h6', 'M6 2.5v3L4 8h8l-2-2.5v-3'],
  pause: ['M5.5 3.5v9', 'M10.5 3.5v9'],
  play: ['M5 3l7 5-7 5z'],
  rollback: ['M4 5h6.5a3 3 0 0 1 0 6H6', 'M6.5 2.5L4 5l2.5 2.5'],
  promote: ['M3 8h9', 'M9 4.5L12.5 8 9 11.5'],
  copy: ['M5.5 5.5h7v7h-7z', 'M3.5 10.5v-7h7'],
  external: ['M9 3h4v4', 'M13 3L7.5 8.5', 'M11 9.5V13H3V5h3.5'],
  search: ['M7 2.5a4.5 4.5 0 1 0 0 9 4.5 4.5 0 1 0 0-9z', 'M10.3 10.3L13.5 13.5'],
  person: ['M8 2.5a2.5 2.5 0 1 0 0 5 2.5 2.5 0 1 0 0-5z', 'M3 13.5c.6-2.5 2.6-4 5-4s4.4 1.5 5 4'],
  pr: ['M4.5 2.5v11', 'M11.5 7v6.5', 'M11.5 7a2.5 2.5 0 0 0-2.5-2.5H6.5', 'M8 3l-1.5 1.5L8 6'],
  warning: ['M8 2.5l6 11H2z', 'M8 6.5v3', 'M8 11.5v.01'],
}

const FILLED: ReadonlySet<IconName> = new Set(['play'])

export interface IconProps extends Omit<SVGProps<SVGSVGElement>, 'name'> {
  name: IconName
  size?: 16 | 20
  /** The accessible name; without one the icon is decorative. */
  label?: string
}

export function Icon({ name, size = 16, label, className, ...rest }: IconProps) {
  const filled = FILLED.has(name)
  return (
    <svg
      viewBox="0 0 16 16"
      width={size}
      height={size}
      className={['k-icon', className].filter(Boolean).join(' ')}
      fill={filled ? 'currentColor' : 'none'}
      stroke="currentColor"
      {...(label ? { role: 'img', 'aria-label': label } : { 'aria-hidden': true, focusable: false })}
      {...rest}
    >
      {PATHS[name].map((d) => <path key={d} d={d} />)}
    </svg>
  )
}

export const ICON_NAMES = Object.keys(PATHS) as IconName[]
