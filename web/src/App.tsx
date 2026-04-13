// App.tsx — Root component with Pipeline list sidebar and DAG view.
// #326: selectedNode is lifted here so NodeDetail renders as a split panel
// sibling of DAGView rather than a position:fixed overlay.
import { useState, useCallback, useMemo, useRef } from 'react'
import { PipelineList } from './components/PipelineList'
import { DAGView } from './components/DAGView'
import { NodeDetail } from './components/NodeDetail'
import { HealthChip } from './components/HealthChip'
import { BlockedBanner } from './components/BlockedBanner'
import { BundleTimeline } from './components/BundleTimeline'
import { BundleDiffPanel } from './components/BundleDiffPanel'
import { PolicyGatesPanel } from './components/PolicyGatesPanel'
import { PipelineLaneView } from './components/PipelineLaneView'
import { api } from './api/client'
import { usePolling } from './usePolling'
import { useRefreshIndicator } from './useRefreshIndicator'
import type { Pipeline, Bundle, GraphNode, GraphResponse, PromotionStep, PolicyGate } from './types'

const POLL_INTERVAL_MS = 5000

/** Format elapsed seconds into a human-readable staleness string. */
function formatElapsed(seconds: number | null): string {
  if (seconds === null) return 'Loading...'
  if (seconds < 5) return 'just now'
  if (seconds < 60) return `${seconds}s ago`
  const mins = Math.floor(seconds / 60)
  return `${mins}m ago`
}

