// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// types.ts — TypeScript types matching the Go API response shapes
// (cmd/kardinal-controller/ui_api.go). Keep the two in sync.

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
  /** #525: static pipeline topology from spec — shown even when no Bundle is promoting. */
  environmentTopology?: EnvironmentNode[]
  /** True when the controller resolved the ordering: environmentTopology[].upstreams
   *  is then complete, and an entry without upstreams is a root. Absent from an
   *  older controller, or when the ordering is invalid (#1580). */
  topologyResolved?: boolean
  /** What the active Bundle ships: its image tag, or "config <sha>" (fleet board). */
  activeBundleVersion?: string
  /** Who shipped the active Bundle (the Agents filter); absent when not known. */
  activeBundleActor?: ActorRef
  /** Per environment, the Bundle it runs (fleet board). Absent: never deployed. */
  deployed?: Record<string, DeployedRelease>
  /** Pipeline.status.deploymentMetrics, computed by the controller (DORA). */
  deploymentMetrics?: DeploymentMetrics
  /** Pipeline.status.summary: one sentence on where its versions are (P1, #1707), e.g.
   *  "1.5.0 live in test, uat; waiting for PR merge in prod (#42)". */
  summary?: string
  /** Pipeline.status.environments (P1, #1707; served since #1727): per environment, fleets
   *  expanded, what runs there and where the newest version stands. States go through displayState. */
  environments?: PipelineEnvironment[]
  /** Pipeline.status.conditions (#1727): Ready (reasons such as ValidationFailed and
   *  GitCredentialMissing), Paused, PathConflict and the SecretReferenceable warning. */
  conditions?: Condition[]
  /** Pipeline.status.failedVersion: why its newest version failed. Absent otherwise. */
  failedVersion?: FailedVersion
}

/** Pipeline.status.failedVersion: the newest version's Bundle failed, before any step
 *  (environment absent: the Graph could not be built, the spec is invalid) or in an environment. */
export interface FailedVersion {
  bundle: string
  version?: string
  /** The Bundle condition's reason: GraphRejected, GraphBuildFailed, InvalidIntent, StepFailed… */
  reason: string
  /** What failed and, where known, what fixes it (after " — "). */
  message?: string
  environment?: string
  /** RFC 3339. */
  since?: string
}

/** One entry of Pipeline.status.environments (P1). States go through displayState. */
export interface PipelineEnvironment {
  name: string
  /** What runs there. */
  bundle?: string
  version?: string
  /** A newer Bundle on its way in. */
  incomingBundle?: string
  incomingVersion?: string
  /** The newest Bundle's step state there (Pending when gates hold it; "" when not reached). */
  state?: string
  /** RFC 3339: Verified time, else when this state was first recorded. */
  since?: string
  /** Gates holding it. */
  blockedBy?: string[]
  prURL?: string
  /** A rollback hold keeps it on bundle. */
  held?: boolean
  /** The freeze (ChangeWindow) that holds the newest Bundle's step here (#1974). */
  frozenBy?: string
}

/** The freeze that holds a PromotionStep (its status.frozenBy). */
export interface StepFreeze {
  name: string
  since?: string
}

/** A Kubernetes status condition, as the UI API serves it. */
export interface Condition {
  type: string
  status: string
  reason?: string
  message?: string
  lastTransitionTime?: string
}

/** What one environment runs: the newest PromotionStep there that landed its change. */
export interface DeployedRelease {
  bundle: string
  /** Image tag(s) or "config <sha>"; empty when the Bundle is gone. */
  version?: string
  /** RFC 3339; absent while the change is still being health checked. */
  verifiedAt?: string
  /** Under an image Bundle: the last Bundle that deployed a config commit here,
   *  and that commit ("config abc1234"). Image and config Bundles do not
   *  supersede each other, so the environment runs both (#1353). */
  configFrom?: string
  configVersion?: string
  /** Under a config Bundle: the last Bundle that deployed images here, and their tags. */
  imagesFrom?: string
  imagesVersion?: string
  /** Who shipped bundle; absent when not known. */
  actor?: ActorRef
}

/** Who shipped a version, in short (the UI API's uiActorRef). */
export interface ActorRef {
  kind: 'human' | 'agent' | 'bot' | 'controller' | 'unverified'
  /** The verified Kubernetes user ("system:serviceaccount:ci:fixer", "alice"). */
  principal?: string
  agent?: string
  sponsor?: string
  /** The actor as one sentence ("agent fixer for alice"). */
  text: string
}

/** Pipeline.status.deploymentMetrics: DORA metrics for the last environment. */
export interface DeploymentMetrics {
  rolloutsLast30Days?: number
  p50CommitToProdMinutes?: number
  p90CommitToProdMinutes?: number
  autoRollbackRateMillis?: number
  operatorInterventionRateMillis?: number
  staleProdDays?: number
  sampleSize?: number
  /** Deployments the stability pair is computed over (last 30). */
  deployments?: number
  failedDeployments?: number
  /** failedDeployments / deployments, thousandths. */
  changeFailureRateMillis?: number
  meanTimeToRestoreMinutes?: number
  restoredFailures?: number
  computedAt?: string
}

