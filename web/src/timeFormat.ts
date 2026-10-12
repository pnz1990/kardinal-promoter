// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// timeFormat.ts — the one way the UI says how long something took or how long ago it was:
// "42s", "4m", "2h 15m", "1d 3h", "4d" (never "96h 0m").

/**
 * A duration in seconds, humanised: "42s", "4m", "2h 15m", "1d 3h", "4d". With precise, a
 * duration under an hour keeps its seconds ("4m 12s"), for a timer that is running.
 */
export function humanDuration(sec: number, { precise = false }: { precise?: boolean } = {}): string {
  const s = Math.max(0, Math.floor(sec))
  if (s < 60) return `${s}s`
  if (s < 3600) {
    const m = Math.floor(s / 60)
    return precise && s % 60 ? `${m}m ${s % 60}s` : `${m}m`
  }
  if (s < 86400) {
    const h = Math.floor(s / 3600)
    const m = Math.floor((s % 3600) / 60)
    return m ? `${h}h ${m}m` : `${h}h`
  }
  const d = Math.floor(s / 86400)
  const h = Math.floor((s % 86400) / 3600)
  return d < 3 && h ? `${d}d ${h}h` : `${d}d`
}

/** Seconds from an ISO timestamp to now; undefined for a missing, unparseable or future one. */
export function secondsSince(iso: string | undefined, now: number = Date.now()): number | undefined {
  if (!iso) return undefined
  const start = new Date(iso).getTime()
  if (isNaN(start)) return undefined
  const sec = Math.floor((now - start) / 1000)
  return sec < 0 ? undefined : sec
}

/**
 * Time since an ISO timestamp, for a running timer: "42s", "4m 12s", "1h 23m", "4d".
 * Returns '' for a missing, unparseable or future timestamp.
 */
export function formatElapsedSince(iso: string | undefined, now: number = Date.now()): string {
  const sec = secondsSince(iso, now)
  return sec === undefined ? '' : humanDuration(sec, { precise: true })
}

/** How long ago: "just now", "5m ago", "2h 15m ago", "4d ago"; '' when unknown. */
export function ago(iso: string | undefined, now: number = Date.now()): string {
  const sec = secondsSince(iso, now)
  if (sec === undefined) return ''
  return sec < 60 ? 'just now' : `${humanDuration(sec)} ago`
}
