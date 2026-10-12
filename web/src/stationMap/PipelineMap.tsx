// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// stationMap/PipelineMap.tsx — the pipeline page's one stage graph (U2): the
// station map of a Pipeline, following one Bundle, with the expanded bands
// and platforms kept per pipeline. It replaces the lane cards and the DAG.

import { lazy, Suspense, useEffect, useMemo, useState } from 'react'
import type { Bundle, GraphResponse, Pipeline, PolicyGate, PromotionStep } from '../types'
import { api } from '../api/client'
import StationMap from './StationMap'
import { Segmented } from './Segmented'
import { Skeleton } from '../ui'
// The Table and the Matrix load when first shown: they are not part of the initial load.
const StageTable = lazy(() => import('./StageTable'))
const VersionMatrix = lazy(() => import('./VersionMatrix'))
import type { MapView } from '../useUrlState'
import { canPromote, canRollback } from '../pipelineActions'
import { PipelineActionDialog, type PipelineActionKind } from '../components/PipelineActionDialog'
import { buildModel, bundleVersion } from './model'
import { ErrorNotice } from '../ui/ErrorNotice'

export interface PipelineMapProps {
  pipeline: Pipeline
  /** The Bundle the thread follows; undefined shows the topology and what runs. */
  bundle?: Bundle
  graph?: GraphResponse
  /** A second Bundle to compare: its route is drawn as an ink dashed thread. */
  compareBundle?: Bundle
  loading?: boolean
  error?: string
  focused?: string
  onOpenStage: (env: string, section?: 'why') => void
  /** After a promote, rollback or release, so the page refreshes. */
  onActionDone?: () => void
  /** Open a gate's details (until the U3 drawer shows gates under "why"). */
  onOpenGate?: (gateNodeId: string) => void
  /** The PolicyGates: approval gates show their quorum on the map. */
  gates?: PolicyGate[]
  /** The Pipeline's Bundles: an older followed Bundle is not shown as incoming. */
  bundles?: Bundle[]
  /** Read the map's data again after a failed read (U4). */
  onRetry?: () => void
  /** How to show the pipeline: the station map (undefined), a table or a matrix. */
  as?: MapView
  onAs?: (as: MapView) => void
  /** The Matrix: follow a version, or compare one with the followed version. */
  onSelectVersion?: (bundle: string) => void
  onCompareVersion?: (bundle: string) => void
  /** What the stage drawer reads (steps and all gates): the Table's rows offer the drawer's actions. */
  stageData?: { steps: PromotionStep[]; gates: PolicyGate[] }
  /** A version the rail previews: its route is a ghost thread on the map. */
  previewBundle?: Bundle
}

/** The graphs read for the compared and previewed versions, by namespace/name: the most
 *  recently used GRAPH_CACHE of them (a Map keeps insertion order; a read moves it last). */
const GRAPHS = new Map<string, GraphResponse>()
const GRAPH_CACHE = 20
function remember(key: string, graph: GraphResponse) {
  GRAPHS.delete(key)
  GRAPHS.set(key, graph)
  while (GRAPHS.size > GRAPH_CACHE) GRAPHS.delete(GRAPHS.keys().next().value!)
}

/** The graph of a Bundle (the compared and the previewed version). It is read again only
 *  when the version or its state changes, not on every poll, which hands a new object. */
function useBundleGraph(b: Bundle | undefined): { bundle: Bundle; graph: GraphResponse } | undefined {
  const [g, setG] = useState<{ name: string; graph: GraphResponse } | undefined>()
  const key = b ? `${b.namespace}/${b.name}` : ''
  const phase = b?.phase
  useEffect(() => {
    if (!key) { setG(undefined); return }
    const [ns, name] = [key.slice(0, key.indexOf('/')), key.slice(key.indexOf('/') + 1)]
    // A version hovered again shows its last route at once, then the fresh one.
    const seen = GRAPHS.get(key)
    if (seen) { remember(key, seen); setG({ name, graph: seen }) }
    let live = true
    api.getGraph(name, ns)
      .then(graph => { remember(key, graph); if (live) setG({ name, graph }) })
      .catch(() => { if (live) setG(undefined) })
    return () => { live = false }
  }, [key, phase])
  return useMemo(() => (b && g?.name === b.name ? { bundle: b, graph: g.graph } : undefined), [b, g])
}

const VIEWS: Array<{ id: MapView; label: string }> = [
  { id: 'map', label: 'Map' }, { id: 'table', label: 'Table' }, { id: 'matrix', label: 'Matrix' },
]