/** #525: one environment in the static Pipeline spec topology. */
export interface EnvironmentNode {
  name: string
  dependsOn?: string[]
  approval?: string
  /** The environments it waits for as the controller resolves them (dependsOn,
   *  waves, or the previous entry). Absent for a root, or when the ordering is invalid. */
  upstreams?: string[]
  /** Set on a fleet environment: environmentStates and deployed are keyed by
   *  its target environments, which the fleet board rolls up (D1). */
  fleet?: EnvironmentFleet
  /** The environment's hold (spec.holds, kardinal rollback --hold), if held. */
  hold?: EnvironmentHold
}

/** A fleet environment's targets and pacing (spec.fleet). */
export interface EnvironmentFleet {
  /** Target environments ("<environment>-<target>"), in promotion order. */
  targets: string[]
  /** Targets promoted at once; absent or 0 is all of them. */
  maxConcurrent?: number
  /** Failed targets that stop the rollout (a percentage resolved to a count,
   *  rounded up, at least 1); absent when unset. */
  maxUnavailable?: number
  /** spec.fleet.maxUnavailable as written ("10%", "2"); absent when unset. */
  maxUnavailableSpec?: string
  /** The fleet is paused (kardinal pause --env <fleet>): no further target starts. */
  paused?: boolean
  /** Why the targets cannot be resolved (a selector fleet). */
  message?: string
}

/** A Pipeline environment pinned to a rollback Bundle until it is released (#1528). */
export interface EnvironmentHold {
  /** The rollback Bundle the environment is held on. */
  bundle: string
  reason: string
  createdBy?: string
  /** RFC 3339. */
  createdAt?: string
  /** When the controller removes the hold, RFC 3339; absent: when released. */
  expiresAt?: string
  /** The rollback Bundle does not exist (#1629); the hold stays in effect. */
  bundleMissing?: boolean
  /** What is wrong and how to recover, with bundleMissing. */
  message?: string
  /** The CLI command that releases the hold. */
  releaseCommand?: string
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
  /** #503: Per-environment promotion statuses. The bundle comparison shows
   *  them and the release metrics bar reads healthCheckedAt. */
  environments?: BundleEnvStatus[]
  /** #563: Container images in this Bundle — shown in the stage drawer's evidence. */
  images?: ImageRef[]
  /** Environments where this Rejected bundle's change is live (its step is
   *  HealthChecking or Verified there): it stays current, marked Rejected,
   *  with a roll-back hint (RejectedLiveBanner). */
  rejectedLiveEnvironments?: string[]
  /** spec.rejected: who rejected the Bundle (kardinal reject), why, and when (RFC 3339). */
  rejected?: { reason: string; by: string; at?: string }
  /** What the Bundle ships, for display (status.version: the image tag,
   *  "config <sha>", or the chart and its version). */
  version?: string
  /** Who created the Bundle (docs/actors.md): its verified actor stamp, or
   *  kind "unknown" with its v0.11 creator. */
  actor?: Actor
  /** The actor as one sentence: "agent fixer for alice", "bob via the UI". */
  actorText?: string
  /** What the creator asserted about itself, labelled as asserted. */
  actorAsserted?: string
}

/** Who made a change (docs/actors.md). kind, principal, agent, sponsor and
 *  via are verified; asserted is what the client said about itself. */
export interface Actor {
  kind: 'human' | 'agent' | 'bot' | 'controller' | 'unverified' | 'unknown'
  principal: string
  agent?: string
  sponsor?: string
  via?: string
  asserted?: { model?: string; session?: string; client?: string }
}

/** #563: A container image reference — repository, tag, and optional digest. */
export interface ImageRef {
  repository: string
  tag?: string
  digest?: string
}

/** #503: Per-environment promotion status for a Bundle. */
export interface BundleEnvStatus {
  name: string
  phase?: string
  /** Sent by the API; the UI takes PR links from the graph nodes instead. */
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
  /** The freeze that holds a PromotionStep node (#1974). */
  frozenBy?: StepFreeze
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
  /** The freeze that holds the step (#1974). */
  frozenBy?: StepFreeze
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
  /** The step's signed promotion receipt (docs/receipts.md), once Verified with receipts on. */
  receipt?: { name: string; phase: string; signed: boolean }
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
 * Superseded (its bundle was superseded; final), Rejected (its bundle was
 * rejected with kardinal reject; final), Pending (not evaluated yet),
 * Waiting (not ready, not holding the bundle; E2E-R19).
 */
export type GateState = 'Pass' | 'Block' | 'Superseded' | 'Rejected' | 'Pending' | 'Waiting'

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
  /** The template an instance was made from (kardinal.io/gate-template): the
   *  gate's name as the user wrote it. Empty on templates. */
  gateTemplate?: string
  /** When the instance was created (RFC 3339): when its environment started waiting on it. */
  createdAt?: string
  /** The longest override the controller accepts for this instance, in minutes. */
  maxOverrideMinutes?: number
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
  /** An approval gate's quorum and decisions (spec.approval, status.approvals). */
  approval?: GateApproval
  /** A verify gate's quorum and verdicts (spec.verify, status.verdicts). */
  verify?: GateVerify
}

