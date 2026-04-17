// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// components/EmptyState.tsx — Improved empty state for the pipeline list.
// Replaces the plain div with an actionable onboarding card (#530).
// NNG Heuristic 10: in-context help must be immediately actionable.
import CopyButton from './CopyButton'

const QUICKSTART_COMMAND = 'kubectl apply -f https://raw.githubusercontent.com/pnz1990/kardinal-promoter/main/examples/quickstart/pipeline.yaml'
const DOCS_URL = 'https://pnz1990.github.io/kardinal-promoter/'

/** Expected output shown after applying the quickstart pipeline. */
const EXPECTED_OUTPUT = `NAME               PHASE      BUNDLE   AGE
kardinal-test-app  Promoting  —        5s`

/**
 * EmptyState — shown when no pipelines are found.
 * Provides actionable onboarding: explanation, kubectl command with copy button,
 * docs link, and expected output preview.
 * #530
 */
export default function EmptyState() {
  return (
    <div
      data-testid="empty-state"
      style={{
        padding: '2.5rem 2rem',
        maxWidth: '600px',
      }}
    >
      {/* Title */}
      <div style={{ fontSize: '1.1rem', fontWeight: 700, color: 'var(--color-text)', marginBottom: '0.5rem' }}>
        No pipelines found
      </div>

      {/* One-sentence explanation */}
      <p style={{ fontSize: '0.9rem', color: 'var(--color-text-muted)', marginBottom: '1.5rem', lineHeight: 1.5 }}>
        A <strong style={{ color: '#cbd5e1' }}>Pipeline</strong> defines your promotion environments
        (test → uat → prod) and the policy gates between them.{' '}
        <a
          href={DOCS_URL}
          target="_blank"
          rel="noopener noreferrer"
          data-testid="docs-link"
          style={{ color: '#6366f1', textDecoration: 'underline' }}
        >
          Read the docs ↗
        </a>
      </p>

      {/* Quickstart command */}
      <div style={{ marginBottom: '1rem' }}>
        <div style={{ fontSize: '0.75rem', color: '#64748b', marginBottom: '0.4rem', fontWeight: 600 }}>
          Apply the quickstart example:
        </div>
        <div style={{
          display: 'flex',
          alignItems: 'center',
          gap: '8px',
          background: 'var(--color-bg)',
          border: '1px solid #1e293b',
          borderRadius: '6px',
          padding: '8px 12px',
        }}>
          <code
            data-testid="quickstart-command"
            style={{
              flex: 1,
              fontSize: '0.75rem',
              color: 'var(--color-text-muted)',
              fontFamily: 'monospace',
              wordBreak: 'break-all',
              lineHeight: 1.5,
            }}
          >
            {QUICKSTART_COMMAND}
          </code>
          <CopyButton text={QUICKSTART_COMMAND} title="Copy kubectl apply command" />
        </div>
      </div>

      {/* Expected output */}
      <div style={{ marginBottom: '1.5rem' }}>
        <div style={{ fontSize: '0.75rem', color: '#64748b', marginBottom: '0.4rem', fontWeight: 600 }}>
          Then run <code style={{ color: '#7dd3fc', fontFamily: 'monospace' }}>kubectl get pipelines</code>:
        </div>
        <pre style={{
          background: 'var(--color-bg)',
          border: '1px solid #1e293b',
          borderRadius: '6px',
          padding: '8px 12px',
          fontSize: '0.75rem',
          color: '#64748b',
          fontFamily: 'monospace',
          margin: 0,
          lineHeight: 1.5,
          overflow: 'auto',
        }}>
          {EXPECTED_OUTPUT}
        </pre>
      </div>

      {/* Polling status */}
      <div
        data-testid="watching-indicator"
        style={{ fontSize: '0.75rem', color: 'var(--color-text-faint)', display: 'flex', alignItems: 'center', gap: '6px' }}
      >
        <span style={{ animation: 'spin 2s linear infinite', display: 'inline-block' }}>◌</span>
        Watching for new pipelines…
        <style>{`@keyframes spin { from { transform: rotate(0deg) } to { transform: rotate(360deg) } }`}</style>
      </div>
    </div>
  )
}