export default function PipelineMap({ pipeline, bundle, graph, compareBundle, loading, error, focused, onOpenStage, onActionDone, onOpenGate, gates, bundles, onRetry, as, onAs, onSelectVersion, onCompareVersion, stageData, previewBundle }: PipelineMapProps) {
  const [pending, setPending] = useState<{ kind: PipelineActionKind; env: string } | null>(null)
  const [result, setResult] = useState<string | null>(null)
  const key = `${pipeline.namespace}/${pipeline.name}`
  const [expanded, setExpanded] = useState<Set<string>>(() => new Set())
  // Expanded groups belong to one pipeline.
  useEffect(() => { setExpanded(new Set()) }, [key])

  const toggleGroup = (id: string) => setExpanded(prev => {
    const next = new Set(prev)
    if (next.has(id)) next.delete(id)
    else next.add(id)
    return next
  })

  const compare = useBundleGraph(compareBundle)
  const preview = useBundleGraph(previewBundle)

  // What the Table's rows read for the drawer's actions: one object while the data stays the same.
  const tableStage = useMemo(() => stageData && { pipeline, bundles: bundles ?? [], steps: stageData.steps, gates: stageData.gates, onChanged: onActionDone },
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [pipeline, bundles, stageData?.steps, stageData?.gates])
  const followed = bundle ? { bundle, graph } : undefined
  const model = useMemo(() => buildModel(pipeline, followed, { expanded, gates, bundles }),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [pipeline, bundle, graph, expanded, gates, bundles])
  const compareModel = useMemo(() => compare ? buildModel(pipeline, compare, { expanded, gates, bundles }) : undefined,
    [pipeline, compare, expanded, gates, bundles])
  const previewModel = useMemo(() => preview ? buildModel(pipeline, preview, { expanded, gates, bundles }) : undefined,
    [pipeline, preview, expanded, gates, bundles])

  // The lane's quick actions, under each station at Detail: Promote where the
  // upstreams are Verified, Roll back where the step landed, Release hold.
  const holds = new Map((pipeline.environmentTopology ?? []).filter(e => e.hold).map(e => [e.name, e.hold!]))
  const actionsFor = (env: string) => {
    const node = graph?.nodes.find(n => n.type === 'PromotionStep' && n.environment === env)
    const held = holds.get(env)
    const promote = !!node && !!graph && canPromote(node, graph.nodes, graph.edges)
    const rollback = !!node && canRollback(node) && !held
    if (!promote && !rollback && !held) return null
    const ask = (kind: PipelineActionKind) => { setResult(null); setPending({ kind, env }) }
    return (
      <>
        {promote && <button type="button" onClick={() => ask('promote')}>Promote to {env}</button>}
        {rollback && <button type="button" data-kind="rollback" onClick={() => ask('rollback')}>Roll back {env}</button>}
        {held && <button type="button" onClick={() => ask('release-hold')}>Release hold on {env}</button>}
      </>
    )
  }

  // Map | Table | Matrix: in the map's own bar on the map, above the table or the matrix.
  const views = onAs && (
    <Segmented label="View" className="map-views" value={as ?? 'map'} onChange={v => onAs(v)}
      items={VIEWS.map(v => ({ id: v.id, text: v.label }))} />
  )

  if (error) {
    // What the failed read means, in words, with Retry (U4).
    return <div className="station-map station-map--message"><ErrorNotice error={error} onRetry={onRetry} /></div>
  }
  if (loading && !graph && !pipeline.environmentTopology) {
    return <div className="station-map station-map--message" aria-busy="true">Loading the pipeline map…</div>
  }
  return (
    <div className="pipeline-map">
    {result && <div role="status" className="station-map__result">{result}</div>}
    {pending && (
      <PipelineActionDialog kind={pending.kind} pipelineName={pipeline.name} environment={pending.env} namespace={pipeline.namespace}
        onDone={message => { setPending(null); setResult(message); onActionDone?.() }}
        onCancel={() => setPending(null)} />
    )}
    {as !== 'map' && views && <div className="pipeline-map__views">{views}</div>}
    {as === 'table' ? (
      <Suspense fallback={<Skeleton label="Loading the table…" />}>
        <StageTable model={model} focused={focused} onOpenStage={env => onOpenStage(env)}
          stage={tableStage} />
      </Suspense>
    ) : as === 'matrix' ? (
      <Suspense fallback={<Skeleton label="Loading the matrix…" />}>
        <VersionMatrix model={model} pipeline={pipeline.name} bundles={bundles ?? []} followed={bundle?.name} compared={compareBundle?.name}
          onSelect={b => onSelectVersion?.(b)} onCompare={b => onCompareVersion?.(b)} onToggleGroup={toggleGroup} />
      </Suspense>
    ) : (
    <StationMap
      views={views || undefined}
      actionsFor={actionsFor}
      onOpenGate={onOpenGate}
      key={key}
      model={model}
      compare={compareModel}
      ghost={previewModel}
      focused={focused}
      onOpenStage={onOpenStage}
      expanded={expanded}
      onToggleGroup={toggleGroup}
      onSetExpanded={setExpanded}
      paused={pipeline.paused}
      followedVersion={bundle ? bundleVersion(bundle, bundle.name === pipeline.activeBundleName ? pipeline.activeBundleVersion : undefined) : undefined}
    />
    )}
    </div>
  )
}
