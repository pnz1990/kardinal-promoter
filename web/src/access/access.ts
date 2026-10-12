// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// access/access.ts — what the person at the UI may do (U7). The UI asks the
// UI API (POST /access, a SubjectAccessReview per check in TokenReview mode)
// about the actions a screen offers, and disables the ones it may not take,
// naming the role that grants them. Answers are cached for 30 s, as the
// controller caches its reviews; the checks one render asks for go out as one
// request per namespace. The controller still checks every action: this only
// decides what to offer.
import { useEffect, useSyncExternalStore } from 'react'
import { api } from '../api/client'
import type { AccessCheck, AccessResponse } from '../types'

/** How long an answer is reused. */
export const ACCESS_TTL_MS = 30_000
/** The most checks one request carries (the UI API's limit). */
const MAX_CHECKS = 32

/** What the UI knows about one action. */
export interface Permission {
  /** False only when the controller said no. Unknown and failed answers allow: the controller decides. */
  allowed: boolean
  /** The answer has arrived. */
  known: boolean
  /** The chart roles that grant it ("approver or admin"). */
  grantedBy?: string
  /** Why it is not allowed, when that is not a missing role (from the controller). */
  reason?: string
}

/** Who the caller is: a user when TokenReview identified them, null when the UI API does not identify callers. */
export type Identity = { username: string; groups: string[] } | null

interface Entry { at: number; allowed: boolean; grantedBy?: string; reason?: string }

const answers = new Map<string, Entry>()
const queued = new Map<string, Map<string, AccessCheck>>()
const inflight = new Set<string>()
let identity: Identity | undefined
let version = 0
const listeners = new Set<() => void>()
let flushScheduled = false
// Bumped when the token changes: an answer that arrives for an older token is dropped.
let generation = 0
let now = () => Date.now()

function emit() {
  version++
  listeners.forEach(l => l())
}

/** The cache key of a check in a namespace. */
export function accessKey(namespace: string, c: AccessCheck): string {
  return [namespace, c.verb, c.resource, c.subresource ?? '', c.name ?? ''].join(' ')
}

function fresh(key: string): boolean {
  const e = answers.get(key)
  return !!e && now() - e.at < ACCESS_TTL_MS
}

function request(namespace: string, checks: AccessCheck[]) {
  for (const c of checks) {
    const key = accessKey(namespace, c)
    if (fresh(key) || inflight.has(key)) continue
    let q = queued.get(namespace)
    if (!q) queued.set(namespace, q = new Map())
    q.set(key, c)
  }
  if (queued.size === 0 || flushScheduled) return
  flushScheduled = true
  queueMicrotask(flush)
}

function flush() {
  flushScheduled = false
  const batches = [...queued.entries()]
  queued.clear()
  const gen = generation
  for (const [namespace, q] of batches) {
    const entries = [...q.entries()]
    for (let i = 0; i < entries.length; i += MAX_CHECKS) {
      const chunk = entries.slice(i, i + MAX_CHECKS)
      chunk.forEach(([k]) => inflight.add(k))
      void Promise.resolve()
        .then(() => api.checkAccess(namespace, chunk.map(([, c]) => c)))
        .then((res: AccessResponse) => {
          if (gen !== generation) return
          const at = now()
          chunk.forEach(([k], j) => {
            const r = res.results?.[j]
            answers.set(k, { at, allowed: r ? r.allowed : true, grantedBy: r?.grantedBy, reason: r?.reason })
          })
          identity = res.user ? { username: res.user.username, groups: res.user.groups ?? [] } : null
        })
        .catch(() => {
          if (gen !== generation) return
          // An older controller (no /access) or a failed review: offer the
          // action and let the controller decide, and ask again after the TTL.
          const at = now()
          chunk.forEach(([k]) => answers.set(k, { at, allowed: true }))
        })
        .finally(() => {
          chunk.forEach(([k]) => inflight.delete(k))
          emit()
        })
    }
  }
}

function subscribe(l: () => void) {
  listeners.add(l)
  return () => { listeners.delete(l) }
}

/** The permission for one check, from the cache. */
export function permission(namespace: string, c: AccessCheck): Permission {
  const e = answers.get(accessKey(namespace, c))
  if (!e) return { allowed: true, known: false }
  return { allowed: e.allowed, known: true, grantedBy: e.grantedBy, reason: e.reason }
}

/**
 * useAccessIn asks about each [namespace, check] and re-renders when answers
 * arrive. It returns a lookup for any check in a namespace and who the
 * caller is (undefined until the first answer).
 */
export function useAccessIn(requests: Array<[string, AccessCheck]>): {
  can: (namespace: string, c: AccessCheck) => Permission
  identity: Identity | undefined
} {
  useSyncExternalStore(subscribe, () => version, () => version)
  const key = requests.map(([ns, c]) => accessKey(ns, c)).join('|')
  useEffect(() => {
    const byNs = new Map<string, AccessCheck[]>()
    for (const [ns, c] of requests) byNs.set(ns, [...(byNs.get(ns) ?? []), c])
    byNs.forEach((checks, ns) => request(ns, checks))
  }, [key]) // requests is described by key
  return { can: permission, identity }
}

/** useAccess is useAccessIn for checks in one namespace (none while it is undefined). */
export function useAccess(namespace: string | undefined, checks: AccessCheck[]): {
  can: (c: AccessCheck) => Permission
  identity: Identity | undefined
} {
  const { can, identity } = useAccessIn(namespace ? checks.map(c => [namespace, c] as [string, AccessCheck]) : [])
  return {
    can: c => namespace ? can(namespace, c) : { allowed: true, known: false },
    identity,
  }
}

/** resetAccess forgets every answer and who the caller is: the token changed, so they are
 *  another user's. Requests in flight are dropped when they answer. */
export function resetAccess() {
  generation++
  answers.clear()
  queued.clear()
  inflight.clear()
  identity = undefined
  flushScheduled = false
  emit()
}
// The token changed (api/client.ts sends this event; its name is repeated here so a test
// that mocks the client keeps it).
if (typeof window !== 'undefined') window.addEventListener('kardinal:token-change', resetAccess)

/** For tests: forget every answer and set the clock. */
export function resetAccessForTests(clock?: () => number) {
  resetAccess()
  now = clock ?? (() => Date.now())
}
