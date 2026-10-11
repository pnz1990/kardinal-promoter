// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// stationMap/display.ts — how a state reads on the station map: P2's
// display-state table (pkg/display, generated into displayStates.gen.ts), with
// the two conveniences the map uses: a missing state reads like an empty one,
// and the rollback hold is named for what it is.

import { displayState as of, held, heldByGate, words } from '../displayStates.gen'
import type { DisplayKind, DisplayState, Tone } from '../displayStates.gen'

export { heldByGate, words }
export type { DisplayKind, DisplayState, Tone }

/** How value of kind is shown; an unknown value gets its words and the unknown tone. */
export function displayState(kind: DisplayKind, value: string | undefined): DisplayState {
  return of(kind, value ?? '')
}

/** An environment held by a rollback (kardinal rollback --hold). */
export function heldByRollback(stepState: string): DisplayState {
  return held(stepState)
}
