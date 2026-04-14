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
  /** #462: number of PolicyGates with ready=false for the active bundle. */
  blockerCount?: number
  /** #462: number of PromotionSteps with state=Failed for the active bundle. */
  failedStepCount?: number
  /** #462: days since the active bundle was created (stale inventory indicator). */
  inventoryAgeDays?: number
  /** #462: RFC3339 timestamp of the last environment that reached Verified. */
  lastMergedAt?: string
  /** #462: CD automation level derived from PolicyGate count. */
  cdLevel?: 'full-cd' | 'mostly-cd' | 'manual'
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
  /** #503: Per-environment promotion statuses for the timeline view. */
  environments?: BundleEnvStatus[]
}

/** #503: Per-environment promotion status for a Bundle. */
export interface BundleEnvStatus {
  name: string
  phase?: string
  prURL?: string
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
  /** #501: Bake countdown — contiguous healthy minutes elapsed (from CRD status). */
  bakeElapsedMinutes?: number
  /** #501: Bake target minutes from Pipeline spec (0 = no bake configured). */
  bakeTargetMinutes?: number
  /** #501: Number of bake timer resets due to health alarms. */
  bakeResets?: number
}

export interface PolicyGate {
  name: string
  namespace: string
  expression: string
  ready: boolean
  reason?: string
  lastEvaluatedAt?: string
  /** #502: Override history from spec.overrides[] — shown in GateDetailPanel. */
  overrides?: PolicyGateOverride[]
}

/** #502: A time-limited emergency override record (K-09 audit record). */
export interface PolicyGateOverride {
  reason: string
  stage?: string
  expiresAt?: string
  createdAt?: string
  createdBy?: string
}
