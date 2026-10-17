// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// stage/StageDrawer.tsx — U3: everything about one environment of a
// pipeline, "why it's waiting" first, then the incident actions, the
// evidence and the history. Raw CEL expressions and conditions sit behind
// "details". The open/close contract with the station map is #1701: the page
// owns node=<env> and section=, the drawer calls onClose and onNavigate, and
// the map returns focus to the station. At the lg breakpoint and wider the
// drawer is a non-modal column beside the page (the page gives up its
// width); narrower, a modal sheet with trapped focus. Esc closes an open action dialog first, then the drawer.
import { useEffect, useMemo, useRef, useState, type MutableRefObject } from 'react'
import { api } from '../api/client'
import CopyButton from '../components/CopyButton'
import { displayState, type Tone } from '../displayStates.gen'
import { formatElapsedSince } from '../timeFormat'
import { ActorBadge, StatusChip } from '../ui'
import { actorOfBundle, actorOfUser } from '../actor'
import { CELBlock } from '../components/CELBlock'
import { VerifyQuorum } from '../components/VerifyQuorum'
import EventsPanel from '../components/EventsPanel'
import { StepProgress } from '../components/StepProgress'
import type { Bundle, Pipeline, PolicyGate, PromotionStep, StepEvent } from '../types'
import { useModalFocus } from '../useModalFocus'
import { KubernetesDetails } from '../ui/KubernetesDetails'
import { ActionButton } from './ActionButton'
import { ACTION_LABEL, DESTRUCTIVE, StageActions } from './StageActions'
import { useStageAccess, type ReasonFor } from './useStageAccess'
import { approveCommand, SignInNote, useSignInNeeded } from './signIn'
import { useChangeHalo } from '../useChangeHalo'
import { useDrawerColumn, useWide } from './drawerColumn'
import { gateDisplay, gateLabel, IN_FLIGHT, stageView, type StageActionKind, type WaitReason } from './stageModel'
import '../styles/StageDrawer.css'

export type StageSection = 'why' | 'actions' | 'evidence' | 'history'

/** A keyboard command for the open drawer (U7): true when it opened the action, the reason when the
 *  person may not take it, false when it does not apply here. */
export type StageCommand = (kind: StageActionKind) => true | string | false

export interface StageDrawerProps {
  namespace: string
  pipeline: string
  env: string
  section?: string
  onClose: () => void
  onNavigate: (env: string, section?: StageSection) => void
  /** The page's polled data (the drawer does not poll these itself). */
  data: { pipeline?: Pipeline; bundles: Bundle[]; steps: PromotionStep[]; gates: PolicyGate[] }
  /** Called after an action succeeds, so the page refetches at once. */
  onChanged?: () => void
  /** For tests: the current time. */
  now?: number
  /** Set to the drawer's keyboard command while it is open (U7). */
  commandRef?: MutableRefObject<StageCommand | null>
}


export function StageDrawer(props: StageDrawerProps) {
  const wide = useWide()
  return wide ? <DrawerColumn {...props} /> : <DrawerSheet {...props} />
}

function DrawerColumn(props: StageDrawerProps) {
  const [dialogOpen, setDialogOpen] = useState(false)
  useDrawerColumn()
  return (
    <aside className="stage-drawer" data-testid="stage-drawer" aria-labelledby={`stage-drawer-${props.env}`}
      onKeyDown={e => {
        // An open action dialog closes on Esc itself; only then the drawer.
        if (e.key === 'Escape' && !dialogOpen && !e.defaultPrevented) { e.preventDefault(); props.onClose() }
      }}>
      <DrawerBody {...props} onDialog={setDialogOpen} />
    </aside>
  )
}

function DrawerSheet(props: StageDrawerProps) {
  const ref = useRef<HTMLDivElement>(null)
  const [dialogOpen, setDialogOpen] = useState(false)
  useModalFocus(ref, () => { if (!dialogOpen) props.onClose() })
  return (
    <div className="stage-drawer__scrim">
      <div ref={ref} role="dialog" aria-modal="true" className="stage-drawer stage-drawer--sheet" data-testid="stage-drawer"
        aria-labelledby={`stage-drawer-${props.env}`}>
        <DrawerBody {...props} onDialog={setDialogOpen} />
      </div>
    </div>
  )
}

