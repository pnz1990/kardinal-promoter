// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// approvalAge.ts — "3h ago" for a decision or a rejection time.
import { ago } from '../timeFormat'

/** How long ago iso was, at now: "just now", "5m ago", "3h ago", "2d ago"; "" when unparseable. */
export function formatRelativeAge(iso: string, now: number = Date.now()): string {
  return ago(iso, now)
}
