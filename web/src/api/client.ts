// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// api/client.ts — Typed fetch wrappers for the kardinal UI backend API.

import type { Pipeline, Bundle, GraphResponse, PromotionStep, PolicyGate } from '../types'
import type { StepEvent } from '../components/EventsPanel'

const BASE = '/api/v1/ui'

async function get<T>(path: string): Promise<T> {
  const resp = await fetch(`${BASE}${path}`)
  if (!resp.ok) {
    throw new Error(`API error ${resp.status}: ${resp.statusText}`)
  }
  return resp.json() as Promise<T>
}

async function post<T>(path: string, body: unknown): Promise<T> {
  const resp = await fetch(`${BASE}${path}`, {
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

export const api = {
  listPipelines: () => get<Pipeline[]>('/pipelines'),
  /** Bundles of a pipeline, newest first. With namespace, only that namespace's
   *  bundles (pipelines are namespaced; the same name can exist twice). */
  listBundles: (pipelineName: string, namespace?: string) =>
    get<Bundle[]>(`/pipelines/${seg(pipelineName)}/bundles${namespace ? `?namespace=${seg(namespace)}` : ''}`),
  getGraph: (bundleName: string) => get<GraphResponse>(`/bundles/${seg(bundleName)}/graph`),
  getSteps: (bundleName: string) => get<PromotionStep[]>(`/bundles/${seg(bundleName)}/steps`),
  listGates: () => get<PolicyGate[]>('/gates'),
  /** Kubernetes events for a PromotionStep node — newest-first, capped at 20 (#527). */
  getStepEvents: (namespace: string, stepName: string) =>
    get<StepEvent[]>(`/steps/${seg(namespace)}/${seg(stepName)}/events`),
  /** Trigger a new promotion for the given pipeline+environment (UI promote button). */
  promote: (pipeline: string, environment: string, namespace = 'default') =>
    post<{ bundle: string; message: string }>('/promote', { pipeline, environment, namespace }),
  /** Trigger a rollback for the given pipeline (#331). */
  rollback: (pipeline: string, environment: string, namespace = 'default', toBundle?: string) =>
    post<{ bundle: string; message: string }>('/rollback', { pipeline, environment, namespace, toBundle }),
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