function DrawerBody({ namespace, pipeline, env, section, onClose, onNavigate, data, onChanged, now: nowProp,
  onDialog, commandRef }: StageDrawerProps & { onDialog?: (open: boolean) => void }) {
  const p = data.pipeline
  const view = useMemo(() => p ? stageView({ env, pipeline: p, bundles: data.bundles, steps: data.steps, gates: data.gates }) : undefined,
    [env, p, data.bundles, data.steps, data.gates])
  // The clock: each second while the promotion runs (its elapsed time ticks), else per render.
  const running = !!view?.step && IN_FLIGHT.has(view.step.state)
  const [clock, setClock] = useState(() => Date.now())
  useEffect(() => {
    if (nowProp !== undefined || !running) return
    const t = window.setInterval(() => setClock(Date.now()), 1000)
    return () => window.clearInterval(t)
  }, [nowProp, running])
  const now = nowProp ?? (running ? clock : Date.now())
  const reasonFor = useStageAccess(view, pipeline, namespace)
  // Without sign-in Approve and Decline are inert; the approval's reason says why, once.
  const signIn = useSignInNeeded(view ? namespace : undefined)
  // U7: the environment's state changed on a poll (not when another environment opens).
  const stateChanged = useChangeHalo(view?.state.label ?? '', env) && !!view
  useEffect(() => {
    if (!commandRef) return
    commandRef.current = kind => {
      if (!view?.actions.has(kind)) return false
      const why = reasonFor(kind)
      if (why) return why
      open(kind)
      return true
    }
    return () => { commandRef.current = null }
  })
  const [action, setAction] = useState<StageActionKind>()
  const [actionGate, setActionGate] = useState<PolicyGate>()
  const open = (k: StageActionKind | undefined, gate?: PolicyGate) => { setAction(k); setActionGate(gate); onDialog?.(!!k) }
  const sectionRefs = useRef<Record<string, HTMLElement | null>>({})
  useEffect(() => {
    if (section) sectionRefs.current[section]?.scrollIntoView?.({ block: 'start' })
  }, [section, env])
  const headingId = `stage-drawer-${env}`
  // Actions shown next to their reason; the action row does not repeat them.
  const inline = useMemo(() => new Set((view?.why ?? []).map(w => w.next).filter((k): k is StageActionKind => !!k)), [view])
  const fleet = p?.environmentTopology?.find(n => n.name === env)?.fleet
  const noteInWhy = signIn && !!view?.why.some(w => w.kind === 'approval' && (w.next === 'approve' || w.next === 'reject'))

  // A fleet environment's drawer has only the fleet's actions (#1835).
  const actions = view && <>
    <h3 id={`${headingId}-actions`} className="stage-section__title">Actions</h3>
    <StageActions view={view} pipeline={pipeline} namespace={namespace} active={action} activeGate={actionGate} onOpen={open} onChanged={onChanged} inline={inline} reasonFor={reasonFor}
      signInNote={signIn ? (noteInWhy ? null : approveCommand(pipeline, env, namespace)) : undefined} />
  </>

  return (
    <>
      <header className="stage-drawer__header">
        <div>
          <p className="stage-drawer__eyebrow">{pipeline} · environment</p>
          <h2 id={headingId} className="stage-drawer__title">{env}</h2>
        </div>
        <button type="button" className="stage-icon-button" aria-label={`Close ${env}`} onClick={onClose}>
          <span aria-hidden="true">×</span>
        </button>
      </header>
      {!view && <p className="stage-muted">Loading {env}…</p>}
      {view && fleet && (
        <section className="stage-section" data-section="why">
          <h3 className="stage-section__title">Fleet</h3>
          <p className="stage-muted">{env} promotes to {fleet.targets.length} targets{fleet.maxConcurrent ? `, ${fleet.maxConcurrent} at a time` : ''}{fleet.paused ? '; paused: no further target starts' : ''}.</p>
          <ul className="stage-targets">
            {fleet.targets.map(t => (
              <li key={t}>
                <button type="button" className="stage-link" onClick={() => onNavigate(t, 'why')}>{t}</button>
                <StateChip label={displayState('step', p?.environmentStates?.[t] ?? 'NotStarted').label}
                  tone={displayState('step', p?.environmentStates?.[t] ?? 'NotStarted').tone} />
              </li>
            ))}
          </ul>
        </section>
      )}
      {view && fleet && <section className="stage-section" data-section="actions">{actions}</section>}
      {view && !fleet && (
        <>
          <div className="stage-drawer__status">
            <StateChip label={view.state.label} tone={view.state.tone} glyph={view.state.glyph} changed={stateChanged} />
            <dl className="stage-versions">
              <div><dt>Running</dt><dd>{view.running ? <Version {...view.running} /> : 'nothing yet'}
                {view.running?.verifiedAt && <span className="stage-muted"> · live {formatElapsedSince(view.running.verifiedAt, now)}</span>}</dd></div>
              {view.incoming && <div><dt>Incoming</dt><dd><Version {...view.incoming} /></dd></div>}
              <ShippedBy bundle={data.bundles.find(x => x.name === (view.incoming ?? view.running)?.bundle)} />
            </dl>
          </div>

          <section className="stage-section" data-section="why" ref={el => { sectionRefs.current.why = el }} aria-labelledby={`${headingId}-why`}>
            <h3 id={`${headingId}-why`} className="stage-section__title">{view.why[0]?.kind === 'failed' ? 'Why it failed' : view.why.length > 0 ? 'Why it’s waiting' : 'Status'}</h3>
            {view.why.length === 0
              ? <p className="stage-muted">{view.running && !view.incoming ? `Nothing is waiting: ${view.running.version} is live.` : `${view.state.label}.`}</p>
              : <ol className="stage-why">{view.why.map((w, i) => (
                  <WhyItem key={i} reason={w} now={now} primary={view.primary} reasonFor={reasonFor} signIn={signIn} cli={cliCommand(w, pipeline, env, namespace)} onAct={k => open(k, w.gate)} onUpstream={u => onNavigate(u, 'why')}
                    upstreams={view.spec?.upstreams ?? []} />))}
                </ol>}
          </section>

          <section className="stage-section" data-section="actions" ref={el => { sectionRefs.current.actions = el }} aria-labelledby={`${headingId}-actions`}>
            {actions}
          </section>

          <section className="stage-section" data-section="evidence" ref={el => { sectionRefs.current.evidence = el }} aria-labelledby={`${headingId}-evidence`}>
            <h3 id={`${headingId}-evidence`} className="stage-section__title">Evidence</h3>
            {view.incoming && <Images bundle={data.bundles.find(b => b.name === view.incoming?.bundle)} />}
            <Evidence step={view.step} gates={view.gates} now={now} />
          </section>

          <section className="stage-section" data-section="history" ref={el => { sectionRefs.current.history = el }} aria-labelledby={`${headingId}-history`}>
            <h3 id={`${headingId}-history`} className="stage-section__title">History</h3>
            <ShippedByLine bundle={data.bundles.find(x => x.name === view.step?.bundle)} />
            <ReceiptLine step={view.step} pipeline={pipeline} />
            <History namespace={namespace} step={view.step} />
          </section>

          {/* The stage's object, for kubectl, collapsed at the bottom (U4 glossary). */}
          {view.step && <KubernetesDetails kind="PromotionStep" name={view.step.name} namespace={view.step.namespace ?? namespace} />}
        </>
      )}
    </>
  )
}

