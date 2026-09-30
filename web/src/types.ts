// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// types.ts — TypeScript types matching the Go API response shapes
// (cmd/kardinal-controller/ui_api.go). Keep the two in sync.

/** PromotionStep status.state values (api/v1alpha1), plus the graph API's synthetic NotStarted. */
export type PromotionStepState =
  | 'Pending' | 'Promoting' | 'WaitingForMerge' | 'HealthChecking'
  | 'Verified' | 'Failed' | 'AbortedByAlarm' | 'RollingBack' | 'NotStarted'

/** Bundle status.phase values. */
export type BundlePhase = 'Available' | 'Promoting' | 'Verified' | 'Failed' | 'Superseded'

/** status.steps[].state values. */
export type StepExecutionState = 'Pending' | 'InProgress' | 'Completed' | 'Failed'

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
  /** #462: number of the active bundle's PolicyGates with ready=false in
   *  environments it has reached (every upstream Verified) but not started:
   *  the gates holding it back. Gates further down the pipeline are not
   *  counted, so a bundle still promoting upstream reads Promoting (E2E-R18). */
  blockerCount?: number
  /** #462: number of PromotionSteps with state=Failed for the active bundle. */
  failedStepCount?: number
  /** #462: days since the active bundle was created (stale inventory indicator).
   *  0 means created today; undefined means the pipeline has no active bundle. */
  inventoryAgeDays?: number
  /** #462: RFC3339 timestamp of the last environment that reached Verified. */
  lastMergedAt?: string
  /** #462: CD automation level derived from PolicyGate count. */
  cdLevel?: 'full-cd' | 'mostly-cd' | 'manual'
  /** #525: static pipeline topology from spec — shown even when no Bundle is promoting. */
  environmentTopology?: EnvironmentNode[]
}

/** #525: one environment in the static Pipeline spec topology. */
export interface EnvironmentNode {
  name: string
  dependsOn?: string[]
  approval?: string
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
  /** #563: Container images in this Bundle — used by NodeDetail diff preview. */
  images?: ImageRef[]
}

/** #563: A container image reference — repository, tag, and optional digest. */
export interface ImageRef {
  repository?: string
  tag?: string
  digest?: string
}

/** #503: Per-environment promotion status for a Bundle. */
export interface BundleEnvStatus {
  name: string
  phase?: string
  prURL?: string
  /** RFC 3339 time the post-merge health check for this environment completed. */
  healthCheckedAt?: string
}

export interface Provenance {
  commitSHA?: string
  ciRunURL?: string
  author?: string
  /** metav1.Time: serialized as null (not omitted) when unset. */
  timestamp?: string | null
  /** Name of the Bundle this Bundle rolls back, when it is a rollback. */
  rollbackOf?: string
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
  /** ISO timestamp of last CEL evaluation. Set on PolicyGate nodes only. */
  lastEvaluatedAt?: string
  /** ISO timestamp when the PromotionStep was created — used for elapsed timers (#330).
   *  Set on PromotionStep nodes only. */
  startedAt?: string
  /** PolicyGate nodes: true when the gate holds the bundle back, the rule the
   *  pipeline's blockerCount uses (graph.GateHolds, decided by the UI API).
   *  The node's state is then 'Block'; see GateState for the others (E2E-R19). */
  holding?: boolean
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
  /** Index of the currently executing sub-step within `steps`. */
  currentStepIndex?: number
  /** Per-step progress from status.steps[], in execution order. */
  steps?: StepStatus[]
  /** #341: Kubernetes conditions from status.conditions — shows transition history. */
  conditions?: Array<{
    type: string
    status: string
    reason?: string
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

/** One entry of PromotionStep status.steps[]. */
export interface StepStatus {
  name: string
  state: StepExecutionState
  startedAt?: string
  completedAt?: string
  durationMs?: number
  message?: string
}

/**
 * PolicyGate state from the UI API, decided by graph.GateState:
 * Pass (ready), Block (holds the bundle back; only these count as blocked),
 * Superseded (its bundle was superseded; final), Pending (not evaluated yet),
 * Waiting (not ready, not holding the bundle; E2E-R19).
 */
export type GateState = 'Pass' | 'Block' | 'Superseded' | 'Pending' | 'Waiting'

export interface PolicyGate {
  name: string
  namespace: string
  expression: string
  ready: boolean
  reason?: string
  lastEvaluatedAt?: string
  /** Pipeline, bundle and environment of a gate instance (kardinal.io/* labels). Empty on templates. */
  pipeline?: string
  bundle?: string
  environment?: string
  /** True for a PolicyGate template: never evaluated for a bundle, always ready=false. */
  template?: boolean
  /** True when this gate instance holds its bundle back (graph.GateHolds, decided
   *  by the UI API). Its state is then 'Block'. */
  holding?: boolean
  /** The state to show, decided by the UI API (graph.GateState), the same as the
   *  gate's node in the bundle graph. */
  state: GateState
  /** #502: Override history from spec.overrides[]. */
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
