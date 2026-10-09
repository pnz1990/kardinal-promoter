// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// App.tsx — Root component with Pipeline list sidebar and DAG view.
//
// Which bundle is on screen: the one the user picked in the timeline, for as
// long as it exists; otherwise the pipeline's active bundle (resolveShownBundle).
// Polls never override the user's pick.
// A pipeline is identified by namespace + name, so same-named pipelines in two
// namespaces never share a header, bundle list or DAG.
// Every load carries a view sequence number; a response that arrives after the
// user moved to another pipeline or bundle is dropped.
// #326: selectedNode is lifted here so NodeDetail renders as a split panel
// sibling of DAGView rather than a position:fixed overlay.
// #740: URL routing — pipeline and node selection persisted in hash fragment.
// #746: Global keyboard shortcuts — ?, r, Esc.
// #747: ErrorBoundary wraps async components to prevent white-screen crashes.
// #800: / shortcut to focus pipeline search input.
import { useState, useCallback, useMemo, useRef, useEffect } from 'react'
import { PipelineList } from './components/PipelineList'
import { PipelineOpsTable } from './components/PipelineOpsTable'
import { DAGView } from './components/DAGView'
import { NodeDetail } from './components/NodeDetail'
import { HealthChip } from './components/HealthChip'
import { RejectedLiveBanner } from './components/RejectedLiveBanner'
import { BlockedBanner } from './components/BlockedBanner'
import { InsecureConnectionBanner } from './components/InsecureConnectionBanner'
import { BundleTimeline } from './components/BundleTimeline'
import { BundleDiffPanel } from './components/BundleDiffPanel'
import { BundleTypeBadge } from './components/BundleTypeBadge'
import { PolicyGatesPanel } from './components/PolicyGatesPanel'
import { PipelineLaneView } from './components/PipelineLaneView'
import { FleetHealthBar, filterPipelines, type FleetFilter } from './components/FleetHealthBar'
import { FleetBoard } from './components/FleetBoard'
import { ReleaseMetricsBar } from './components/ReleaseMetricsBar'
import { ActionBar } from './components/ActionBar'
import { CreateBundleButton } from './components/CreateBundleDialog'
import EmptyState from './components/EmptyState'
import PromotionErrorsPanel from './components/PromotionErrorsPanel'
import CopyButton from './components/CopyButton'
import { formatRelativeAge } from './components/approvalAge'
import { CIRunLink } from './components/CIRunLink'
import { ErrorBoundary } from './components/ErrorBoundary'
import { api } from './api/client'
import { usePolling } from './usePolling'
import { useRefreshIndicator } from './useRefreshIndicator'
import { useTheme } from './ThemeContext'
import { useUrlState } from './useUrlState'
import { useKeyboardShortcuts } from './useKeyboardShortcuts'
import { resolveShownBundle } from './bundleSelection'
import { KeyboardShortcutsPanel } from './components/KeyboardShortcutsPanel'

import type { Pipeline, Bundle, GraphNode, GraphResponse, PromotionStep, PolicyGate } from './types'

const POLL_INTERVAL_MS = 5000

/** The message of a failed read. The views print it after "Error: ", so it
 *  must not start with one (String(new Error(m)) is "Error: m"). */
