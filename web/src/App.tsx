// App.tsx — Root component with Pipeline list sidebar and DAG view.
import { useState, useCallback } from 'react'
import { PipelineList } from './components/PipelineList'
import { DAGView } from './components/DAGView'
import { HealthChip } from './components/HealthChip'
import { api } from './api/client'
import { usePolling } from './usePolling'
import type { Pipeline, Bundle, GraphResponse } from './types'

const POLL_INTERVAL_MS = 5000

export function App() {
  const [pipelines, setPipelines] = useState<Pipeline[]>([])
  const [pipelinesLoading, setPipelinesLoading] = useState(true)
  const [pipelinesError, setPipelinesError] = useState<string | undefined>()

  const [selectedPipeline, setSelectedPipeline] = useState<string | undefined>()
  const [bundles, setBundles] = useState<Bundle[]>([])
  const [bundleHistoryOpen, setBundleHistoryOpen] = useState(false)

  const [graph, setGraph] = useState<GraphResponse | undefined>()
  const [graphLoading, setGraphLoading] = useState(false)
  const [graphError, setGraphError] = useState<string | undefined>()

  // Poll pipeline list every 5 seconds.
  usePolling(async () => {
    try {
      const ps = await api.listPipelines()
      setPipelines(ps)
      setPipelinesError(undefined)
    } catch (e) {
      setPipelinesError(String(e))
    } finally {
      setPipelinesLoading(false)
    }
  }, POLL_INTERVAL_MS)

  // Refresh bundles + graph for the selected pipeline every 5 seconds.
  usePolling(async () => {
    if (!selectedPipeline) return
    try {
      const bs = await api.listBundles(selectedPipeline)
      setBundles(bs)
      const promoting = bs.find(b => b.phase === 'Promoting') ?? bs[0]
      if (promoting) {
        const g = await api.getGraph(promoting.name)
        setGraph(g)
        setGraphError(undefined)
      }
    } catch (e) {
      setGraphError(String(e))
    }
  }, POLL_INTERVAL_MS, !!selectedPipeline)

  const handleSelectPipeline = useCallback((name: string) => {
    setSelectedPipeline(name)
    setGraph(undefined)
    setGraphError(undefined)
    setGraphLoading(true)
    setBundleHistoryOpen(false)

    api.listBundles(name)
      .then(bs => {
        setBundles(bs)
        const promoting = bs.find(b => b.phase === 'Promoting') ?? bs[0]
        if (promoting) {
          return api.getGraph(promoting.name).then(g => setGraph(g))
        }
      })
      .catch(e => setGraphError(String(e)))
      .finally(() => setGraphLoading(false))
  }, [])

  const activePipeline = pipelines.find(p => p.name === selectedPipeline)
  const activeBundle = bundles.find(b => b.phase === 'Promoting') ?? bundles[0]

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
          fontWeight: 700,
          fontSize: '0.9rem',
          color: '#6366f1',
          letterSpacing: '0.05em',
        }}>
          KARDINAL
        </div>
        <div style={{ padding: '0.75rem 1rem', fontSize: '0.75rem', color: '#475569', fontWeight: 600 }}>
          PIPELINES
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

      {/* Main area */}
      <main style={{ flex: 1, overflow: 'auto', padding: '1.5rem', background: '#0f172a' }}>
        {!selectedPipeline ? (
          <div style={{ color: '#475569', padding: '2rem' }}>
            Select a pipeline to view its promotion status.
          </div>
        ) : (
          <>
            {/* Pipeline header */}
            <div style={{ marginBottom: '1rem' }}>
              <h1 style={{ fontSize: '1.25rem', fontWeight: 700, marginBottom: '0.25rem' }}>
                {activePipeline?.name}
              </h1>
              {activeBundle && (
                <div style={{ fontSize: '0.85rem', color: '#94a3b8' }}>
                  Bundle: <span style={{ color: '#7dd3fc' }}>{activeBundle.name}</span>
                  {' · '}
                  {activeBundle.phase}
                  {activeBundle.provenance?.commitSHA && (
                    <> · <span style={{ fontFamily: 'monospace' }}>{activeBundle.provenance.commitSHA.slice(0, 8)}</span></>
                  )}
                </div>
              )}
            </div>

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

            {/* DAG */}
            <div style={{
              background: '#1e293b',
              borderRadius: '8px',
              padding: '1rem',
              minHeight: '300px',
            }}>
              <DAGView
                nodes={graph?.nodes ?? []}
                edges={graph?.edges ?? []}
                loading={graphLoading}
                error={graphError}
              />
            </div>
          </>
        )}
      </main>
    </div>
  )
}
