// api/client.ts — Typed fetch wrappers for the kardinal UI backend API.

import type { Pipeline, Bundle, GraphResponse, PromotionStep, PolicyGate, StepEvent } from '../types'

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

export const api = {
  listPipelines: () => get<Pipeline[]>('/pipelines'),
  listBundles: (pipelineName: string) => get<Bundle[]>(`/pipelines/${pipelineName}/bundles`),
  getGraph: (bundleName: string) => get<GraphResponse>(`/bundles/${bundleName}/graph`),
  getSteps: (bundleName: string) => get<PromotionStep[]>(`/bundles/${bundleName}/steps`),
  listGates: () => get<PolicyGate[]>('/gates'),
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
  /** Approve (override) a PolicyGate with a reason and expiry (#506). */
  approveGate: (gateName: string, gateNamespace = 'default', reason: string, expiresInMinutes = 60) =>
    post<{ message: string }>(`/gates/${gateNamespace}/${gateName}/approve`, { reason, expiresInMinutes }),
  /** Validate a CEL expression using the server-side kro CEL environment. */
  validateCEL: (expression: string) =>
    post<{ valid: boolean; error?: string }>('/validate-cel', { expression }),
  /** #527: Kubernetes Events for a PromotionStep — shown in NodeDetail event log. */
  getStepEvents: (namespace: string, stepName: string) =>
    get<StepEvent[]>(`/steps/${namespace}/${stepName}/events`),
}
