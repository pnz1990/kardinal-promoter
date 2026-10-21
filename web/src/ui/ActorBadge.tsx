// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// ActorBadge.tsx — who made a change (docs/actors.md), the same everywhere: a
// glyph for the kind (a person, an agent, a bot, the controller) and the name,
// an agent with "for <sponsor>", a person written while kardinal's agent index
// was not Ready with "unverified" (#2451: it never counts as a person's
// decision). Never color alone: the glyph and the words say the kind, the
// title and screen readers get the full sentence, and what a client asserted
// about itself is labelled as asserted.
import type { ActorKind, ActorView } from '../actor'
import { Icon, type IconName } from './Icon'
import { cx } from './cx'

const GLYPH: Record<ActorKind, IconName> = {
  human: 'person', agent: 'agent', bot: 'bot', controller: 'controller', unverified: 'person', unknown: 'person',
}

/** The kind as a screen reader says it before the name. */
const KIND_WORD: Record<ActorKind, string> = {
  human: '', agent: 'agent', bot: 'bot', controller: '', unverified: '', unknown: '',
}

export interface ActorBadgeProps {
  actor: ActorView
  /** Only the glyph and name, no sponsor (dense rows, tooltips). */
  compact?: boolean
  className?: string
}

export function ActorBadge({ actor, compact, className }: ActorBadgeProps) {
  // asserted already says it is asserted ("model x (asserted)").
  const title = actor.asserted ? `${actor.text} · ${actor.asserted}` : actor.text
  const kind = KIND_WORD[actor.kind]
  return (
    <span className={cx('k-actor', `k-actor--${actor.kind}`, className)} title={title} data-actor-kind={actor.kind}>
      <Icon name={GLYPH[actor.kind]} className="k-actor__glyph" />
      {kind && <span className="sr-only">{kind} </span>}
      <span className="k-actor__name">{actor.name}</span>
      {actor.kind === 'unverified' && <>{' '}<span className="k-actor__qualifier">unverified</span></>}
      {!compact && actor.sponsor && <>{' '}<span className="k-actor__sponsor">for {actor.sponsor}</span></>}
    </span>
  )
}
