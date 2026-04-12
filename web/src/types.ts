// types.ts — TypeScript types matching the Go API response shapes.

export interface Pipeline {
  name: string
  namespace: string
  phase: string
  environmentCount: number
  activeBundleName?: string
}

export interface Bundle {
  name: string
  namespace: string
  phase: string
  type: string
  pipeline: string
  provenance?: Provenance
}

export interface Provenance {
  commitSHA?: string
  ciRunURL?: string
  author?: string
  timestamp?: string
}

export interface GraphNode {
  id: string
  type: 'PromotionStep' | 'PolicyGate'
  label: string
  environment: string
  state: string
  message?: string
  prURL?: string
  outputs?: Record<string, string>
  /** CEL expression for PolicyGate nodes. Populated by the graph API. */
  expression?: string
  /** ISO timestamp of last CEL evaluation for PolicyGate nodes. */
  lastEvaluatedAt?: string
}

export interface GraphEdge {
  from: string
  to: string
}

export interface GraphResponse {
  nodes: GraphNode[]
  edges: GraphEdge[]
}

export interface PromotionStep {
  name: string
  namespace: string
  pipeline: string
  bundle: string
  environment: string
  stepType: string
  state: string
  message?: string
  prURL?: string
  outputs?: Record<string, string>
}

export interface PolicyGate {
  name: string
  namespace: string
  expression: string
  ready: boolean
  reason?: string
  lastEvaluatedAt?: string
}