function errorText(e: unknown): string {
  return e instanceof Error ? e.message : String(e)
}

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

  const selectedPipeline = urlState.pipeline
  const [bundles, setBundles] = useState<Bundle[]>([])
  const [bundlesLoading, setBundlesLoading] = useState(false)
  const [bundleHistoryOpen, setBundleHistoryOpen] = useState(false)
  // Name of the bundle whose graph is on screen.
  const [shownBundleName, setShownBundleName] = useState<string | undefined>()

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
  // and the current selectedNode doesn't already match. With no node= (Back to an entry
  // before the node was opened), the details close.
  useEffect(() => {
    const nodeId = urlState.node
    if (!nodeId) {
      if (selectedNode?.id) setSelectedNodeLocal(null)
      return
    }
    if (!graph) return
    if (selectedNode?.id === nodeId) return
    const node = graph.nodes?.find(n => n.id === nodeId)
    if (node) setSelectedNodeLocal(node)
  }, [graph, urlState.node, selectedNode?.id])

  // #338: Bundle diff comparison state. Shift-click picks the bundle to
  // compare (compareTarget); the timeline then offers Compare and clear.
  // #740: the comparison is open while the URL has bundle=, so a link opens it.
  // Closing it keeps the pick, so Compare and clear are where focus returns.
  const [compareTarget, setCompareTarget] = useState<string | undefined>()
  const showDiffPanel = !!urlState.bundle
  const closeDiffPanel = useCallback(() => {
    setUrlState({ bundle: undefined })
  }, [setUrlState])

  // Refresh indicator: tracks last successful poll for the staleness indicator.
  const { elapsedSeconds, onSuccess: onPollSuccess } = useRefreshIndicator()

  // Blocked gate filter state.
  const [showBlockedOnly, setShowBlockedOnly] = useState(false)

  // #913: insecure connection banner — dismissed state for the current session.
  const [insecureBannerDismissed, setInsecureBannerDismissed] = useState(false)

  // #462: view mode toggle — 'list' (sidebar) | 'ops-table' (full-width operations table).
  const [viewMode, setViewMode] = useState<'list' | 'ops-table'>('list')

  // #505: Fleet filter — drives which pipelines are visible in the sidebar list.
  const [fleetFilter, setFleetFilter] = useState<FleetFilter>('all')
  const filteredPipelines = filterPipelines(pipelines, fleetFilter)

  // #746: Keyboard shortcuts help panel visibility.
  const [showShortcutsPanel, setShowShortcutsPanel] = useState(false)

  // #800: Ref for the pipeline search input — used by the / keyboard shortcut.
  const searchInputRef = useRef<HTMLInputElement>(null)

  // Latest pipeline list, the user's timeline pick, and the view sequence
  // number (bumped on every pipeline or bundle switch) for the loaders below.
  const pipelinesRef = useRef<Pipeline[]>([])
  const userBundleRef = useRef<string | undefined>(undefined)
  const viewSeq = useRef(0)
  const inflight = useRef<{ seq: number; promise: Promise<void> } | null>(null)
  const track = (seq: number, promise: Promise<void>): Promise<void> => {
    const entry = { seq, promise }
    inflight.current = entry
    return promise.finally(() => { if (inflight.current === entry) inflight.current = null })
  }

  // Shared fetch function — called by both interval poll and manual refresh.
  const doFetchAll = useCallback(async () => {
    try {
      const [ps, gs] = await Promise.all([
        api.listPipelines(),
        api.listGates().catch(() => [] as PolicyGate[]),
      ])
      pipelinesRef.current = ps
      setPipelines(ps)
      setGates(gs)
      setGatesLoading(false)
      setPipelinesError(undefined)
      // #522: mark poll success so the header staleness indicator clears "Loading..."
      onPollSuccess()
    } catch (e) {
      setPipelinesError(errorText(e))
    } finally {
      setPipelinesLoading(false)
    }
  }, [onPollSuccess])

  // Load bundles, then the graph and steps of the bundle to show, for one
  // pipeline. seq is the view the load belongs to; results for an older view
  // are dropped.
  const loadPipeline = useCallback((name: string, ns: string | undefined, seq: number): Promise<void> => {
    const stale = () => seq !== viewSeq.current
    const run = async () => {
      try {
        const nsEff = ns ?? pipelinesRef.current.find(p => p.name === name)?.namespace
        const listed = await api.listBundles(name, nsEff)
        if (stale()) return
        const bs = nsEff ? listed.filter(b => b.namespace === nsEff) : listed
        if (userBundleRef.current && !bs.some(b => b.name === userBundleRef.current)) {
          userBundleRef.current = undefined // the picked bundle is gone
        }
        const pl = pipelinesRef.current.find(p => p.name === name && (!nsEff || p.namespace === nsEff))
        const shown = resolveShownBundle(bs, userBundleRef.current, pl?.activeBundleName)
        if (!shown) {
          setBundles(bs)
          setBundlesLoading(false)
          setShownBundleName(undefined)
          setGraph(undefined)
          setActiveSteps([])
          setGraphError(undefined)
          return
        }
        try {
          // The bundle's own namespace: its name may repeat in other namespaces.
          const [g, steps] = await Promise.all([
            api.getGraph(shown.name, shown.namespace),
            api.getSteps(shown.name, shown.namespace),
          ])
          if (stale()) return
          setGraph(g)
          setActiveSteps(steps)
          setGraphError(undefined)
        } finally {
          if (!stale()) {
            setBundles(bs)
            setBundlesLoading(false)
            setShownBundleName(shown.name)
          }
        }
      } catch (e) {
        if (!stale()) {
          setGraphError(errorText(e))
          setBundlesLoading(false)
        }
      } finally {
        if (!stale()) setGraphLoading(false)
      }
    }
    return track(seq, run())
  }, [])

  // Load the selected pipeline whenever it changes (list click, Back/forward,
  // deep link). Everything tied to the previous pipeline is cleared first.
  useEffect(() => {
    const seq = ++viewSeq.current
    userBundleRef.current = undefined
    setShownBundleName(undefined)
    setGraph(undefined)
    setGraphError(undefined)
    setActiveSteps([])
    setBundles([])
    setBundleHistoryOpen(false)
    setShowBlockedOnly(false)
    setSelectedNodeLocal(null)
    setCompareTarget(undefined)
    if (!selectedPipeline) {
      setGraphLoading(false)
      setBundlesLoading(false)
      return
    }
    setGraphLoading(true)
    setBundlesLoading(true)
    void loadPipeline(selectedPipeline, urlState.ns, seq)
  }, [selectedPipeline, urlState.ns, loadPipeline])

  // An open comparison (bundle=, from Compare, a link or Back) is also the
  // pick. This runs after the reset above, so a link that names a pipeline
  // and a bundle= keeps the pick when the comparison closes.
  useEffect(() => {
    if (urlState.bundle) setCompareTarget(urlState.bundle)
  }, [urlState.bundle, selectedPipeline, urlState.ns])

  // Poll pipeline list every 5 seconds.
  usePolling(doFetchAll, POLL_INTERVAL_MS)

  // Refresh bundles + graph + steps for the selected pipeline every 5 seconds.
  // Single poll callback — no independent sub-polls in children (#321, #322, #324).
  // A tick is skipped while a load for the current view is still running.
  const viewRef = useRef({ pipeline: selectedPipeline, ns: urlState.ns })
  viewRef.current = { pipeline: selectedPipeline, ns: urlState.ns }
  usePolling(() => {
    const { pipeline, ns } = viewRef.current
    if (!pipeline || inflight.current?.seq === viewSeq.current) return
    void loadPipeline(pipeline, ns, viewSeq.current)
  }, POLL_INTERVAL_MS, !!selectedPipeline)

  // Manual refresh (#362): re-fetch everything immediately on demand.
  const manualRefresh = useCallback(async () => {
    await doFetchAll()
    const { pipeline, ns } = viewRef.current
    if (pipeline) await loadPipeline(pipeline, ns, ++viewSeq.current)
  }, [doFetchAll, loadPipeline])

  // #746: Global keyboard shortcuts — ?, r, Esc.
  useKeyboardShortcuts({
    onHelp: useCallback(() => setShowShortcutsPanel(p => !p), []),
    onRefresh: useCallback(() => { void manualRefresh() }, [manualRefresh]),
    onEscape: useCallback(() => {
      if (showShortcutsPanel) { setShowShortcutsPanel(false); return }
      if (selectedNode) { setSelectedNode(null); return }
      if (showDiffPanel) { closeDiffPanel(); return }
    }, [showShortcutsPanel, selectedNode, showDiffPanel, setSelectedNode, closeDiffPanel]),
    // #800: / focuses the pipeline search input
    onSearch: useCallback(() => {
      searchInputRef.current?.focus()
    }, []),
  })

  // One history entry per switch: the new pipeline, with no node or diff open.
  // The load effect above does the fetching.
  const handleSelectPipeline = useCallback((name: string, namespace: string) => {
    setSelectedNodeLocal(null)
    setUrlState({ pipeline: name, ns: namespace, node: undefined, bundle: undefined })
  }, [setUrlState])

  const activePipeline = selectedPipeline
    ? pipelines.find(p => p.name === selectedPipeline && (!urlState.ns || p.namespace === urlState.ns))
    : undefined
  const selectedNamespace = urlState.ns ?? activePipeline?.namespace
  const activeBundle = shownBundleName ? bundles.find(b => b.name === shownBundleName) : undefined
  // The picked comparison bundle, while the pipeline still has it and it is
  // not the bundle on screen (which is the one it is compared against).
  const compareBundle = compareTarget && compareTarget !== activeBundle?.name && bundles.some(b => b.name === compareTarget)
    ? compareTarget
    : undefined

  // Handler for timeline bundle selection — shows that bundle until the user
  // picks another one or it disappears.
  const handleTimelineBundleSelect = useCallback((bundleName: string) => {
    const seq = ++viewSeq.current
    const stale = () => seq !== viewSeq.current
    // The timeline lists the selected pipeline's bundles, so the picked one is
    // in that pipeline's namespace.
    const namespace = bundles.find(b => b.name === bundleName)?.namespace ?? selectedNamespace
    userBundleRef.current = bundleName
    setShownBundleName(bundleName)
    // Showing the comparison bundle makes it the one compared against.
    setCompareTarget(t => t === bundleName ? undefined : t)
    setGraphLoading(true)
    setGraphError(undefined)
    setSelectedNode(null) // close detail panel when switching bundles
    void track(seq, Promise.all([api.getGraph(bundleName, namespace), api.getSteps(bundleName, namespace)])
      .then(([g, steps]) => {
        if (stale()) return
        setGraph(g)
        setActiveSteps(steps)
      })
      .catch(e => { if (!stale()) setGraphError(errorText(e)) })
      .finally(() => { if (!stale()) setGraphLoading(false) }))
  }, [setSelectedNode, bundles, selectedNamespace])

  // Namespace chip: the selected pipeline's namespace, or the only namespace.
  const currentNamespace = activePipeline?.namespace
    ?? (new Set(pipelines.map(p => p.namespace)).size === 1 ? pipelines[0].namespace : undefined)

  // Gates of the bundle on screen; templates are never evaluated, so they are left out.
  const shownGates = useMemo(
    () => activeBundle
      ? gates.filter(g => !g.template && g.bundle === activeBundle.name && g.namespace === activeBundle.namespace)
      : [],
    [gates, activeBundle],
  )

  // Determine staleness indicator color — escalates at 15s (amber) and 30s (red).
  // A failing poll is what makes data that old, so the error does not hold it amber.
  const staleness = elapsedSeconds ?? 0
  const indicatorColor = staleness > 30
    ? 'var(--color-error)'     // red when critically stale > 30s (#766)
    : pipelinesError || staleness > 15
    ? 'var(--color-warning)'   // amber on error, or when stale > 15s
    : 'var(--color-text-secondary)'

  // PolicyGate nodes that hold the bundle back. The UI API decides (holding),
  // with the rule the sidebar's blockerCount uses; a not-ready gate the bundle
  // has not reached is Waiting and not counted (E2E-R19).
  const blockedGateIds = useMemo<Set<string>>(() => {
    if (!graph) return new Set()
    const ids = new Set<string>()
    for (const node of graph.nodes) {
      if (node.type === 'PolicyGate' && node.holding) {
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
      state: 'NotStarted',
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

  // #913: Insecure connection warning — plain HTTP from a non-loopback address.
  // It heads every main view, so it shows before any pipeline loads (the API
  // may refuse the page's requests outright).
  const insecureBanner = (
    <InsecureConnectionBanner
      dismissed={insecureBannerDismissed}
      onDismiss={() => setInsecureBannerDismissed(true)}
      margin="1rem 1.5rem 0"
    />
  )

  return (
    <div style={{ display: 'flex', height: '100vh', overflow: 'hidden', background: 'var(--color-bg)', color: 'var(--color-text)' }}>
      {/* The first Tab stop: past the sidebar to the main content. */}
      <a className="skip-link" href="#main-content"
        onClick={e => { e.preventDefault(); document.getElementById('main-content')?.focus() }}>
        Skip to main content
      </a>
      {/* #746: Keyboard shortcuts help panel — rendered at root level so it overlays all content. */}
      {showShortcutsPanel && (
        <KeyboardShortcutsPanel onClose={() => setShowShortcutsPanel(false)} />
      )}
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
          {/* Brand: logo + wordmark; it opens the fleet board. */}
          <button
            type="button"
            onClick={() => { setSelectedNodeLocal(null); setUrlState({ pipeline: undefined, ns: undefined, node: undefined, bundle: undefined }); setViewMode('list') }}
            title="Show the fleet"
            aria-label="Show the fleet"
            style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', background: 'none', border: 'none', padding: 0, cursor: 'pointer', borderRadius: '4px' }}
          >
            <img
              src={`${import.meta.env.BASE_URL}logo.png`}
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
          </button>
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
              title={pipelinesError ? `Error: ${pipelinesError}` : staleness > 30 ? 'Data is stale — click to refresh' : 'Last updated'}
              style={{
                fontSize: '0.65rem',
                color: indicatorColor,
                fontVariantNumeric: 'tabular-nums',
                animation: staleness > 30 ? 'stalePulse 1.5s ease-in-out infinite' : undefined,
              }}
              aria-live="polite"
              aria-label={`Data ${formatElapsed(elapsedSeconds)}`}
            >
              {pipelinesError ? '⚠' : staleness > 30 ? '⚠' : '●'} {formatElapsed(elapsedSeconds)}
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
          <ErrorBoundary fallbackMessage="Failed to load pipelines">
            <PipelineList
              pipelines={filteredPipelines}
              selected={selectedPipeline}
              selectedNamespace={selectedNamespace}
              onSelect={(name, ns) => { handleSelectPipeline(name, ns); if (viewMode === 'ops-table') setViewMode('list') }}
              loading={pipelinesLoading}
              error={pipelinesError}
              searchInputRef={searchInputRef}
              total={pipelines.length}
            />
          </ErrorBoundary>
        </div>
      </aside>

      {/* Ops table mode — full-width table replaces the main content area */}
      {viewMode === 'ops-table' ? (
        <main id="main-content" tabIndex={-1} style={{ flex: 1, overflow: 'hidden', display: 'flex', flexDirection: 'column', background: 'var(--color-bg-deep)' }}>
          {insecureBanner}
          <PipelineOpsTable
            pipelines={pipelines}
            selected={selectedPipeline}
            selectedNamespace={selectedNamespace}
            onSelect={(name, ns) => { handleSelectPipeline(name, ns); setViewMode('list') }}
            loading={pipelinesLoading}
            error={pipelinesError}
          />
        </main>
      ) : (
        <>{/* Main area — column layout for header + content row */}
        <main id="main-content" tabIndex={-1} style={{ flex: 1, overflow: 'hidden', display: 'flex', flexDirection: 'column', background: 'var(--color-bg)' }}>
        {insecureBanner}
        {!selectedPipeline ? (
          <div style={pipelines.length > 0
            ? { flex: 1, display: 'flex', flexDirection: 'column', overflow: 'hidden' }
            : { color: 'var(--color-text-faint)', padding: '3rem 2rem', textAlign: 'center' }}>
            {pipelines.length > 0 ? (
              <FleetBoard pipelines={filteredPipelines} total={pipelines.length} onSelect={handleSelectPipeline} />
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
                      background: 'var(--color-accent-bg)',
                      color: 'var(--color-accent)',
                      border: '1px solid var(--color-accent)',
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
                      Bundle: <span style={{ color: 'var(--color-code)', fontFamily: 'monospace' }}>{activeBundle.name}</span>
                      {/* #763: copy bundle name */}
                      <CopyButton text={activeBundle.name} title={`Copy bundle name "${activeBundle.name}"`} />
                    </span>
                    <BundleTypeBadge type={activeBundle.type} />
                    <span style={{ color: 'var(--color-text-faint)' }}>·</span>
                    <HealthChip state={activeBundle.phase} size="sm" />
                    <RejectedLiveBanner bundle={activeBundle} />
                    {activeBundle.provenance?.commitSHA && (
                      <>
                        <span style={{ color: 'var(--color-text-faint)' }}>·</span>
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
                        <span style={{ color: 'var(--color-text-faint)' }}>·</span>
                        <span title="Author">{activeBundle.provenance.author}</span>
                      </>
                    )}
                    {activeBundle.provenance?.ciRunURL && (
                      <>
                        <span style={{ color: 'var(--color-text-faint)' }}>·</span>
                        <CIRunLink url={activeBundle.provenance.ciRunURL} />
                      </>
                    )}
                    {activeBundle.rejected && (
                      <span className="bundle-rejected" role="note">
                        Rejected by <strong>{activeBundle.rejected.by}</strong>
                        {activeBundle.rejected.at && <> {formatRelativeAge(activeBundle.rejected.at)}</>}: {activeBundle.rejected.reason}
                      </span>
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

              {/* #917: CreateBundleButton — lets platform engineers trigger a Bundle from the UI. */}
              {activePipeline && (
                <CreateBundleButton
                  pipelineName={activePipeline.name}
                  namespace={activePipeline.namespace ?? 'default'}
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
              <PolicyGatesPanel gates={shownGates} loading={gatesLoading} />

              {/* Bundle history (collapsible) */}
              {bundles.length > 0 && (
                <div style={{ marginBottom: '1rem' }}>
                  <button
                    onClick={() => setBundleHistoryOpen(o => !o)}
                    style={{
                      background: 'none',
                      border: 'none',
                      color: 'var(--color-accent)',
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
                          color: 'var(--color-text-muted)',
                        }}>
                          <HealthChip state={b.phase} size="sm" />
                          <span style={{ fontFamily: 'monospace', color: 'var(--color-text)' }}>{b.name}</span>
                          {b.provenance?.commitSHA && (
                            <span style={{ color: 'var(--color-text-muted)', fontFamily: 'monospace' }}>
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
              <ReleaseMetricsBar
                bundles={bundles}
                finalEnvironment={activePipeline?.environmentTopology?.at(-1)?.name}
                deploymentMetrics={activePipeline?.deploymentMetrics}
              />

              {/* Bundle Timeline — horizontal strip showing bundle history (Kargo freight timeline parity).
                  Receives bundles from parent state — no independent fetch (#321). */}
              <ErrorBoundary fallbackMessage="Timeline unavailable">
                <BundleTimeline
                  bundles={bundles}
                  loading={bundlesLoading}
                  selectedBundle={activeBundle?.name}
                  onSelectBundle={handleTimelineBundleSelect}
                  compareBundle={compareBundle}
                  onCompareBundle={(name) => setCompareTarget(name ?? undefined)}
                  onCompare={() => setUrlState({ bundle: compareBundle })}
                />
              </ErrorBoundary>
            </div>

            {/* #338: Bundle diff panel — shows when two bundles are selected for comparison */}
            {showDiffPanel && activeBundle && urlState.bundle !== activeBundle.name && (
              (() => {
                const compareBundleObj = bundles.find(b => b.name === urlState.bundle)
                if (!compareBundleObj) return null
                return (
                  <BundleDiffPanel
                    bundleA={activeBundle}
                    bundleB={compareBundleObj}
                    onClose={closeDiffPanel}
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
              edges={graph?.edges ?? []}
              selectedNode={selectedNode}
              onSelectNode={setSelectedNode}
              activeBundleName={activeBundle?.name}
              pipelineName={activePipeline?.name}
              namespace={activePipeline?.namespace ?? 'default'}
              onActionDone={() => { void manualRefresh() }}
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
                <ErrorBoundary fallbackMessage="Graph failed to load">
                  <DAGView
                    nodes={displayGraph?.nodes ?? []}
                    edges={displayGraph?.edges ?? []}
                    loading={graphLoading}
                    error={graphError}
                    highlightNodeIds={highlightIds}
                    selectedNode={selectedNode}
                    onSelectNode={setSelectedNode}
                  />
                </ErrorBoundary>
              </div>

              {/* NodeDetail split panel (#326) — sibling of DAGView, not overlay */}
              {selectedNode && (
                <ErrorBoundary fallbackMessage="Details unavailable">
                  <NodeDetail
                    node={selectedNode}
                    onClose={() => setSelectedNode(null)}
                    bundleName={activeBundle?.name}
                    pipelineName={activePipeline?.name}
                    namespace={activePipeline?.namespace ?? 'default'}
                    steps={activeSteps}
                    activeBundle={activeBundle}
                    onActionDone={() => { void manualRefresh() }}
                    nodes={graph?.nodes ?? []}
                    edges={graph?.edges ?? []}
                  />
                </ErrorBoundary>
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
