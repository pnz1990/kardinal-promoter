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
//
// components/EmptyState.tsx — Onboarding card shown when no pipelines exist (#530).
//
// Replaces the bare empty state in App.tsx with:
// - One-sentence explanation of what a Pipeline is
// - Copyable kubectl apply command
// - Link to quickstart docs
// - Animated "watching for new pipelines..." indicator
import { CopyButton } from './CopyButton'

const QUICKSTART_CMD = 'kubectl apply -f examples/quickstart/pipeline.yaml'
const DOCS_URL = 'https://pnz1990.github.io/kardinal-promoter/'

/**
 * EmptyState renders an onboarding card when no pipelines have been created.
 */
export function EmptyState() {
  return (
    <div
      style={{
        display: 'flex',
        flexDirection: 'column',
        alignItems: 'center',
        justifyContent: 'center',
        height: '100%',
        padding: '3rem 2rem',
        textAlign: 'center',
      }}
      data-testid="empty-state"
    >
      <div style={{
        maxWidth: '520px',
        background: '#0f172a',
        border: '1px solid #1e293b',
        borderRadius: '10px',
        padding: '2rem',
      }}>
        {/* Title */}
        <div style={{ fontSize: '1.5rem', marginBottom: '0.75rem' }}>
          🚀
        </div>
        <h2 style={{ fontSize: '1.1rem', fontWeight: 700, color: '#e2e8f0', marginBottom: '0.6rem' }}>
          No pipelines yet
        </h2>

        {/* One-sentence explanation */}
        <p style={{ fontSize: '0.87rem', color: '#94a3b8', marginBottom: '1.5rem', lineHeight: 1.6 }}
           data-testid="empty-state-description">
          A Pipeline defines your promotion environments (test → uat → prod) and the
          policy gates between them. Create one to start promoting.
        </p>

        {/* Copyable kubectl command */}
        <div style={{
          display: 'flex',
          alignItems: 'center',
          gap: '0.5rem',
          background: '#1e293b',
          border: '1px solid #334155',
          borderRadius: '6px',
          padding: '0.6rem 0.75rem',
          marginBottom: '1.25rem',
          textAlign: 'left',
        }}>
          <code style={{
            flex: 1,
            fontSize: '0.8rem',
            color: '#7dd3fc',
            fontFamily: 'monospace',
            wordBreak: 'break-all',
          }}
          data-testid="quickstart-command">
            {QUICKSTART_CMD}
          </code>
          <CopyButton
            text={QUICKSTART_CMD}
            size="md"
            aria-label="Copy quickstart command"
          />
        </div>

        {/* Docs link */}
        <div style={{ marginBottom: '1.5rem' }}>
          <a
            href={DOCS_URL}
            target="_blank"
            rel="noopener noreferrer"
            style={{ color: '#6366f1', fontSize: '0.85rem', textDecoration: 'none' }}
            data-testid="docs-link"
          >
            Read the quickstart docs ↗
          </a>
        </div>

        {/* Watching spinner */}
        <div style={{
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'center',
          gap: '0.4rem',
          fontSize: '0.75rem',
          color: '#475569',
        }}
        data-testid="watching-indicator">
          <span style={{ animation: 'spin 2s linear infinite', display: 'inline-block' }}>↺</span>
          Watching for new pipelines…
        </div>
      </div>

      <style>{`
        @keyframes spin {
          from { transform: rotate(0deg); }
          to { transform: rotate(360deg); }
        }
      `}</style>
    </div>
  )
}
