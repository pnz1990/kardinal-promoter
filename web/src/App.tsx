// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// App.tsx — Root component with Pipeline list sidebar and DAG view.
//
// Which bundle is on screen: the one the user picked in the timeline, for as
// long as it exists; otherwise the pipeline's active bundle (resolveShownBundle).
// Polls never override the user's pick.
// A pipeline is identified by namespace + name, so same-named pipelines in two
// namespaces never share a header, bundle list or map.
// Every load carries a view sequence number; a response that arrives after the
// user moved to another pipeline or bundle is dropped.
// #326: selectedNode is lifted here so the stage drawer (U3) opens beside
// the station map.
// #740: URL routing — pipeline and node selection persisted in hash fragment.
// #746: Global keyboard shortcuts — ?, r, Esc.
// #747: ErrorBoundary wraps async components to prevent white-screen crashes.
// #800: / shortcut to focus pipeline search input.
import { useState, useCallback, useMemo, useRef, useEffect, type RefObject } from 'react'
import { PipelineList } from './components/PipelineList'
import { PipelineOpsTable } from './components/PipelineOpsTable'
import PipelineMap from './stationMap/PipelineMap'
import { StageDrawer } from './stage/StageDrawer'
import { HealthChip } from './components/HealthChip'
import { RejectedLiveBanner } from './components/RejectedLiveBanner'
import { InsecureConnectionBanner } from './components/InsecureConnectionBanner'
import { Breadcrumb } from './components/Breadcrumb'
import { CommandPalette, type PaletteCommand } from './components/CommandPalette'
import { ErrorNotice, Icon, Kbd, TabPanel, Tabs } from './ui'
import { NotFound } from './components/NotFound'
import { PipelineConditions } from './components/PipelineConditions'
import { NARROW_QUERY, useMediaQuery } from './useMediaQuery'
import { useModalFocus } from './useModalFocus'
import { displayState } from './displayStates.gen'
import { versionName } from './versionName'
import { BundleTimeline } from './components/BundleTimeline'
import { BundleDiffPanel } from './components/BundleDiffPanel'
import { BundleTypeBadge } from './components/BundleTypeBadge'
import { PolicyGatesPanel } from './components/PolicyGatesPanel'
import { FleetHealthBar, filterPipelines, isFleetFilter, type FleetFilter } from './components/FleetHealthBar'
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
import { useTheme, themeControlLabel } from './ThemeContext'
import './styles/App.css'
import { EMPTY_URL_STATE, serializeHash, useUrlState, type PipelineTab } from './useUrlState'
import { useKeyboardShortcuts } from './useKeyboardShortcuts'
import { resolveShownBundle } from './bundleSelection'
import { terminalEnvironments } from './fleetModel'
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

/** The environment a gate node holds: the PromotionStep its edge leads to. */
function gatedEnvironment(node: GraphNode, graph: { nodes?: GraphNode[]; edges?: { from: string; to: string }[] } | null | undefined): string | undefined {
  const to = graph?.edges?.filter(e => e.from === node.id).map(e => graph.nodes?.find(n => n.id === e.to))
    .find(n => n?.type === 'PromotionStep')
  return to?.environment
}

/** Focus handling for the pipeline drawer while it is open (Esc closes it). */
function SidebarFocus({ sidebarRef, onClose }: { sidebarRef: RefObject<HTMLElement | null>; onClose: () => void }) {
  useModalFocus(sidebarRef, onClose)
  return null
}

