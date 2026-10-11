// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// stage/overrideLimits.ts — the override durations the UI offers for a gate,
// up to the cap the controller sends with it (maxOverrideMinutes, from
// --gate-override-max-minutes). Without one, only the controller's default
// duration (1 hour) is offered.
import type { PolicyGate } from '../types'

const CHOICES = [15, 60, 240, 1440]

/** Durations, in minutes, the override dialog offers for gate. */
export function overrideChoices(gate?: PolicyGate): number[] {
  const cap = gate?.maxOverrideMinutes ?? 60
  const out = CHOICES.filter(m => m <= cap)
  if (!out.includes(cap) && cap > 0) out.push(cap)
  return out
}
