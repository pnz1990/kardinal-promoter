// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// approvalAge.ts — "3h ago" for a decision or a rejection time.

/** How long ago iso was, at now: "just now", "5m ago", "3h ago", "2d ago"; "" when unparseable. */
export function formatRelativeAge(iso: string, now: number = Date.now()): string {
  const t = Date.parse(iso)
  if (isNaN(t)) return ''
  const s = Math.max(0, Math.floor((now - t) / 1000))
  if (s < 60) return 'just now'
  if (s < 3600) return `${Math.floor(s / 60)}m ago`
  if (s < 86400) return `${Math.floor(s / 3600)}h ago`
  return `${Math.floor(s / 86400)}d ago`
}
