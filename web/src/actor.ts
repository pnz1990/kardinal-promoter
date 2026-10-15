// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// actor.ts — who made a change, as the UI shows it (docs/actors.md): one shape
// for a Bundle's verified actor, the UI API's short ActorRef, and a bare user
// name (an approval's or an override's, which carry no actor kind yet), so
// ActorBadge renders all of them the same way.

import type { Actor, ActorRef, Bundle } from './types'

export type ActorKind = 'human' | 'agent' | 'bot' | 'controller' | 'unknown'

/** What ActorBadge shows. */
export interface ActorView {
  kind: ActorKind
  /** The short name: the user, the agent, the bot's account, "kardinal". */
  name: string
  /** The agent's sponsor: "for alice". */
  sponsor?: string
  /** The full sentence, for the title and screen readers. */
  text: string
  /** What the client said about itself, labelled as asserted. */
  asserted?: string
}

/** The Kubernetes ServiceAccount user prefix: such a user is an automation. */
const SA_PREFIX = 'system:serviceaccount:'

/** A ServiceAccount user as namespace/name; anything else as it is. */
function shortUser(user: string): string {
  if (!user.startsWith(SA_PREFIX)) return user
  return user.slice(SA_PREFIX.length).replace(':', '/')
}

/** The view of a Bundle's verified actor stamp, or undefined when unknown. */
export function actorOfBundle(b?: Bundle): ActorView | undefined {
  const a = b?.actor
  if (!a || a.kind === 'unknown' || !b.actorText || b.actorText === 'unknown') return undefined
  return fromActor(a, b.actorText, b.actorAsserted)
}

function fromActor(a: Actor | ActorRef, text: string, asserted?: string): ActorView {
  switch (a.kind) {
    case 'agent': return { kind: 'agent', name: a.agent ?? text, sponsor: a.sponsor, text, asserted }
    case 'controller': return { kind: 'controller', name: 'kardinal', text, asserted }
    default: {
      const principal = 'principal' in a ? a.principal : ''
      return { kind: a.kind, name: principal ? shortUser(principal) : text, text, asserted }
    }
  }
}

/** The view of the UI API's short actor (fleet board, map, filter). */
export function actorOfRef(r?: ActorRef): ActorView | undefined {
  return r ? fromActor(r, r.text) : undefined
}

/** The view of a bare user name (an approval's, an override's): a person, or
 *  an automation when it is a ServiceAccount. */
export function actorOfUser(user?: string): ActorView | undefined {
  if (!user) return undefined
  return user.startsWith(SA_PREFIX)
    ? { kind: 'bot', name: shortUser(user), text: `bot ${user}` }
    : { kind: 'human', name: user, text: user }
}

/** Whether an agent shipped it (the Agents filter, the map's mark). */
export function isAgent(r?: { kind: string }): boolean {
  return r?.kind === 'agent'
}
