// types.ts — TypeScript types matching the Go API response shapes.

export interface Pipeline {
  name: string
  namespace: string
  phase: string
  environmentCount: number
  activeBundleName?: string
  /** True when the pipeline has spec.paused=true (#328). */
  paused?: boolean
  /** #342: per-environment promotion phases from active Bundle status.
   * Keys are environment names, values are the promotion phase (Promoting, Verified, etc.) */
  environmentStates?: Record<string, string>
  /** #462: Number of environments currently in Failed state. */
  blockedCount?: number
  /** #462: Seconds since the active bundle was created (0 = no active bundle). */
  lastBundleAgeSeconds?: number
  /** #462: ISO 8601 creation time of the active bundle. */
  activeBundleCreatedAt?: string
}

export interface Bundle {
  name: string
  namespace: string
  phase: string
  type: string
  pipeline: string
  /** ISO 8601 creation timestamp for timeline sorting (#337). */
  createdAt?: string
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
  /** ISO timestamp when the PromotionStep was created — used for elapsed timers (#330). */
  startedAt?: string
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
  /** Index of the currently executing sub-step within the step sequence. */
  currentStepIndex?: number
  /** #341: Kubernetes conditions from status.conditions — shows transition history. */
  conditions?: Array<{
    type: string
    status: string
    message?: string
    lastTransitionTime?: string
  }>
}

export interface PolicyGate {
  name: string
  namespace: string
  expression: string
  ready: boolean
  reason?: string
  lastEvaluatedAt?: string
}