function StateChip({ label, tone, glyph, changed }: { label: string; tone: Tone; glyph?: string; changed?: boolean }) {
  // The ui chip (its colours and shape); stage-chip names it for the specs.
  return <StatusChip tone={tone} label={label} glyph={glyph} className={`stage-chip stage-chip--${tone}`} data-changed={changed || undefined} />
}

/** Who shipped the version the drawer is about (docs/actors.md), with what
 *  its client asserted; nothing when that is not known. */
function ShippedBy({ bundle }: { bundle?: Bundle }) {
  const by = actorOfBundle(bundle)
  if (!by) return null
  return (
    <div><dt>Shipped by</dt><dd><ActorBadge actor={by} />
      {by.asserted && <span className="stage-muted"> · {by.asserted}</span>}</dd></div>
  )
}

/** The history's first line: who shipped the step's version. */
function ShippedByLine({ bundle }: { bundle?: Bundle }) {
  const by = actorOfBundle(bundle)
  if (!by || !bundle) return null
  return <p className="stage-muted" data-testid="shipped-by">{bundle.version ?? bundle.name} shipped by <ActorBadge actor={by} />.</p>
}

/** The step's signed promotion receipt (docs/receipts.md): signed once
 *  signed, with the command that checks it; nothing without one. */
function ReceiptLine({ step, pipeline }: { step?: PromotionStep; pipeline: string }) {
  const r = step?.receipt
  if (!step || !r) return null
  return (
    <p className="stage-muted" data-testid="receipt">
      Receipt {r.name}: {r.signed ? 'signed ✓' : r.phase.toLowerCase()}
      {r.signed && <> · <code>kardinal verify-receipt {pipeline} {step.environment} --bundle {step.bundle}</code></>}
    </p>
  )
}

