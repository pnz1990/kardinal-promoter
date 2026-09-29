// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// timeFormat.ts — shared elapsed-time formatting for running promotions.

/**
 * Time since an ISO timestamp as "42s", "4m 12s" or "1h 23m".
 * Returns '' for a missing, unparseable or future timestamp.
 */
export function formatElapsedSince(iso: string | undefined, now: number = Date.now()): string {
  if (!iso) return ''
  const start = new Date(iso).getTime()
  if (isNaN(start)) return ''
  const sec = Math.floor((now - start) / 1000)
  if (sec < 0) return ''
  if (sec < 60) return `${sec}s`
  if (sec < 3600) return `${Math.floor(sec / 60)}m ${sec % 60}s`
  return `${Math.floor(sec / 3600)}h ${Math.floor((sec % 3600) / 60)}m`
}