/** A verify gate: the check it waits for, its quorum, and the verdicts it counted. */
export interface GateVerify {
  check: string
  /** The environment verified: where the version runs. */
  environment: string
  /** Passing votes needed (at least 1). */
  required: number
  /** Failing votes tolerated. */
  maxFailures?: number
  /** Agents whose verdicts count (agent verification is opt-in). */
  allowedAgents?: string[]
  onInconclusive: 'hold' | 'retry' | 'fail' | string
  timeout?: string
  /** The StepTemplate its AgentRuns run, when it runs verifiers in the cluster. */
  template?: string
  passed: number
  failed: number
  verdicts?: GateVerdict[]
}

/** One Verdict as the verify gate counted it. */
export interface GateVerdict {
  name: string
  /** Who wrote it, in a sentence ("agent qa-explorer for bob"). */
  verifier: string
  result: 'pass' | 'fail' | 'inconclusive' | string
  counted: boolean
  /** Why it does not count, or what it waits for. */
  reason?: string
  /** The AgentRun's run and attempt, for an in-cluster verifier. */
  run?: number
  attempt?: number
  summary?: string
  evidenceURL?: string
  findings?: number
  /** The findings of high severity among findings. */
  highFindings?: number
  createdAt?: string
  /** Who wrote it, in short, for the actor badge; absent when not known (then verifier). */
  verifierActor?: ActorRef
}

/** An approval gate: how many allowed people must approve, and who did. */
export interface GateApproval {
  /** Distinct allowed approvals needed (at least 1). */
  required: number
  allowedUsers?: string[]
  allowedGroups?: string[]
  /** The Bundle's creator's approval does not count. */
  excludeAuthor?: boolean
  /** Counted approve decisions. */
  approved: number
  /** A counted reject blocks the gate. */
  rejected?: boolean
  decisions?: GateDecision[]
  /** Structured values an approve must carry (spec.approval.fields): one form input each. */
  fields?: ApprovalFieldDecl[]
}

/** One structured value an approval gate asks for (spec.approval.fields). */
export interface ApprovalFieldDecl {
  name: string
  type: 'string' | 'bool' | 'int' | 'url' | 'enum' | string
  required?: boolean
  pattern?: string
  /** The one value accepted ("true" for an acknowledgement). */
  mustBe?: string
  /** An enum field's choices. */
  values?: string[]
  description?: string
}

/** One approve or reject decision as the gate counted it. */
export interface GateDecision {
  user: string
  decision: 'approve' | 'reject' | string
  counted: boolean
  /** Why it does not count. */
  reason?: string
  comment?: string
  /** RFC 3339: when the gate first saw it. */
  firstSeenAt?: string
  /** The decision's values of the declared fields. */
  fields?: Record<string, string>
}

/** #502: A time-limited emergency override record (K-09 audit record). */
export interface PolicyGateOverride {
  reason: string
  stage?: string
  expiresAt?: string
  createdAt?: string
  createdBy?: string
}

/** A Kubernetes Event of a PromotionStep (GET /api/v1/ui/steps/:ns/:name/events). */
export interface StepEvent {
  type: string          // "Normal" | "Warning"
  reason: string        // short CamelCase reason
  message: string       // human-readable message
  count: number         // number of occurrences
  firstTimestamp: string  // RFC3339
  lastTimestamp: string   // RFC3339
}

/** One action the UI asks POST /access about: verb on a kardinal.io resource. */
export interface AccessCheck {
  verb: 'get' | 'list' | 'create' | 'update' | 'patch' | 'delete'
  resource: string
  subresource?: string
  name?: string
}

/** The answer to one AccessCheck. */
export interface AccessResult {
  allowed: boolean
  /** The chart roles that grant it ("approver or admin"). */
  grantedBy?: string
  /** Why it is not allowed, when that is not a missing role (an approval without TokenReview). */
  reason?: string
}

/** POST /access: who the caller is (TokenReview mode only) and what they may do. */
export interface AccessResponse {
  user?: { username: string; groups?: string[] }
  results: AccessResult[]
}

/** A freeze: a ChangeWindow with spec.freeze (GET /api/v1/ui/freezes). */
export interface Freeze {
  name: string
  /** agents (versions agents created) or everyone (stop-the-line). */
  actors: 'agents' | 'everyone'
  /** Empty: every namespace. */
  namespaces: string[]
  /** Whom and where it holds, in words ("agents, every namespace"). */
  scope: string
  active: boolean
  since: string
  /** Empty: until it is lifted. */
  until?: string
  createdBy: string
  reason: string
  /** Exemptions in effect (kardinal override --freeze). */
  exemptions: number
}