/** A version as people know it (the tag), with the Bundle name after it only when it says something more. */
function Version({ version, bundle }: { version: string; bundle: string }) {
  return (
    <>
      <span className="stage-version">{version}</span>
      {bundle !== version && <span className="stage-muted"> · {bundle}</span>}
    </>
  )
}

/**
 * The CLI command that moves a wait on, for the waits a person ends: the
 * approval (where the UI cannot approve, e.g. without token review, this is
 * the way to do it). -n only outside default.
 */
export function cliCommand(w: WaitReason, pipeline: string, env: string, namespace: string): string | undefined {
  return w.kind === 'approval' ? approveCommand(pipeline, env, namespace) : undefined
}

function WhyItem({ reason: w, now, primary, cli, onAct, onUpstream, upstreams, reasonFor, signIn }: { reason: WaitReason; now: number; primary?: StageActionKind;
  cli?: string; reasonFor: ReasonFor; onAct: (k: StageActionKind) => void; onUpstream: (env: string) => void; upstreams: string[]
  /** Approving needs sign-in: the inert Approve gets the one line that says so, with the command. */
  signIn?: boolean }) {
  const age = formatElapsedSince(w.evaluatedAt, now)
  const signInNote = signIn && w.kind === 'approval' && cli && (w.next === 'approve' || w.next === 'reject') ? cli : undefined
  return (
    <li className={`stage-why__item stage-why__item--${w.kind}`}>
      <p className="stage-why__title" title={w.tip}>{w.title}</p>
      {w.detail && <p className="stage-why__detail">{w.detail}</p>}
      {w.kind === 'approval' && w.gate?.approval?.decisions && w.gate.approval.decisions.length > 0 && (
        <ul className="stage-decisions">{w.gate.approval.decisions.map((d, i) => (
          <li key={i}><ActorBadge actor={actorOfUser(d.user)!} compact /> {d.decision === 'approve' ? 'approved' : 'rejected'}{d.counted ? '' : ` (not counted: ${d.reason ?? 'not allowed'})`}</li>
        ))}</ul>
      )}
      {w.kind === 'verify' && w.gate?.verify && (
        <VerifyQuorum verify={w.gate.verify} pipeline={w.gate.pipeline} namespace={w.gate.namespace} now={now} />
      )}
      {cli && !signInNote && (
        <p className="stage-why__cli">
          <span className="stage-muted">From a terminal: </span>
          <code className="stage-cli">{cli}</code>
          <CopyButton text={cli} title={`Copy ${cli}`} />
        </p>
      )}
      <div className="stage-why__meta">
        {w.gate && <span className="stage-muted">{age ? `evaluated ${age} ago` : 'not evaluated yet'}</span>}
        {w.kind === 'upstream' && upstreams.map(u => (
          <button key={u} type="button" className="stage-link" onClick={() => onUpstream(u)}>Open {u}</button>
        ))}
        {w.next && w.next !== 'openPR' && (
          <ActionButton kind={w.next} label={ACTION_LABEL[w.next]} restricted={reasonFor(w.next, w.gate)}
            className={`stage-button stage-button--sm stage-button--${w.next === primary ? 'primary' : DESTRUCTIVE.has(w.next) ? 'destructive' : 'secondary'}`}
            onClick={() => onAct(w.next!)} />
        )}
        {w.next === 'openPR' && w.prURL && <a className="stage-link" href={w.prURL} target="_blank" rel="noreferrer">Open pull request</a>}
      </div>
      {signInNote && <SignInNote command={signInNote} />}
      {w.gate && <GateDetails gate={w.gate} k8s />}
    </li>
  )
}

/** A gate's raw details, closed: its CEL, checked when opened; for a gate that holds the
 *  stage, also its object, for kubectl (the only place the drawer names the kind, U4). */
function GateDetails({ gate, k8s }: { gate: PolicyGate; k8s?: boolean }) {
  const [open, setOpen] = useState(false)
  return (
    <>
      <details className="stage-details" onToggle={e => setOpen((e.currentTarget as HTMLDetailsElement).open)}>
        <summary>Details</summary>
        {open ? <CelCheck expression={gate.expression} /> : <pre className="stage-code"><CELBlock expression={gate.expression} /></pre>}
      </details>
      {k8s && <KubernetesDetails kind="PolicyGate" name={gate.name} namespace={gate.namespace} />}
    </>
  )
}

