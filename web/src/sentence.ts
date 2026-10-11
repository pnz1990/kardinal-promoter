// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// sentence.ts — the one way the UI puts a display label (P2, displayStates.gen.ts) inside a
// sentence, the rule pkg/reconciler/pipeline Summarize uses for status.summary: a label
// lowercases its first letter mid-sentence ("1.5.0 live in test"), unless it starts with an
// acronym ("PR …").
import { displayState, type DisplayKind } from './displayStates.gen'

/** A label as it reads mid-sentence: "Live" → "live", "Waiting for PR merge" → "waiting for PR merge", "PR open" stays. */
export function lowerFirst(label: string): string {
  return label.length > 1 && label[1] === label[1].toLowerCase() ? label[0].toLowerCase() + label.slice(1) : label
}

/** How a state reads mid-sentence: its display label, lowerFirst. */
export function stateWords(kind: DisplayKind, value: string): string {
  return lowerFirst(displayState(kind, value).label)
}
