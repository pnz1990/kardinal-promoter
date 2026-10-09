// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// api/client.ts — Typed fetch wrappers for the kardinal UI backend API.

import type { Pipeline, Bundle, GraphResponse, PromotionStep, PolicyGate } from '../types'
import type { StepEvent } from '../components/EventsPanel'

const BASE = '/api/v1/ui'

// ─── Authentication ──────────────────────────────────────────────────────────
//
// With --ui-auth-token or --ui-tokenreview-auth the controller answers 401 to
// any /api/v1/ui/* request without a valid "Authorization: Bearer" header.
// The token lives in sessionStorage (this tab only, cleared when it closes).
// On a 401 the client drops the stored token, asks the registered prompt
// (TokenPrompt) for a new one and retries. Concurrent requests share one
// prompt, and requests started while the prompt is open wait for it.

/** sessionStorage key for the UI bearer token. */
export const TOKEN_STORAGE_KEY = 'kardinal-ui-token'

/** Why a token is requested: none stored yet, or the stored one got a 401. */
export type TokenPromptReason = 'required' | 'rejected'

/** Resolves with the token the user entered. Rejecting gives up (the 401 is returned). */
export type TokenPromptHandler = (reason: TokenPromptReason) => Promise<string>

let promptHandler: TokenPromptHandler | null = null
let pendingPrompt: Promise<string> | null = null

/** Registers the token prompt. Returns a function that unregisters it. */
export function setTokenPrompt(handler: TokenPromptHandler): () => void {
  promptHandler = handler
  return () => {
    if (promptHandler === handler) promptHandler = null
  }
}

function readToken(): string | null {
  try {
    return sessionStorage.getItem(TOKEN_STORAGE_KEY)
  } catch {
    return null // storage disabled: behave as if no token is stored
  }
}

function writeToken(token: string | null): void {
  try {
    if (token) sessionStorage.setItem(TOKEN_STORAGE_KEY, token)
    else sessionStorage.removeItem(TOKEN_STORAGE_KEY)
  } catch {
    // storage disabled: the token is not kept and the prompt reappears
  }
}

function askForToken(reason: TokenPromptReason): Promise<string> | null {
  if (!promptHandler) return null
  if (!pendingPrompt) {
    const handler = promptHandler
    pendingPrompt = handler(reason)
      .then(token => {
        writeToken(token)
        return token
      })
      .finally(() => {
        pendingPrompt = null
      })
  }
  return pendingPrompt
}

// ─── Read timeout ────────────────────────────────────────────────────────────

/** A read whose network time exceeds this fails, so one hung request cannot
 *  stop the 5 s polling (the poll waits for the previous one to finish). */
export const REQUEST_TIMEOUT_MS = 10_000

/** Aborts a request after REQUEST_TIMEOUT_MS of network time. The clock runs
 *  only while a fetch or body read is in progress: time the user spends in the
 *  token prompt does not count, so a read waiting for sign-in is not failed. */
class NetworkDeadline {
  private readonly ctrl = new AbortController()
  private timer: ReturnType<typeof setTimeout> | undefined
  readonly signal = this.ctrl.signal
  start(): void {
    this.stop()
    this.timer = setTimeout(() => this.ctrl.abort(), REQUEST_TIMEOUT_MS)
  }
  stop(): void {
    clearTimeout(this.timer)
    this.timer = undefined
  }
}

/** fetch with the stored bearer token; on 401 asks for a token and retries.
 *  With a deadline, the clock runs from each fetch until its 401 or its
 *  answer, so waiting for the token prompt is never counted. */
async function request(path: string, init: RequestInit = {}, deadline?: NetworkDeadline): Promise<Response> {
  for (;;) {
    if (pendingPrompt) {
      try {
        await pendingPrompt
      } catch {
        // prompt abandoned: send the request anyway and report its result
      }
    }
    const token = readToken()
    const headers: Record<string, string> = { ...(init.headers as Record<string, string> | undefined) }
    if (token) headers.Authorization = `Bearer ${token}`
    deadline?.start()
    const resp = await fetch(`${BASE}${path}`, deadline ? { ...init, headers, signal: deadline.signal } : { ...init, headers })
    if (resp.status !== 401) return resp
    deadline?.stop()

    const current = readToken()
    if (current && current !== token) continue // another request already got a new token
    if (current) writeToken(null)
    const next = askForToken(token ? 'rejected' : 'required')
    if (!next) return resp
    try {
      await next
    } catch {
      return resp
    }
  }
}