/** A gate's CEL, with whether the controller's CEL environment compiles it (UI-NODE-01). */
function CelCheck({ expression }: { expression: string }) {
  const [check, setCheck] = useState<{ valid: boolean; error?: string }>()
  useEffect(() => {
    setCheck(undefined)
    if (!expression) return
    let live = true
    void Promise.resolve()
      .then(() => api.validateCEL(expression))
      .then(r => { if (live) setCheck(r) })
      .catch(() => { /* advisory: no chip when the check itself fails */ })
    return () => { live = false }
  }, [expression])
  return (
    <div className="stage-cel" data-invalid={check?.valid === false || undefined}>
      <pre className="stage-code"><CELBlock expression={expression} /></pre>
      {check?.valid === true && <StatusChip tone="verified" glyph="✓" label="valid" compact data-cel-check="valid" />}
      {check?.valid === false && (
        <>
          <StatusChip tone="failed" glyph="✗" label="error" compact data-cel-check="error"
            title={check.error ? `CEL compile error: ${check.error}` : 'CEL compile error'} />
          {check.error && <p className="stage-why__detail">{`CEL compile error: ${check.error}`}</p>}
        </>
      )}
    </div>
  )
}

/** The images the incoming version deploys. */
function Images({ bundle }: { bundle?: Bundle }) {
  const images = bundle?.images ?? []
  if (images.length === 0) return null
  return (
    <ul className="stage-gates" aria-label={`Images of ${bundle?.name}`}>{images.map(i => (
      <li key={i.repository}><span className="stage-version">{i.repository}:{i.tag || i.digest}</span></li>
    ))}</ul>
  )
}

function Evidence({ step, gates, now }: { step?: PromotionStep; gates: PolicyGate[]; now: number }) {
  if (!step) return <p className="stage-muted">Evidence appears when this environment gets its first promotion: the steps it ran, its bake time and its gates.</p>
  const startedAt = step.steps?.find(s => s.startedAt)?.startedAt
  return (
    <>
      {IN_FLIGHT.has(step.state) && startedAt && (
        <p className="stage-muted" data-testid="stage-elapsed">Running for {formatElapsedSince(startedAt, now)}</p>
      )}
      {/* The sub-steps and their timing bars (UI-STEPTIME-01), as the node details drew them. */}
      <StepProgress step={step} />
      {step.bakeTargetMinutes ? (
        <p className="stage-muted">Bake: {step.bakeElapsedMinutes ?? 0} of {step.bakeTargetMinutes} healthy minutes
          {step.bakeResets ? `, reset ${step.bakeResets} time${step.bakeResets === 1 ? '' : 's'}` : ''}.</p>
      ) : null}
      {step.message && <p className="stage-why__detail">{step.message}</p>}
      {gates.length > 0 && (
        <ul className="stage-gates">{gates.map(g => {
          const d = gateDisplay(g)
          return (
            <li key={g.name}><StateChip label={d.label} tone={d.tone} glyph={d.glyph} /> <span title={g.name}>{gateLabel(g)}</span>
              {g.lastEvaluatedAt && <span className="stage-muted"> · {formatElapsedSince(g.lastEvaluatedAt, now)} ago</span>}
              <GateDetails gate={g} /></li>
          )
        })}</ul>
      )}
      {step.conditions && step.conditions.length > 0 && (
        <details className="stage-details">
          <summary>Details</summary>
          <dl className="stage-conditions">{step.conditions.map(c => (
            <div key={c.type}><dt>{c.type}</dt><dd>{c.status}{c.reason ? ` · ${c.reason}` : ''}{c.message ? `: ${c.message}` : ''}</dd></div>
          ))}</dl>
        </details>
      )}
    </>
  )
}

function History({ namespace, step }: { namespace: string; step?: PromotionStep }) {
  const [events, setEvents] = useState<StepEvent[]>()
  const [error, setError] = useState<string>()
  useEffect(() => {
    if (!step) return
    let live = true
    api.getStepEvents(namespace, step.name)
      .then(e => { if (live) { setEvents(e); setError(undefined) } })
      .catch(e => { if (live) setError(e instanceof Error ? e.message : String(e)) })
    return () => { live = false }
  }, [namespace, step?.name, step?.state])
  if (!step) return <p className="stage-muted">Events appear here once a promotion starts in this environment.</p>
  if (error) return <p className="stage-muted">The events could not be read: {error}</p>
  if (!events) return <p className="stage-muted">Loading events…</p>
  if (events.length === 0) return <p className="stage-muted">No events yet. They appear as the promotion runs.</p>
  // The Kubernetes events of the step, as the node details listed them (UI-NODE-01).
  return <EventsPanel events={events} stepName={step.name} namespace={namespace} />
}
