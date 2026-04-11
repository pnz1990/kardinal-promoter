// api/client.ts — Typed fetch wrappers for the kardinal UI backend API.

import type { Pipeline, Bundle, GraphResponse, PromotionStep, PolicyGate } from '../types'

const BASE = '/api/v1/ui'

async function get<T>(path: string): Promise<T> {
  const resp = await fetch(`${BASE}${path}`)
  if (!resp.ok) {
    throw new Error(`API error ${resp.status}: ${resp.statusText}`)
  }
  return resp.json() as Promise<T>
}

export const api = {
  listPipelines: () => get<Pipeline[]>('/pipelines'),
  listBundles: (pipelineName: string) => get<Bundle[]>(`/pipelines/${pipelineName}/bundles`),
  getGraph: (bundleName: string) => get<GraphResponse>(`/bundles/${bundleName}/graph`),
  getSteps: (bundleName: string) => get<PromotionStep[]>(`/bundles/${bundleName}/steps`),
  listGates: () => get<PolicyGate[]>('/gates'),
}