async function get<T>(path: string): Promise<T> {
  const deadline = new NetworkDeadline()
  try {
    const resp = await request(path, {}, deadline)
    if (!resp.ok) {
      throw new Error(`API error ${resp.status}: ${resp.statusText}`)
    }
    // The body is read on the same clock as the fetch that returned it.
    return await (resp.json() as Promise<T>)
  } catch (e) {
    if (deadline.signal.aborted) {
      throw new Error(`GET ${BASE}${path} got no answer within ${REQUEST_TIMEOUT_MS / 1000} s. The UI tries again on the next refresh.`)
    }
    throw e
  } finally {
    deadline.stop()
  }
}

async function post<T>(path: string, body: unknown): Promise<T> {
  const resp = await request(path, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
  if (!resp.ok) {
    const text = await resp.text()
    throw new Error(`API error ${resp.status}: ${text || resp.statusText}`)
  }
  return resp.json() as Promise<T>
}

/** Encode one URL path segment, so a name can never add path segments or a query. */
const seg = encodeURIComponent

/** The ?namespace= query of a namespaced read, or nothing without a namespace. */
const nsQuery = (namespace?: string) => (namespace ? `?namespace=${seg(namespace)}` : '')

export const api = {
  listPipelines: () => get<Pipeline[]>('/pipelines'),
  /** Bundles of a pipeline, newest first. With namespace, only that namespace's
   *  bundles (pipelines are namespaced; the same name can exist twice). */
  listBundles: (pipelineName: string, namespace?: string) =>
    get<Bundle[]>(`/pipelines/${seg(pipelineName)}/bundles${nsQuery(namespace)}`),
  /** The DAG of a bundle. Bundle names repeat across namespaces (each
   *  pipeline's bundles are its namespace's), so pass the namespace on screen:
   *  without it the server reads whichever namespace's bundle it finds first. */
  getGraph: (bundleName: string, namespace?: string) =>
    get<GraphResponse>(`/bundles/${seg(bundleName)}/graph${nsQuery(namespace)}`),
  /** The PromotionSteps of a bundle; pass the namespace as for getGraph. */
  getSteps: (bundleName: string, namespace?: string) =>
    get<PromotionStep[]>(`/bundles/${seg(bundleName)}/steps${nsQuery(namespace)}`),
  listGates: () => get<PolicyGate[]>('/gates'),
  /** Kubernetes events for a PromotionStep node — newest-first, capped at 20 (#527). */
  getStepEvents: (namespace: string, stepName: string) =>
    get<StepEvent[]>(`/steps/${seg(namespace)}/${seg(stepName)}/events`),
  /** Trigger a new promotion for the given pipeline+environment (UI promote button). */
  promote: (pipeline: string, environment: string, namespace = 'default') =>
    post<{ bundle: string; message: string }>('/promote', { pipeline, environment, namespace }),
  /** Trigger a rollback for the given pipeline (#331). */
  rollback: (pipeline: string, environment: string, namespace = 'default', toBundle?: string, holdReason?: string) =>
    post<{ bundle: string; message: string; held?: boolean }>('/rollback', {
      pipeline, environment, namespace, toBundle,
      ...(holdReason !== undefined ? { hold: true, holdReason } : {}),
    }),
  /** Release the hold of a rollback on an environment (kardinal release-hold, #1528). */
  releaseHold: (pipeline: string, environment: string, namespace = 'default') =>
    post<{ message: string }>('/release-hold', { pipeline, environment, namespace }),
  /** Pause a pipeline — sets spec.paused=true (#506). */
  pause: (pipeline: string, namespace = 'default') =>
    post<{ message: string }>('/pause', { pipeline, namespace }),
  /** Resume a paused pipeline — sets spec.paused=false (#506). */
  resume: (pipeline: string, namespace = 'default') =>
    post<{ message: string }>('/resume', { pipeline, namespace }),
  /** Validate a CEL expression using the server-side kro CEL environment. */
  validateCEL: (expression: string) =>
    post<{ valid: boolean; error?: string }>('/validate-cel', { expression }),
  /** Create a Bundle from the UI "Create Bundle" dialog (#917). */
  createBundle: (pipeline: string, image: string, commitSHA?: string, author?: string, namespace = 'default') =>
    post<{ bundle: string; message: string }>('/bundles', { pipeline, image, commitSHA, author, namespace }),
}