export function App() {
  const { theme, preference, toggleTheme, density, setDensity } = useTheme()
  // #740: URL routing — pipeline and node selection backed by hash fragment.
  const [urlState, setUrlState] = useUrlState()
  const [pipelines, setPipelines] = useState<Pipeline[]>([])
  const [pipelinesLoading, setPipelinesLoading] = useState(true)
  const [pipelinesError, setPipelinesError] = useState<string | undefined>()

  const selectedPipeline = urlState.pipeline
  // The pipeline page's tab (tab= in the URL; Map when unset).
  const pipelineTab = urlState.tab ?? 'map'
  const [bundles, setBundles] = useState<Bundle[]>([])
  const [bundlesLoading, setBundlesLoading] = useState(false)
  // True once a read of the pipeline's versions worked and found none (the first-version call to action).
  const [noVersion, setNoVersion] = useState(false)
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

  // #326: selectedNode lifted here so the stage drawer is a split-panel sibling of the map.
  // #740: selectedNode ID is persisted in URL hash (node= param); full GraphNode object in local state.
  const [selectedNode, setSelectedNodeLocal] = useState<GraphNode | null>(null)

  /** Update both local state and the URL when a node is selected: an environment's step is
   *  the …/env/<env> route (the stage drawer, #1701); a gate opens the drawer of the
   *  environment it gates, at "why it's waiting". */
  const setSelectedNode = useCallback((node: GraphNode | null) => {
    setSelectedNodeLocal(node)
    if (!node) { setUrlState({ env: undefined, node: undefined, section: undefined }); return }
    const env = node.type === 'PromotionStep' ? node.environment : gatedEnvironment(node, graph)
    setUrlState({ env: env || undefined, node: undefined, section: node.type === 'PromotionStep' ? undefined : 'why' })
  }, [setUrlState, graph])

  // #740: When graph data changes, restore selectedNode from URL if a node= param is present
  // and the current selectedNode doesn't already match. With no node= (Back to an entry
  // before the node was opened), the details close.
  useEffect(() => {
    const { env, node: nodeId } = urlState
    if (!env && !nodeId) {
      if (selectedNode) setSelectedNodeLocal(null)
      return
    }
    if (!graph) return
    // A node= link (a gate, or a step from before the env route): open the drawer of the
    // environment it names, replacing the entry so Back does not return to it.
    if (!env && nodeId) {
      const n = graph.nodes?.find(x => x.id === nodeId || (x.type === 'PromotionStep' && x.environment === nodeId))
      const target = n && (n.type === 'PromotionStep' ? n.environment : gatedEnvironment(n, graph))
      if (target) setUrlState({ env: target, node: undefined, section: n?.type === 'PromotionStep' ? urlState.section : 'why' }, { replace: true })
      return
    }
    const matches = (n: GraphNode) => env ? n.type === 'PromotionStep' && n.environment === env : n.id === nodeId
    if (selectedNode && matches(selectedNode)) return
    const node = graph.nodes?.find(matches)
    if (node) setSelectedNodeLocal(node)
  }, [graph, urlState, selectedNode, setUrlState])

  // The user picked the Map tab's view (Map, Table or Matrix): a narrow screen
  // then keeps the Map instead of opening on the Table.
  const [mapChosen, setMapChosen] = useState(false)

  // U3: the environment whose stage drawer is open (the …/env/<env> route).
  const stageEnv = urlState.env

  // U2: a station opens its stage drawer (the #1701 contract): its route, and the
  // section to scroll to; an environment the version has not reached opens too.
  const openStage = useCallback((env: string, section?: string) => {
    setSelectedNodeLocal(graph?.nodes.find(n => n.type === 'PromotionStep' && n.environment === env) ?? null)
    setUrlState({ env, node: undefined, section })
  }, [graph, setUrlState])

  // #338: Bundle diff comparison state. Shift-click picks the bundle to
  // compare (compareTarget); the timeline then offers Compare and clear.
  // #740: the comparison is open while the URL has bundle=, so a link opens it.
  // Closing it keeps the pick, so Compare and clear are where focus returns.
  const [compareTarget, setCompareTarget] = useState<string | undefined>()
  const showDiffPanel = !!urlState.compare
  const closeDiffPanel = useCallback(() => {
    setUrlState({ compare: undefined })
  }, [setUrlState])

  // Refresh indicator: tracks last successful poll for the staleness indicator.
  const { elapsedSeconds, onSuccess: onPollSuccess } = useRefreshIndicator()

  // #913: insecure connection banner — dismissed state for the current session.
  const [insecureBannerDismissed, setInsecureBannerDismissed] = useState(false)

  // #462: view mode — 'list' (sidebar) | 'ops-table' (full-width operations table), in the
  // URL as view=ops.
  type ViewMode = 'list' | 'ops-table'
  const viewMode: ViewMode = urlState.view === 'ops' ? 'ops-table' : 'list'
  const setViewMode = useCallback((v: ViewMode | ((prev: ViewMode) => ViewMode)) => {
    const next = typeof v === 'function' ? v(viewMode) : v
    setUrlState({ view: next === 'ops-table' ? 'ops' : undefined })
  }, [viewMode, setUrlState])

  // #505: Fleet filter — drives which pipelines are visible in the sidebar list; in the URL
  // as filter=.
  const fleetFilter: FleetFilter = isFleetFilter(urlState.filter) ? urlState.filter : 'all'
  const setFleetFilter = useCallback((f: FleetFilter) => {
    setUrlState({ filter: f === 'all' ? undefined : f })
  }, [setUrlState])
  const filteredPipelines = filterPipelines(pipelines, fleetFilter)

  // #746: Keyboard shortcuts help panel visibility.
  const [showShortcutsPanel, setShowShortcutsPanel] = useState(false)
  // ⌘K / Ctrl+K: the command palette.
  const [paletteOpen, setPaletteOpen] = useState(false)

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
          setNoVersion(bs.length === 0)
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
            setNoVersion(false)
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
    // The version the URL names (…/v/<bundle>), if any, is the pick.
    userBundleRef.current = urlState.version
    setShownBundleName(undefined)
    setGraph(undefined)
    setGraphError(undefined)
    setActiveSteps([])
    setBundles([])
    setNoVersion(false)
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

  // An open comparison (compare=, from Compare, a link or Back) is also the
  // pick. This runs after the reset above, so a link that names a pipeline
  // and a compare= keeps the pick when the comparison closes.
  useEffect(() => {
    if (urlState.compare) setCompareTarget(urlState.compare)
  }, [urlState.compare, selectedPipeline, urlState.ns])

  // With exactly one pipeline, a first visit (no route in the URL) opens it directly.
  const openSingle = useRef(window.location.hash === '' || window.location.hash === '#')
  useEffect(() => {
    if (!openSingle.current || pipelinesLoading) return
    openSingle.current = false
    if (!selectedPipeline && pipelines.length === 1) {
      setUrlState({ pipeline: pipelines[0].name, ns: pipelines[0].namespace }, { replace: true })
    }
  }, [pipelines, pipelinesLoading, selectedPipeline, setUrlState])

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
      if (selectedNode || urlState.env) { setSelectedNode(null); return }
      if (showDiffPanel) { closeDiffPanel(); return }
    }, [showShortcutsPanel, selectedNode, urlState.env, showDiffPanel, setSelectedNode, closeDiffPanel]),
    // #800: / focuses the pipeline search input
    onSearch: useCallback(() => {
      searchInputRef.current?.focus()
    }, []),
    onPalette: useCallback(() => setPaletteOpen(true), []),
  })

  // One history entry per switch: the new pipeline, with no node or diff open.
  // The load effect above does the fetching.
  const handleSelectPipeline = useCallback((name: string, namespace: string, env?: string) => {
    setSelectedNodeLocal(null)
    setUrlState({ pipeline: name, ns: namespace, env, node: undefined, version: undefined, compare: undefined, view: undefined })
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

  // Shows a bundle's graph and steps until the user picks another one or it disappears.
  const showBundle = useCallback((bundleName: string) => {
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
    setSelectedNodeLocal(null)
    void track(seq, Promise.all([api.getGraph(bundleName, namespace), api.getSteps(bundleName, namespace)])
      .then(([g, steps]) => {
        if (stale()) return
        setGraph(g)
        setActiveSteps(steps)
      })
      .catch(e => { if (!stale()) setGraphError(errorText(e)) })
      .finally(() => { if (!stale()) setGraphLoading(false) }))
  }, [bundles, selectedNamespace])

  // A timeline pick is the …/v/<bundle> route (one history entry, details closed).
  const handleTimelineBundleSelect = useCallback((bundleName: string) => {
    showBundle(bundleName)
    setUrlState({ version: bundleName, env: undefined, node: undefined })
  }, [setUrlState, showBundle])

  // The URL's version, from a pick, a link, Back or Forward: show it once the pipeline's
  // bundles are loaded. Without one, the pipeline's own choice (resolveShownBundle) applies.
  useEffect(() => {
    if (!selectedPipeline || bundlesLoading) return
    const v = urlState.version
    if (v) {
      if (v !== shownBundleName && bundles.some(b => b.name === v)) showBundle(v)
      return
    }
    if (userBundleRef.current) {
      userBundleRef.current = undefined
      void loadPipeline(selectedPipeline, urlState.ns, ++viewSeq.current)
    }
  }, [urlState.version, urlState.ns, selectedPipeline, bundles, bundlesLoading, shownBundleName, showBundle, loadPipeline])

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

  // How fresh the data is — late at 15s (ochre), stale at 30s (garnet, pulsing).
  // A failing poll is what makes data that old, so the error does not hold it amber.
  const staleness = elapsedSeconds ?? 0
  const freshness: 'fresh' | 'late' | 'stale' = staleness > 30
    ? 'stale'                  // garnet and pulsing when critically stale > 30s (#766)
    : pipelinesError || staleness > 15
    ? 'late'                   // ochre on error, or when stale > 15s
    : 'fresh'

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

  // Stages of the shown version whose promotion failed (the Activity tab groups them).
  const failedStepCount = activeSteps.filter(st => st.state === 'Failed').length

  // A route naming a pipeline or a version the cluster does not have says so (NotFound),
  // and never shows another one in its place.
  const pipelineMissing = !!selectedPipeline && !pipelinesLoading && !pipelinesError && !activePipeline
  const versionMissing = !!activePipeline && !!urlState.version && !bundlesLoading
    && !bundles.some(b => b.name === urlState.version)


  // Below the md breakpoint the pipeline list is a drawer, so the page gets the full width.
  const narrow = useMediaQuery(NARROW_QUERY)
  const [navOpen, setNavOpen] = useState(false)
  const sidebarRef = useRef<HTMLElement>(null)
  const navButtonRef = useRef<HTMLButtonElement>(null)
  const closeNav = useCallback(() => {
    setNavOpen(false)
    navButtonRef.current?.focus()
  }, [])
  // Going somewhere closes the drawer.
  useEffect(() => { setNavOpen(false) }, [urlState.pipeline, urlState.ns, urlState.env, urlState.view, viewMode])
  const navButton = narrow ? (
    <button
      ref={navButtonRef}
      type="button"
      className="k-icon-button k-icon-button--md app-topbar__nav"
      aria-label="Pipelines"
      title="Pipelines"
      aria-expanded={navOpen}
      aria-controls="app-sidebar"
      onClick={() => setNavOpen(o => !o)}
    >
      <Icon name="menu" />
    </button>
  ) : null

  // The top bar's tools: Go to or do… (⌘K), and the visible way to help (?).
  const topbarTools = (
    <div className="app-topbar__tools">
      <button type="button" className="app-topbar__search" aria-label="Go to or do… ⌘K" onClick={() => setPaletteOpen(true)}>
        <Icon name="search" />
        <span>Go to or do…</span>
        <Kbd>⌘K</Kbd>
      </button>
      <button
        type="button"
        className="k-icon-button k-icon-button--md app-topbar__help"
        aria-label="Help and keyboard shortcuts"
        title="Help and keyboard shortcuts (?)"
        aria-haspopup="dialog"
        onClick={() => setShowShortcutsPanel(true)}
      >
        ?
      </button>
    </div>
  )

  // #913: Insecure connection warning — plain HTTP from a non-loopback address.
  // It heads every main view, so it shows before any pipeline loads (the API
  // may refuse the page's requests outright).
  const insecureBanner = (
    <InsecureConnectionBanner
      dismissed={insecureBannerDismissed}
      onDismiss={() => setInsecureBannerDismissed(true)}
      className="app-insecure-banner"
    />
  )

  const paletteCommands = useMemo<PaletteCommand[]>(() => {
    const go: PaletteCommand[] = [{
      id: 'go:fleet', group: 'Go to', label: 'Pipelines', hint: 'fleet',
      run: () => setUrlState({ ...EMPTY_URL_STATE, filter: urlState.filter, q: urlState.q }),
    }]
    for (const p of pipelines) {
      go.push({ id: `go:p:${p.namespace}/${p.name}`, group: 'Go to', label: p.name, hint: p.namespace, run: () => handleSelectPipeline(p.name, p.namespace) })
    }
    for (const p of pipelines) {
      for (const e of p.environmentTopology ?? []) {
        go.push({
          id: `go:e:${p.namespace}/${p.name}/${e.name}`, group: 'Go to', label: `${p.name} › ${e.name}`, hint: p.namespace,
          run: () => handleSelectPipeline(p.name, p.namespace, e.name),
        })
      }
    }
    if (activePipeline) {
      for (const b of bundles) {
        go.push({
          id: `go:v:${b.name}`, group: 'Go to', label: `Version ${b.name}`, hint: activePipeline.name, keywords: b.provenance?.commitSHA,
          run: () => handleTimelineBundleSelect(b.name),
        })
      }
    }
    const next = preference === 'system' ? 'Light' : preference === 'light' ? 'Dark' : 'System'
    const act: PaletteCommand[] = [
      { id: 'do:theme', group: 'Do', label: `Switch the theme to ${next}`, hint: 'theme', run: toggleTheme },
      {
        id: 'do:density', group: 'Do', label: density === 'compact' ? 'Use comfortable density' : 'Use compact density', hint: 'density',
        run: () => setDensity(density === 'compact' ? 'comfortable' : 'compact'),
      },
      { id: 'do:refresh', group: 'Do', label: 'Refresh now', hint: 'r', run: () => { void manualRefresh() } },
      { id: 'do:ops', group: 'Do', label: viewMode === 'ops-table' ? 'Show the fleet board' : 'Show the operations table', run: () => setViewMode(v => v === 'list' ? 'ops-table' : 'list') },
      { id: 'do:shortcuts', group: 'Do', label: 'Show keyboard shortcuts', hint: '?', run: () => setShowShortcutsPanel(true) },
    ]
    return [...go, ...act]
  }, [pipelines, activePipeline, bundles, preference, density, viewMode, urlState.filter, urlState.q, setUrlState,
    handleSelectPipeline, handleTimelineBundleSelect, toggleTheme, setDensity, manualRefresh, setViewMode])

  return (
    <div className="app" data-narrow={narrow ? '' : undefined}>
      {/* The first Tab stop: past the sidebar to the main content. */}
      <a className="skip-link" href="#main-content"
        onClick={e => { e.preventDefault(); document.getElementById('main-content')?.focus() }}>
        Skip to main content
      </a>
      {/* #746: Keyboard shortcuts help panel — rendered at root level so it overlays all content. */}
      {showShortcutsPanel && (
        <KeyboardShortcutsPanel onClose={() => setShowShortcutsPanel(false)} />
      )}
      <CommandPalette open={paletteOpen} onClose={() => setPaletteOpen(false)} commands={paletteCommands} />
      {/* Sidebar; below the md breakpoint a drawer over the page, opened from the top bar. */}
      {narrow && navOpen && <SidebarFocus sidebarRef={sidebarRef} onClose={closeNav} />}
      {narrow && navOpen && <div className="app-sidebar-scrim" aria-hidden="true" onClick={closeNav} />}
      <aside
        ref={sidebarRef}
        id="app-sidebar"
        className="app-sidebar"
        data-open={narrow && navOpen ? '' : undefined}
        {...(narrow ? { role: 'dialog', 'aria-modal': navOpen || undefined, 'aria-label': 'Pipelines', hidden: !navOpen } : {})}
      >
        <div className="app-sidebar__brandbar">
          {/* Brand: logo + wordmark; it opens the fleet board. */}
          <button
            type="button"
            onClick={() => { setSelectedNodeLocal(null); setUrlState({ ...EMPTY_URL_STATE, filter: urlState.filter, q: urlState.q }) }}
            title="Show the fleet"
            aria-label="Show the fleet"
            className="app-brand"
          >
            <img
              src={`${import.meta.env.BASE_URL}logo.png`}
              alt="Kardinal"
              className="app-brand__logo"
            />
            <span className="app-brand__word">
              KARDINAL
            </span>
          </button>
          {/* Staleness indicator with manual refresh button (#362) */}
          <div className="app-status">
          <button
            onClick={manualRefresh}
            title="Refresh now"
            aria-label="Refresh data"
            className="app-refresh"
          >
            <span
              title={pipelinesError ? `Error: ${pipelinesError}` : staleness > 30 ? 'Data is stale — click to refresh' : 'Last updated'}
              className="app-refresh__age"
              data-level={freshness}
              aria-live="polite"
              aria-label={`Data ${formatElapsed(elapsedSeconds)}`}
            >
              {pipelinesError ? '⚠' : staleness > 30 ? '⚠' : '●'} {formatElapsed(elapsedSeconds)}
            </span>
            <span className="app-refresh__icon" title="Click to refresh">↺</span>
          </button>
          {/* Theme toggle button (#722) */}
          <button
            onClick={toggleTheme}
            title={themeControlLabel(preference, theme)}
            aria-label={themeControlLabel(preference, theme)}
            className="app-theme"
          >
            <span aria-hidden="true">{preference === 'system' ? '◐' : preference === 'light' ? '☀' : '☾'}</span>
          </button>
          </div>
        </div>
        <div className="app-sidebar__heading">
          <span>PIPELINES</span>
          <div className="app-sidebar__tools">
            {/* #462: view mode toggle — list sidebar vs ops table */}
            <button
              onClick={() => setViewMode(v => v === 'list' ? 'ops-table' : 'list')}
              title={viewMode === 'list' ? 'Switch to Operations Table' : 'Switch to List View'}
              aria-label={viewMode === 'list' ? 'Switch to Operations Table' : 'Switch to List View'}
              className="app-view-toggle"
              data-active={viewMode === 'ops-table' || undefined}
            >
              {viewMode === 'list' ? '⊞ Ops' : '☰ List'}
            </button>
            {currentNamespace && (
              <span className="app-namespace" title={`Namespace: ${currentNamespace}`}>
                {currentNamespace}
              </span>
            )}
          </div>
        </div>
        <div className="app-sidebar__scroll">
          {/* #505: Fleet health bar — above pipeline list in sidebar */}
          {pipelines.length > 0 && (
            <div className="app-sidebar__fleet">
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
              onSelect={(name, ns) => handleSelectPipeline(name, ns)}
              loading={pipelinesLoading}
              error={pipelinesError}
              onRetry={() => { void manualRefresh() }}
              searchInputRef={searchInputRef}
              total={pipelines.length}
              initialQuery={urlState.q}
              onQueryChange={q => setUrlState({ q: q || undefined }, { replace: true })}
            />
          </ErrorBoundary>
        </div>
      </aside>

      {/* Ops table mode — full-width table replaces the main content area */}
      {viewMode === 'ops-table' ? (
        <main id="main-content" tabIndex={-1} className="app-main app-main--ops">
          <div className="app-topbar">
            {navButton}
            <Breadcrumb route={urlState} namespace={activePipeline?.namespace} />
            {topbarTools}
          </div>
          {insecureBanner}
          <PipelineOpsTable
            pipelines={pipelines}
            selected={selectedPipeline}
            selectedNamespace={selectedNamespace}
            onSelect={(name, ns) => handleSelectPipeline(name, ns)}
            loading={pipelinesLoading}
            error={pipelinesError}
            onRetry={() => { void manualRefresh() }}
          />
        </main>
      ) : (
        <>{/* Main area — column layout for header + content row */}
        <main id="main-content" tabIndex={-1} className="app-main">
          <div className="app-topbar">
            {navButton}
            <Breadcrumb route={urlState} namespace={activePipeline?.namespace} />
            {topbarTools}
          </div>
        {insecureBanner}
        {!selectedPipeline ? (
          <div className={pipelines.length > 0 ? 'app-fleet' : 'app-empty'}>
            {pipelines.length > 0 ? (
              <FleetBoard pipelines={filteredPipelines} total={pipelines.length} onSelect={handleSelectPipeline} />
            ) : pipelinesError ? (
              /* A failed first read: the cluster may have pipelines, so no onboarding. */
              <ErrorNotice error={pipelinesError} onRetry={() => { void manualRefresh() }} className="app-empty__error" />
            ) : (
              /* #530: Improved empty state with copy button, docs link, expected output */
              <EmptyState />
            )}
          </div>
        ) : (
          <>
            {/* Header: the name and the pipeline's actions, then one summary line. */}
            <div className="app-pipeline-head">
              <div className="app-pipeline-head__bar">
                <h1 className="app-pipeline-head__title">{activePipeline?.name ?? selectedPipeline}</h1>
                {/* Paused banner in main panel (#328) */}
                {activePipeline?.paused && (
                  <span className="app-paused">⏸ PAUSED — no new promotions</span>
                )}
                {activePipeline && (
                  <div className="app-pipeline-head__actions">
                    {/* #506: pause / resume; while paused, Resume is the primary action. */}
                    <ActionBar
                      pipelineName={activePipeline.name}
                      namespace={activePipeline.namespace ?? 'default'}
                      paused={activePipeline.paused ?? false}
                      onRefresh={manualRefresh}
                    />
                    {/* #917: ship a version from the UI. */}
                    <CreateBundleButton
                      pipelineName={activePipeline.name}
                      namespace={activePipeline.namespace ?? 'default'}
                      onRefresh={manualRefresh}
                      variant={activePipeline.paused || (noVersion && !graphError) ? 'secondary' : 'primary'}
                    />
                  </div>
                )}
              </div>
              {activePipeline && (
                <p className="app-pipeline-head__summary">
                  {/* P1's status.summary sentence; the shown version's state when there is none. */}
                  <span>{activePipeline.summary ?? (activeBundle ? displayState('bundle', activeBundle.phase).label : 'No version yet')}</span>
                  {/* #329/#763: the shown version's provenance, quiet; none for a version that is not found. */}
                  {activeBundle && !versionMissing && (
                    <span className="app-provenance">
                      <span className="app-sep" aria-hidden="true">·</span>
                      <span title={activeBundle.name}>
                        Version <span className="app-provenance__bundle">{versionName(activeBundle.name, activePipeline.name)}</span>
                      </span>
                      <CopyButton text={activeBundle.name} title={`Copy version name "${activeBundle.name}"`} />
                      <BundleTypeBadge type={activeBundle.type} compact />
                      {urlState.version && (
                        <span>({displayState('bundle', activeBundle.phase).label})</span>
                      )}
                      {activeBundle.provenance?.commitSHA && (
                        <>
                          <span className="app-sep" aria-hidden="true">·</span>
                          <span className="app-sha" title="Commit SHA">
                            {activeBundle.provenance.commitSHA.slice(0, 8)}
                          </span>
                          <CopyButton text={activeBundle.provenance.commitSHA} title="Copy commit SHA" />
                        </>
                      )}
                      {activeBundle.provenance?.author && (
                        <>
                          <span className="app-sep" aria-hidden="true">·</span>
                          <span title="Author">{activeBundle.provenance.author}</span>
                        </>
                      )}
                      {activeBundle.provenance?.ciRunURL && (
                        <>
                          <span className="app-sep" aria-hidden="true">·</span>
                          <CIRunLink url={activeBundle.provenance.ciRunURL} />
                        </>
                      )}
                    </span>
                  )}
                </p>
              )}
              {/* #1727: the Pipeline's own problems (not Ready, an unlabelled git Secret). */}
              <PipelineConditions conditions={activePipeline?.conditions} />
              {activeBundle && !versionMissing && <RejectedLiveBanner bundle={activeBundle} />}
              {activeBundle?.rejected && !versionMissing && (
                <p className="bundle-rejected" role="note">
                  Rejected by <strong>{activeBundle.rejected.by}</strong>
                  {activeBundle.rejected.at && <> {formatRelativeAge(activeBundle.rejected.at)}</>}: {activeBundle.rejected.reason}
                </p>
              )}
              {activePipeline && !versionMissing && (
                <Tabs
                  label="Pipeline views"
                  value={pipelineTab}
                  onChange={(t) => setUrlState({ tab: t === 'map' ? undefined : (t as PipelineTab) })}
                  items={[
                    { id: 'map', label: 'Map' },
                    { id: 'versions', label: 'Versions', count: bundles.length || undefined },
                    { id: 'activity', label: 'Activity', count: (blockedGateIds.size + failedStepCount) || undefined },
                    { id: 'metrics', label: 'Metrics' },
                  ]}
                />
              )}
            </div>

            {pipelineMissing ? (
              <NotFound
                title={`No pipeline named ${selectedPipeline}${urlState.ns ? ` in ${urlState.ns}` : ''}`}
                detail="It may have been deleted, or the link has a typo."
                href={serializeHash(EMPTY_URL_STATE)}
                linkText="Back to all pipelines"
              />
            ) : versionMissing ? (
              <NotFound
                title={`No version ${urlState.version} in ${activePipeline?.name}`}
                detail="It may have been cleaned up after newer versions shipped, or the link has a typo."
                href={serializeHash({ ...EMPTY_URL_STATE, pipeline: activePipeline?.name, ns: activePipeline?.namespace })}
                linkText={`Show the current version of ${activePipeline?.name}`}
              />
            ) : activePipeline ? (
            <>
            {/* Every tab's panel is in the page (hidden when not shown), so each tab's aria-controls resolves. */}
            <TabPanel id="map" hidden={pipelineTab !== 'map'} className="app-tab app-tab--map">
              {pipelineTab === 'map' && (<>
                <div className="app-map-head">
                  {/* An empty pipeline: the one thing to do next (U4). */}
                  {/* Only after a read that worked: a failed one shows its error on the map instead. */}
                  {noVersion && !graphError && (
                    <div className="app-first-version" data-testid="first-version">
                      <p className="app-first-version__text">No version of {activePipeline.name} has shipped yet.</p>
                      <CreateBundleButton
                        pipelineName={activePipeline.name}
                        namespace={activePipeline.namespace ?? 'default'}
                        onRefresh={manualRefresh}
                        label="Ship your first version"
                        variant={activePipeline.paused ? 'secondary' : 'primary'}
                      />
                    </div>
                  )}
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
                      onCompare={() => setUrlState({ compare: compareBundle })}
                    />
                  </ErrorBoundary>
                </div>

                {/* #338: Bundle diff panel — shows when two bundles are selected for comparison */}
                {showDiffPanel && activeBundle && urlState.compare !== activeBundle.name && (
                  (() => {
                    const compareBundleObj = bundles.find(b => b.name === urlState.compare)
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

                {/* U2: one stage graph, the station map: it replaces the lane cards and the
                    DAG. A station opens its stage drawer (#1701); what holds the version back
                    is the map's attention strip and the gates on its edges. */}
                <div className="app-content">
                  <div className="app-dag app-map" data-split={stageEnv ? true : undefined}>
                    <ErrorBoundary fallbackMessage="The pipeline map failed to load">
                      <PipelineMap
                        pipeline={activePipeline}
                        bundle={activeBundle}
                        graph={graph ?? undefined}
                        compareBundle={bundles.find(b => b.name === (urlState.compare ?? compareBundle) && b.name !== activeBundle?.name)}
                        loading={graphLoading}
                        error={graphError}
                        onRetry={() => { void manualRefresh() }}
                        focused={stageEnv}
                        onOpenStage={openStage}
                        onActionDone={() => { void manualRefresh() }}
                        onOpenGate={id => { const n = graph?.nodes.find(x => x.id === id); if (n) setSelectedNode(n) }}
                        gates={shownGates}
                        bundles={bundles}
                        // Narrow screens open on the Table, the map's linear equivalent (DESIGN-SYSTEM §3.5).
                        as={urlState.as ?? (narrow && !mapChosen ? 'table' : undefined)}
                        onAs={v => { setMapChosen(true); setUrlState({ as: v }) }}
                        onSelectVersion={handleTimelineBundleSelect}
                        onCompareVersion={b => { setCompareTarget(b); setUrlState({ compare: b }) }}
                      />
                    </ErrorBoundary>
                  </div>

                  {/* U3: the stage drawer of the environment in the route (#1701): a column
                      beside the page at lg and wider, a modal sheet narrower. */}
                  {stageEnv && activePipeline && (
                    <ErrorBoundary fallbackMessage="Details unavailable">
                      <StageDrawer
                        namespace={activePipeline.namespace}
                        pipeline={activePipeline.name}
                        env={stageEnv}
                        section={urlState.section}
                        onClose={() => setSelectedNode(null)}
                        onNavigate={(env, section) => { setSelectedNodeLocal(null); setUrlState({ env, node: undefined, section }) }}
                        data={{ pipeline: activePipeline, bundles, steps: activeSteps, gates }}
                        onChanged={() => { void manualRefresh() }}
                      />
                    </ErrorBoundary>
                  )}
                </div>
              </>)}
            </TabPanel>

            <TabPanel id="versions" hidden={pipelineTab !== 'versions'} className="app-tab">
              {pipelineTab === 'versions' && (bundles.length === 0
                ? <p className="app-tab__empty">No version of this pipeline yet.</p>
                : (
                  <ul className="app-history__list" aria-label="Versions">
                    {bundles.map(b => (
                      <li key={b.name} className="app-history__item">
                        <HealthChip state={b.phase} kind="bundle" size="sm" />
                        <a className="app-history__name" title={b.name} href={serializeHash({ ...urlState, version: b.name, env: undefined, node: undefined, tab: undefined })}>{versionName(b.name, activePipeline.name)}</a>
                        {b.provenance?.commitSHA && (
                          <span className="app-sha">
                            {b.provenance.commitSHA.slice(0, 8)}
                          </span>
                        )}
                      </li>
                    ))}
                  </ul>
                ))}
            </TabPanel>

            <TabPanel id="activity" hidden={pipelineTab !== 'activity'} className="app-tab">
              {pipelineTab === 'activity' && (<>
                {/* #528: failures grouped across environments; a click opens the environment on the map. */}
                <PromotionErrorsPanel
                  steps={activeSteps}
                  onSelectEnvironment={(env) => {
                    const node = graph?.nodes.find(n => n.environment === env && n.type === 'PromotionStep')
                    if (node) { setUrlState({ tab: undefined }); setSelectedNode(node) }
                  }}
                />
                {/* #340: the shown version's gates, their conditions and approvals. */}
                <PolicyGatesPanel gates={shownGates} loading={gatesLoading} onDecided={() => { void manualRefresh() }} />
              </>)}
            </TabPanel>

            <TabPanel id="metrics" hidden={pipelineTab !== 'metrics'} className="app-tab">
              {pipelineTab === 'metrics' && (<>
                {/* #504: Release efficiency metrics bar — inline metrics for the pipeline. */}
                <ReleaseMetricsBar
                  bundles={bundles}
                  finalEnvironments={terminalEnvironments(activePipeline?.environmentTopology, activePipeline?.topologyResolved)}
                  deploymentMetrics={activePipeline?.deploymentMetrics}
                />
              </>)}
            </TabPanel>
            </>
            ) : null}
          </>
        )}
      </main>
        </>
      )}
    </div>
  )
}
