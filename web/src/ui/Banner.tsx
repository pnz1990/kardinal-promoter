// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Banner.tsx — a page-wide notice: something that holds or endangers every
// view (a freeze, a rejected change that is live, a plain-HTTP connection).
// One pattern for all of them: an icon and the words (never color alone), the
// status tokens of its tone, and its actions at the end. A banner is in the
// flow; it is shown with the first content it belongs to, or it shifts the
// page (journeys 024 and 027).
import type { HTMLAttributes, ReactNode } from 'react'
import { Icon, type IconName } from './Icon'
import { cx } from './cx'

/** The tones a banner takes: blocked (it holds or warns), failed (it is broken or stops everything). */
export type BannerTone = 'blocked' | 'failed'

export interface BannerProps extends HTMLAttributes<HTMLDivElement> {
  tone: BannerTone
  /** Default: warning. */
  icon?: IconName
  /** Buttons or commands at the end of the banner. */
  actions?: ReactNode
  children: ReactNode
}

export function Banner({ tone, icon = 'warning', actions, children, className, role = 'alert', ...rest }: BannerProps) {
  return (
    <div role={role} className={cx('k-banner', `k-banner--${tone}`, className)} {...rest}>
      <Icon name={icon} className="k-banner__icon" />
      <div className="k-banner__body">{children}</div>
      {actions && <div className="k-banner__actions">{actions}</div>}
    </div>
  )
}