export function App() {
  const [pipelines, setPipelines] = useState<Pipeline[]>([])
  const [pipelinesLoading, setPipelinesLoading] = useState(true)
  const [pipelinesError, setPipelinesError] = useState<string | undefined>()

  const [selectedPipeline, setSelectedPipeline] = useState<string | undefined>()
  const [bundles, setBundles] = useState<Bundle[]>([])
  const [bundleHistoryOpen, setBundleHistoryOpen] = useState(false)
  // Bundle selected via timeline — overrides the automatic activeBundle selection.
  const [timelineSelectedBundle, setTimelineSelectedBundle] = useState<string | undefined>()

  const [graph, setGraph] = useState<GraphResponse | undefined>()
  const [graphLoading, setGraphLoading] = useState(false)
  const [graphError, setGraphError] = useState<string | undefined>()

  // Steps for the active bundle — passed down to NodeDetail to avoid independent polling.
  const [activeSteps, setActiveSteps] = useState<PromotionStep[]>([])

  // #340: PolicyGates — polled globally every 5s.
  const [gates, setGates] = useState<PolicyGate[]>([])
  const [gatesLoading, setGatesLoading] = useState(true)

  // #326: selectedNode lifted from DAGView so NodeDetail is a split-panel sibling.
  const [selectedNode, setSelectedNode] = useState<GraphNode | null>(null)

  // #338: Bundle diff comparison state.
  const [compareBundle, setCompareBundle] = useState<string | undefined>()
  const [showDiffPanel, setShowDiffPanel] = useState(false)

  // Refresh indicator: tracks last successful poll for the staleness indicator.
  const { elapsedSeconds, onSuccess: onPollSuccess } = useRefreshIndicator()

  // Blocked gate filter state.
  const [showBlockedOnly, setShowBlockedOnly] = useState(false)

  // Shared fetch function — called by both interval poll and manual refresh.
  const doFetchAll = useCallback(async () => {
    try {
      const [ps, gs] = await Promise.all([
        api.listPipelines(),
        api.listGates().catch(() => [] as PolicyGate[]),
      ])
      setPipelines(ps)
      setGates(gs)
      setGatesLoading(false)
      setPipelinesError(undefined)
    } catch (e) {
      setPipelinesError(String(e))
    } finally {
      setPipelinesLoading(false)
    }
  }, [])

  const doFetchGraph = useCallback(async (pipelineName: string) => {
    try {
      const bs = await api.listBundles(pipelineName)
      setBundles(bs)
      const promoting = bs.find(b => b.phase === 'Promoting') ?? bs[0]
      if (promoting) {
        const [g, steps] = await Promise.all([
          api.getGraph(promoting.name),
          api.getSteps(promoting.name),
        ])
        setGraph(g)
        setActiveSteps(steps)
        setGraphError(undefined)
      }
      onPollSuccess()
    } catch (e) {
      setGraphError(String(e))
    }
  }, [onPollSuccess])

  // Poll pipeline list every 5 seconds.
  usePolling(doFetchAll, POLL_INTERVAL_MS)

  // Refresh bundles + graph + steps for the selected pipeline every 5 seconds.
  // Single poll callback — no independent sub-polls in children (#321, #322, #324).
  const selectedPipelineRef = useRef(selectedPipeline)
  selectedPipelineRef.current = selectedPipeline
  usePolling(async () => {
    if (!selectedPipelineRef.current) return
    await doFetchGraph(selectedPipelineRef.current)
  }, POLL_INTERVAL_MS, !!selectedPipeline)

  // Manual refresh (#362): re-fetch everything immediately on demand.
  const manualRefresh = useCallback(async () => {
    await doFetchAll()
    if (selectedPipelineRef.current) {
      await doFetchGraph(selectedPipelineRef.current)
    }
  }, [doFetchAll, doFetchGraph])

  const handleSelectPipeline = useCallback((name: string) => {
    setSelectedPipeline(name)
    setGraph(undefined)
    setGraphError(undefined)
    setGraphLoading(true)
    setBundleHistoryOpen(false)
    setShowBlockedOnly(false)
    setTimelineSelectedBundle(undefined) // reset timeline selection on pipeline change
    setActiveSteps([])
    setSelectedNode(null) // close detail panel when switching pipelines

    api.listBundles(name)
      .then(bs => {
        setBundles(bs)
        const promoting = bs.find(b => b.phase === 'Promoting') ?? bs[0]
        if (promoting) {
          return Promise.all([
            api.getGraph(promoting.name),
            api.getSteps(promoting.name),
          ]).then(([g, steps]) => {
            setGraph(g)
            setActiveSteps(steps)
          })
        }
      })
      .catch(e => setGraphError(String(e)))
      .finally(() => setGraphLoading(false))
  }, [])

  const activePipeline = pipelines.find(p => p.name === selectedPipeline)
  // Use timeline-selected bundle if set, otherwise fall back to auto-selection.
  const autoActiveBundle = bundles.find(b => b.phase === 'Promoting') ?? bundles[0]
  const activeBundle = timelineSelectedBundle
    ? bundles.find(b => b.name === timelineSelectedBundle) ?? autoActiveBundle
    : autoActiveBundle

  // Handler for timeline bundle selection — loads that bundle's graph.
  const handleTimelineBundleSelect = useCallback((bundleName: string) => {
    setTimelineSelectedBundle(bundleName)
    setGraphLoading(true)
    setGraphError(undefined)
    setSelectedNode(null) // close detail panel when switching bundles
    Promise.all([
      api.getGraph(bundleName),
      api.getSteps(bundleName),
    ])
      .then(([g, steps]) => {
        setGraph(g)
        setActiveSteps(steps)
        setGraphError(undefined)
      })
      .catch(e => setGraphError(String(e)))
      .finally(() => setGraphLoading(false))
  }, [])

  // Derive current namespace from the first loaded pipeline.
  const currentNamespace = pipelines[0]?.namespace

  // Determine staleness indicator color.
  const staleness = elapsedSeconds ?? 0
  const indicatorColor = pipelinesError
    ? '#f59e0b'  // amber on error
    : staleness > 15
    ? '#f59e0b'  // amber when stale > 15s
    : '#64748b'  // default muted

  // Compute blocked PolicyGate node IDs from the graph.
  const blockedGateIds = useMemo<Set<string>>(() => {
    if (!graph) return new Set()
    const ids = new Set<string>()
    for (const node of graph.nodes) {
      if (node.type === 'PolicyGate' && (node.state === 'Block' || node.state === 'Fail')) {
        ids.add(node.id)
      }
    }
    return ids
  }, [graph])

  // When showBlockedOnly is active, pass the blocked IDs to DAGView for highlight.
  const highlightIds = showBlockedOnly ? blockedGateIds : undefined

  return (
    <div style={{ display: 'flex', height: '100vh', overflow: 'hidden' }}>
      {/* Sidebar */}
      <aside style={{
        width: '240px',
        minWidth: '200px',
        background: '#0f172a',
        borderRight: '1px solid #1e293b',
        display: 'flex',
        flexDirection: 'column',
        overflow: 'hidden',
      }}>
        <div style={{
          padding: '1rem',
          borderBottom: '1px solid #1e293b',
          display: 'flex',
          justifyContent: 'space-between',
          alignItems: 'center',
        }}>
          <span style={{
            fontWeight: 700,
            fontSize: '0.9rem',
            color: '#6366f1',
            letterSpacing: '0.05em',
          }}>
            KARDINAL
          </span>
          {/* Staleness indicator with manual refresh button (#362) */}
          <button
            onClick={manualRefresh}
            title="Refresh now"
            aria-label="Refresh data"
            style={{
              background: 'none',
              border: 'none',
              cursor: 'pointer',
              display: 'flex',
              alignItems: 'center',
              gap: '3px',
              padding: 0,
            }}
          >
            <span
              title={pipelinesError ? `Error: ${pipelinesError}` : 'Last updated'}
              style={{
                fontSize: '0.65rem',
                color: indicatorColor,
                fontVariantNumeric: 'tabular-nums',
              }}
              aria-label={`Data ${formatElapsed(elapsedSeconds)}`}
            >
              {pipelinesError ? '⚠' : '●'} {formatElapsed(elapsedSeconds)}
            </span>
            <span style={{ fontSize: '0.6rem', color: '#475569' }} title="Click to refresh">↺</span>
          </button>
        </div>
        <div style={{ padding: '0.75rem 1rem', fontSize: '0.75rem', color: '#475569', fontWeight: 600, display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
          <span>PIPELINES</span>
          {currentNamespace && (
            <span style={{
              fontSize: '0.65rem',
              color: '#334155',
              background: '#1e293b',
              borderRadius: '4px',
              padding: '1px 5px',
              fontWeight: 400,
              fontFamily: 'monospace',
            }} title={`Namespace: ${currentNamespace}`}>
              {currentNamespace}
            </span>
          )}
        </div>
        <div style={{ overflowY: 'auto', flex: 1 }}>
          <PipelineList
            pipelines={pipelines}
            selected={selectedPipeline}
            onSelect={handleSelectPipeline}
            loading={pipelinesLoading}
            error={pipelinesError}
          />
        </div>
      </aside>

      {/* Main area — column layout for header + content row */}
      <main style={{ flex: 1, overflow: 'hidden', display: 'flex', flexDirection: 'column', background: '#0f172a' }}>
        {!selectedPipeline ? (
          <div style={{ color: '#475569', padding: '3rem 2rem', textAlign: 'center' }}>
            {pipelines.length > 0 ? (
              <>
                <div style={{ fontSize: '1.5rem', marginBottom: '0.5rem' }}>←</div>
                <p style={{ color: '#64748b', fontSize: '0.9rem' }}>
                  Select a pipeline to view its promotion DAG.
                </p>
              </>
            ) : (
              <>
                <p style={{ color: '#64748b', fontSize: '0.9rem', marginBottom: '0.75rem' }}>
                  No pipelines found. Apply a Pipeline to get started:
                </p>
                <code style={{
                  display: 'block',
                  background: '#1e293b',
                  border: '1px solid #334155',
                  borderRadius: '6px',
                  padding: '0.6rem 1rem',
                  fontSize: '0.8rem',
                  color: '#7dd3fc',
                  fontFamily: 'monospace',
                  textAlign: 'left',
                  maxWidth: '480px',
                  margin: '0 auto',
                }}>
                  kubectl apply -f examples/quickstart/pipeline.yaml
                </code>
              </>
            )}
          </div>
        ) : (
          <>
            {/* Header area (fixed height) */}
            <div style={{ padding: '1.5rem 1.5rem 0', flexShrink: 0 }}>
              {/* Pipeline header */}
              <div style={{ marginBottom: '1rem' }}>
                <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', marginBottom: '0.25rem' }}>
                  <h1 style={{ fontSize: '1.25rem', fontWeight: 700, margin: 0 }}>
                    {activePipeline?.name}
                  </h1>
                  {/* Paused banner in main panel (#328) */}
                  {activePipeline?.paused && (
                    <span style={{
                      fontSize: '0.7rem',
                      background: '#1e1b4b',
                      color: '#a5b4fc',
                      border: '1px solid #4338ca',
                      borderRadius: '4px',
                      padding: '2px 8px',
                      fontWeight: 700,
                      letterSpacing: '0.05em',
                    }}>
                      ⏸ PAUSED — no new promotions
                    </span>
                  )}
                </div>
                {/* Bundle provenance card (#329) */}
                {activeBundle && (
                  <div style={{
                    display: 'flex',
                    flexWrap: 'wrap',
                    gap: '0.5rem',
                    alignItems: 'center',
                    fontSize: '0.82rem',
                    color: '#94a3b8',
                  }}>
                    <span>
                      Bundle: <span style={{ color: '#7dd3fc', fontFamily: 'monospace' }}>{activeBundle.name}</span>
                    </span>
                    <span style={{ color: '#334155' }}>·</span>
                    <HealthChip state={activeBundle.phase} size="sm" />
                    {activeBundle.provenance?.commitSHA && (
                      <>
                        <span style={{ color: '#334155' }}>·</span>
                        <span style={{ fontFamily: 'monospace', color: '#64748b' }}
                              title="Commit SHA">
                          {activeBundle.provenance.commitSHA.slice(0, 8)}
                        </span>
                      </>
                    )}
                    {activeBundle.provenance?.author && (
                      <>
                        <span style={{ color: '#334155' }}>·</span>
                        <span title="Author">{activeBundle.provenance.author}</span>
                      </>
                    )}
                    {activeBundle.provenance?.ciRunURL && (
                      <>
                        <span style={{ color: '#334155' }}>·</span>
                        <a
                          href={activeBundle.provenance.ciRunURL}
                          target="_blank"
                          rel="noopener noreferrer"
                          style={{ color: '#6366f1', fontSize: '0.78rem' }}
                          title="CI run"
                        >
                          CI run ↗
                        </a>
                      </>
                    )}
                  </div>
                )}
              </div>

              {/* Blocked PolicyGate banner */}
              <BlockedBanner
                blockedCount={blockedGateIds.size}
                highlightActive={showBlockedOnly}
                onToggleHighlight={() => setShowBlockedOnly(v => !v)}
              />

              {/* #340: PolicyGates panel — shows all active gates with CEL expressions */}
              <PolicyGatesPanel gates={gates} loading={gatesLoading} />

              {/* Bundle history (collapsible) */}
              {bundles.length > 0 && (
                <div style={{ marginBottom: '1rem' }}>
                  <button
                    onClick={() => setBundleHistoryOpen(o => !o)}
                    style={{
                      background: 'none',
                      border: 'none',
                      color: '#6366f1',
                      cursor: 'pointer',
                      fontSize: '0.8rem',
                      padding: '0.25rem 0',
                      fontWeight: 600,
                    }}
                    aria-expanded={bundleHistoryOpen}
                  >
                    {bundleHistoryOpen ? '▾' : '▸'} Bundle history ({bundles.length})
                  </button>
                  {bundleHistoryOpen && (
                    <ul style={{
                      listStyle: 'none',
                      padding: '0.5rem 0',
                      margin: 0,
                      borderLeft: '2px solid #1e293b',
                      paddingLeft: '0.75rem',
                    }}>
                      {bundles.map(b => (
                        <li key={b.name} style={{
                          display: 'flex',
                          alignItems: 'center',
                          gap: '0.5rem',
                          marginBottom: '0.35rem',
                          fontSize: '0.8rem',
                          color: '#cbd5e1',
                        }}>
                          <HealthChip state={b.phase} size="sm" />
                          <span style={{ fontFamily: 'monospace', color: '#e2e8f0' }}>{b.name}</span>
                          {b.provenance?.commitSHA && (
                            <span style={{ color: '#64748b', fontFamily: 'monospace' }}>
                              {b.provenance.commitSHA.slice(0, 8)}
                            </span>
                          )}
                        </li>
                      ))}
                    </ul>
                  )}
                </div>
              )}

              {/* Bundle Timeline — horizontal strip showing bundle history (Kargo freight timeline parity).
                  Receives bundles from parent state — no independent fetch (#321). */}
              <BundleTimeline
                bundles={bundles}
                selectedBundle={activeBundle?.name}
                onSelectBundle={handleTimelineBundleSelect}
                compareBundle={compareBundle}
                onCompareBundle={(name) => {
                  setCompareBundle(name ?? undefined)
                  if (!name) setShowDiffPanel(false)
                }}
                onCompare={() => setShowDiffPanel(true)}
              />
            </div>

            {/* #338: Bundle diff panel — shows when two bundles are selected for comparison */}
            {showDiffPanel && compareBundle && activeBundle && (
              (() => {
                const compareBundleObj = bundles.find(b => b.name === compareBundle)
                if (!compareBundleObj) return null
                return (
                  <BundleDiffPanel
                    bundleA={activeBundle}
                    bundleB={compareBundleObj}
                    onClose={() => { setShowDiffPanel(false); setCompareBundle(undefined) }}
                  />
                )
              })()
            )}

            {/* #332: Pipeline lane view — horizontal stage cards (Kargo-parity).
                Shows each PromotionStep environment as a card with state chip, bundle, and actions. */}
            <PipelineLaneView
              nodes={graph?.nodes ?? []}
              selectedNode={selectedNode}
              onSelectNode={setSelectedNode}
              activeBundleName={activeBundle?.name}
              pipelineName={selectedPipeline ?? undefined}
              onPromote={(environment) => {
                if (!selectedPipeline || !activePipeline) return
                api.promote(selectedPipeline, environment, activePipeline.namespace ?? 'default')
                  .catch((err: Error) => console.error('promote failed:', err.message))
              }}
              onRollback={(environment) => {
                if (!selectedPipeline || !activePipeline) return
                api.rollback(selectedPipeline, environment, activePipeline.namespace ?? 'default')
                  .catch((err: Error) => console.error('rollback failed:', err.message))
              }}
              loading={graphLoading}
            />

            {/* #326: Content row — DAG + NodeDetail split panel side by side.
                NodeDetail is a flex sibling, not position:fixed overlay. */}
            <div style={{ flex: 1, display: 'flex', overflow: 'hidden', padding: '0 1.5rem 1.5rem' }}>
              {/* DAG area */}
              <div style={{
                flex: 1,
                background: '#1e293b',
                borderRadius: selectedNode ? '8px 0 0 8px' : '8px',
                padding: '1rem',
                minHeight: '300px',
                overflow: 'auto',
              }}>
                <DAGView
                  nodes={graph?.nodes ?? []}
                  edges={graph?.edges ?? []}
                  loading={graphLoading}
                  error={graphError}
                  highlightNodeIds={highlightIds}
                  selectedNode={selectedNode}
                  onSelectNode={setSelectedNode}
                />
              </div>

              {/* NodeDetail split panel (#326) — sibling of DAGView, not overlay */}
              {selectedNode && (
                <NodeDetail
                  node={selectedNode}
                  onClose={() => setSelectedNode(null)}
                  bundleName={activeBundle?.name}
                  pipelineName={selectedPipeline}
                  namespace={activePipeline?.namespace ?? 'default'}
                  steps={activeSteps}
                />
              )}
            </div>
          </>
        )}
      </main>
    </div>
  )
}
