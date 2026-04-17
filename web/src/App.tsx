// App.tsx — Root component with Pipeline list sidebar and DAG view.
// #326: selectedNode is lifted here so NodeDetail renders as a split panel
// sibling of DAGView rather than a position:fixed overlay.
// #740: URL routing — pipeline and node selection persisted in hash fragment.
import { useState, useCallback, useMemo, useRef, useEffect } from 'react'
import { PipelineList } from './components/PipelineList'
import { PipelineOpsTable } from './components/PipelineOpsTable'
import { DAGView } from './components/DAGView'
import { NodeDetail } from './components/NodeDetail'
import { HealthChip } from './components/HealthChip'
import { BlockedBanner } from './components/BlockedBanner'
import { BundleTimeline } from './components/BundleTimeline'
import { BundleDiffPanel } from './components/BundleDiffPanel'
import { PolicyGatesPanel } from './components/PolicyGatesPanel'
import { PipelineLaneView } from './components/PipelineLaneView'
import { FleetHealthBar, filterPipelines, type FleetFilter } from './components/FleetHealthBar'
import { ReleaseMetricsBar } from './components/ReleaseMetricsBar'
import { ActionBar } from './components/ActionBar'
import EmptyState from './components/EmptyState'
import PromotionErrorsPanel from './components/PromotionErrorsPanel'
import CopyButton from './components/CopyButton'
import { api } from './api/client'
import { usePolling } from './usePolling'
import { useRefreshIndicator } from './useRefreshIndicator'
import { useTheme } from './ThemeContext'
import { useUrlState } from './useUrlState'
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
  const { theme, toggleTheme } = useTheme()
  // #740: URL routing — pipeline and node selection backed by hash fragment.
  const [urlState, setUrlState] = useUrlState()
  const [pipelines, setPipelines] = useState<Pipeline[]>([])
  const [pipelinesLoading, setPipelinesLoading] = useState(true)
  const [pipelinesError, setPipelinesError] = useState<string | undefined>()

  const [selectedPipeline, setSelectedPipeline] = [
    urlState.pipeline,
    (name: string | undefined) => setUrlState({ pipeline: name }),
  ] as const
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
  // #740: selectedNode ID is persisted in URL hash (node= param); full GraphNode object in local state.
  const [selectedNode, setSelectedNodeLocal] = useState<GraphNode | null>(null)

  /** Update both local state and URL hash when a node is selected. */
  const setSelectedNode = useCallback((node: GraphNode | null) => {
    setSelectedNodeLocal(node)
    setUrlState({ node: node?.id ?? undefined })
  }, [setUrlState])

  // #740: When graph data changes, restore selectedNode from URL if a node= param is present
  // and the current selectedNode doesn't already match.
  useEffect(() => {
    const nodeId = urlState.node
    if (!nodeId || !graph) return
    if (selectedNode?.id === nodeId) return
    const node = graph.nodes?.find(n => n.id === nodeId)
    if (node) setSelectedNodeLocal(node)
  }, [graph, urlState.node, selectedNode?.id])

  // #338: Bundle diff comparison state.
  // #740: compareBundle and showDiffPanel are derived from URL bundle= param.
  const compareBundle = urlState.bundle
  const showDiffPanel = !!urlState.bundle
  const setCompareBundle = useCallback((name: string | undefined) => {
    setUrlState({ bundle: name })
  }, [setUrlState])
  const setShowDiffPanel = useCallback((show: boolean) => {
    if (!show) setUrlState({ bundle: undefined })
  }, [setUrlState])

  // Refresh indicator: tracks last successful poll for the staleness indicator.
  const { elapsedSeconds, onSuccess: onPollSuccess } = useRefreshIndicator()

  // Blocked gate filter state.
  const [showBlockedOnly, setShowBlockedOnly] = useState(false)

  // #462: view mode toggle — 'list' (sidebar) | 'ops-table' (full-width operations table).
  const [viewMode, setViewMode] = useState<'list' | 'ops-table'>('list')

  // #505: Fleet filter — drives which pipelines are visible in the sidebar list.
  const [fleetFilter, setFleetFilter] = useState<FleetFilter>('all')
  const filteredPipelines = filterPipelines(pipelines, fleetFilter)

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
      // #522: mark poll success so the header staleness indicator clears "Loading..."
      onPollSuccess()
    } catch (e) {
      setPipelinesError(String(e))
    } finally {
      setPipelinesLoading(false)
    }
  }, [onPollSuccess])

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
    : 'var(--color-text-secondary)'  // WCAG AA compliant; was #64748b which fails at small font sizes

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

  // #525: Build a static topology graph from Pipeline.environmentTopology when no
  // active bundle graph is available. This ensures the DAG always renders the
  // pipeline structure even when nothing is currently promoting.
  const staticGraph = useMemo<GraphResponse | undefined>(() => {
    const topo = activePipeline?.environmentTopology
    if (!topo || topo.length === 0) return undefined
    const nodes: GraphNode[] = topo.map(env => ({
      id: env.name,
      type: 'PromotionStep' as const,
      label: env.name,
      environment: env.name,
      state: 'Idle',
      message: env.approval === 'pr-review' ? 'Manual approval required' : undefined,
    }))
    // Build edges: if dependsOn is set, draw edges from each dependency; otherwise
    // draw sequential edges (previous → current) for environments without dependsOn.
    const edges: { from: string; to: string }[] = []
    for (let i = 0; i < topo.length; i++) {
      const env = topo[i]
      if (env.dependsOn && env.dependsOn.length > 0) {
        for (const dep of env.dependsOn) {
          edges.push({ from: dep, to: env.name })
        }
      } else if (i > 0) {
        // No explicit dependsOn: assume sequential after the previous environment
        // that also has no explicit dependsOn. Matches default Pipeline ordering.
        const prev = topo[i - 1]
        if (!prev.dependsOn || prev.dependsOn.length === 0) {
          edges.push({ from: prev.name, to: env.name })
        }
      }
    }
    return { nodes, edges }
  }, [activePipeline?.environmentTopology])

  // Use the bundle graph when available; fall back to static topology (#525).
  const displayGraph = graph ?? staticGraph

  return (
    <div style={{ display: 'flex', height: '100vh', overflow: 'hidden', background: 'var(--color-bg)', color: 'var(--color-text)' }}>
      {/* Sidebar */}
      <aside style={{
        width: '240px',
        minWidth: '200px',
        background: 'var(--color-bg)',
        borderRight: '1px solid var(--color-border-muted)',
        display: 'flex',
        flexDirection: 'column',
        overflow: 'hidden',
      }}>
        <div style={{
          padding: '1rem',
          borderBottom: '1px solid var(--color-border-muted)',
          display: 'flex',
          justifyContent: 'space-between',
          alignItems: 'center',
        }}>
          {/* Brand: logo + wordmark */}
          <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem' }}>
            <img
              src="/logo.png"
              alt="Kardinal"
              style={{ width: '32px', height: '32px', objectFit: 'contain' }}
            />
            <span style={{
              fontWeight: 700,
              fontSize: '0.9rem',
              color: 'var(--color-text)',
              letterSpacing: '0.05em',
            }}>
              KARDINAL
            </span>
          </div>
          {/* Staleness indicator with manual refresh button (#362) */}
          <div style={{ display: 'flex', alignItems: 'center', gap: '6px' }}>
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
            <span style={{ fontSize: '0.6rem', color: 'var(--color-text-faint)' }} title="Click to refresh">↺</span>
          </button>
          {/* Theme toggle button (#722) */}
          <button
            onClick={toggleTheme}
            title={theme === 'dark' ? 'Switch to light mode' : 'Switch to dark mode'}
            aria-label={theme === 'dark' ? 'Switch to light mode' : 'Switch to dark mode'}
            style={{
              background: 'none',
              border: '1px solid var(--color-border)',
              borderRadius: '4px',
              cursor: 'pointer',
              fontSize: '0.7rem',
              padding: '1px 4px',
              color: 'var(--color-text-muted)',
              lineHeight: 1,
            }}
          >
            {theme === 'dark' ? '☀' : '☾'}
          </button>
          </div>
        </div>
        <div style={{ padding: '0.75rem 1rem', fontSize: '0.75rem', color: 'var(--color-text-secondary)', fontWeight: 600, display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
          <span>PIPELINES</span>
          <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem' }}>
            {/* #462: view mode toggle — list sidebar vs ops table */}
            <button
              onClick={() => setViewMode(v => v === 'list' ? 'ops-table' : 'list')}
              title={viewMode === 'list' ? 'Switch to Operations Table' : 'Switch to List View'}
              aria-label={viewMode === 'list' ? 'Switch to Operations Table' : 'Switch to List View'}
              style={{
                background: viewMode === 'ops-table' ? 'var(--color-surface)' : 'none',
                border: '1px solid ' + (viewMode === 'ops-table' ? 'var(--color-accent)' : 'var(--color-border)'),
                borderRadius: '4px',
                cursor: 'pointer',
                padding: '1px 5px',
                fontSize: '0.65rem',
                color: viewMode === 'ops-table' ? 'var(--color-accent)' : 'var(--color-text-secondary)',
              }}
            >
              {viewMode === 'list' ? '⊞ Ops' : '☰ List'}
            </button>
            {currentNamespace && (
              <span style={{
                fontSize: '0.65rem',
                color: 'var(--color-text-secondary)',
                background: 'var(--color-surface)',
                borderRadius: '4px',
                padding: '1px 5px',
                fontWeight: 400,
                fontFamily: 'monospace',
              }} title={`Namespace: ${currentNamespace}`}>
                {currentNamespace}
              </span>
            )}
          </div>
        </div>
        <div style={{ overflowY: 'auto', flex: 1 }}>
          {/* #505: Fleet health bar — above pipeline list in sidebar */}
          {pipelines.length > 0 && (
            <div style={{ padding: '0.5rem 0.75rem 0' }}>
              <FleetHealthBar
                pipelines={pipelines}
                activeFilter={fleetFilter}
                onFilterChange={setFleetFilter}
              />
            </div>
          )}
          <PipelineList
            pipelines={filteredPipelines}
            selected={selectedPipeline}
            onSelect={name => { handleSelectPipeline(name); if (viewMode === 'ops-table') setViewMode('list') }}
            loading={pipelinesLoading}
            error={pipelinesError}
          />
        </div>
      </aside>

      {/* Ops table mode — full-width table replaces the main content area */}
      {viewMode === 'ops-table' ? (
        <main style={{ flex: 1, overflow: 'hidden', display: 'flex', flexDirection: 'column', background: 'var(--color-bg-deep)' }}>
          <PipelineOpsTable
            pipelines={pipelines}
            selected={selectedPipeline}
            onSelect={name => { handleSelectPipeline(name); setViewMode('list') }}
            loading={pipelinesLoading}
            error={pipelinesError}
          />
        </main>
      ) : (
        <>{/* Main area — column layout for header + content row */}
        <main style={{ flex: 1, overflow: 'hidden', display: 'flex', flexDirection: 'column', background: 'var(--color-bg)' }}>
        {!selectedPipeline ? (
          <div style={{ color: 'var(--color-text-faint)', padding: '3rem 2rem', textAlign: 'center' }}>
            {pipelines.length > 0 ? (
              <>
                <div style={{ fontSize: '1.5rem', marginBottom: '0.5rem' }}>←</div>
                <p style={{ color: '#64748b', fontSize: '0.9rem' }}>
                  Select a pipeline to view its promotion DAG.
                </p>
              </>
            ) : (
              /* #530: Improved empty state with copy button, docs link, expected output */
              <EmptyState />
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
                      color: 'var(--color-accent)',
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
                    color: 'var(--color-text-muted)',
                  }}>
                    <span>
                      Bundle: <span style={{ color: '#7dd3fc', fontFamily: 'monospace' }}>{activeBundle.name}</span>
                      {/* #763: copy bundle name */}
                      <CopyButton text={activeBundle.name} title={`Copy bundle name "${activeBundle.name}"`} />
                    </span>
                    <span style={{ color: 'var(--color-border)' }}>·</span>
                    <HealthChip state={activeBundle.phase} size="sm" />
                    {activeBundle.provenance?.commitSHA && (
                      <>
                        <span style={{ color: 'var(--color-border)' }}>·</span>
                        <span style={{ fontFamily: 'monospace', color: 'var(--color-text-muted)' }}
                              title="Commit SHA">
                          {activeBundle.provenance.commitSHA.slice(0, 8)}
                        </span>
                        {/* #763: copy full commit SHA */}
                        <CopyButton text={activeBundle.provenance.commitSHA} title="Copy commit SHA" />
                      </>
                    )}
                    {activeBundle.provenance?.author && (
                      <>
                        <span style={{ color: 'var(--color-border)' }}>·</span>
                        <span title="Author">{activeBundle.provenance.author}</span>
                      </>
                    )}
                    {activeBundle.provenance?.ciRunURL && (
                      <>
                        <span style={{ color: 'var(--color-border)' }}>·</span>
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

              {/* #506: ActionBar — pause/resume buttons for the selected pipeline. */}
              {activePipeline && (
                <ActionBar
                  pipelineName={activePipeline.name}
                  namespace={activePipeline.namespace ?? 'default'}
                  paused={activePipeline.paused ?? false}
                  onRefresh={manualRefresh}
                />
              )}

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
                      borderLeft: '2px solid var(--color-border-muted)',
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
                          <span style={{ fontFamily: 'monospace', color: 'var(--color-text)' }}>{b.name}</span>
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

              {/* #504: Release efficiency metrics bar — inline metrics for the pipeline. */}
              <div style={{ marginBottom: '1rem' }}>
                <ReleaseMetricsBar bundles={bundles} />
              </div>

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
            {/* #528: Cross-environment error aggregation — shown above lane view when steps fail */}
            <PromotionErrorsPanel
              steps={activeSteps}
              onSelectEnvironment={(env) => {
                const node = graph?.nodes.find(n => n.environment === env && n.type === 'PromotionStep')
                if (node) setSelectedNode(node)
              }}
            />
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
                background: 'var(--color-surface)',
                borderRadius: selectedNode ? '8px 0 0 8px' : '8px',
                padding: '1rem',
                minHeight: '300px',
                overflow: 'auto',
              }}>
                <DAGView
                  nodes={displayGraph?.nodes ?? []}
                  edges={displayGraph?.edges ?? []}
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
                  activeBundle={activeBundle}
                />
              )}
            </div>
          </>
        )}
      </main>
        </>
      )}
    </div>
  )
}
