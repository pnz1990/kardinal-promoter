// App.tsx — Root component with Pipeline list sidebar and DAG view.
// Polls every 5 seconds per spec 006 (usePolling hook).
import { useState, useCallback } from 'react'
import { PipelineList } from './components/PipelineList'
import { DAGView } from './components/DAGView'
import { usePolling } from './hooks/usePolling'
import { api } from './api/client'
import type { Pipeline, Bundle, GraphResponse } from './types'

function phaseBadgeColor(phase: string): string {
  switch (phase) {
    case 'Verified': return '#22c55e'
    case 'Promoting': return '#f59e0b'
    case 'Failed': return '#ef4444'
    case 'Superseded': return '#94a3b8'
    default: return '#64748b'
  }
}

export function App() {
  const [pipelines, setPipelines] = useState<Pipeline[]>([])
  const [pipelinesLoading, setPipelinesLoading] = useState(true)
  const [pipelinesError, setPipelinesError] = useState<string | undefined>()

  const [selectedPipeline, setSelectedPipeline] = useState<string | undefined>()
  const [bundles, setBundles] = useState<Bundle[]>([])

  const [graph, setGraph] = useState<GraphResponse | undefined>()
  const [graphLoading, setGraphLoading] = useState(false)
  const [graphError, setGraphError] = useState<string | undefined>()

  // Poll pipeline list every 5 seconds (spec 006).
  usePolling(useCallback(() => {
    api.listPipelines()
      .then(ps => {
        setPipelines(ps)
        setPipelinesLoading(false)
        setPipelinesError(undefined)
      })
      .catch(e => {
        setPipelinesError(String(e))
        setPipelinesLoading(false)
      })
  }, []), { intervalMs: 5000 })

  // Poll bundles + graph for the selected pipeline every 5 seconds.
  usePolling(useCallback(() => {
    if (!selectedPipeline) return
    api.listBundles(selectedPipeline)
      .then(bs => {
        setBundles(bs)
        // Load graph for the most recent Promoting bundle.
        const promoting = bs.find(b => b.phase === 'Promoting') ?? bs[0]
        if (promoting) {
          setGraphLoading(true)
          return api.getGraph(promoting.name)
            .then(g => {
              setGraph(g)
              setGraphError(undefined)
            })
            .catch(e => setGraphError(String(e)))
            .finally(() => setGraphLoading(false))
        }
      })
      .catch(e => setGraphError(String(e)))
  }, [selectedPipeline]), { intervalMs: 5000, active: !!selectedPipeline })

  const handleSelectPipeline = useCallback((name: string) => {
    setSelectedPipeline(name)
    setGraph(undefined)
    setGraphError(undefined)
    setBundles([])
    setGraphLoading(true)
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

            {/* Bundle history — shown when more than one bundle exists */}
            {bundles.length > 1 && (
              <div style={{ marginTop: '1.5rem' }}>
                <div style={{ fontSize: '0.75rem', color: '#475569', fontWeight: 600, marginBottom: '0.5rem' }}>
                  BUNDLE HISTORY
                </div>
                <div style={{ display: 'flex', flexDirection: 'column', gap: '0.5rem' }}>
                  {bundles.map(b => (
                    <div
                      key={b.name}
                      style={{
                        background: '#1e293b',
                        borderRadius: '6px',
                        padding: '0.6rem 0.75rem',
                        display: 'flex',
                        justifyContent: 'space-between',
                        alignItems: 'center',
                        fontSize: '0.8rem',
                      }}
                    >
                      <span style={{ fontFamily: 'monospace', color: '#e2e8f0' }}>{b.name}</span>
                      <span style={{
                        background: phaseBadgeColor(b.phase),
                        color: '#fff',
                        fontSize: '0.7rem',
                        padding: '2px 6px',
                        borderRadius: '9999px',
                        fontWeight: 600,
                      }}>
                        {b.phase}
                      </span>
                    </div>
                  ))}
                </div>
              </div>
            )}
          </>
        )}
      </main>
    </div>
  )
}
