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

// components/BundleTimelineTable.tsx — Detailed bundle promotion timeline (#466).
// Shows one row per bundle: version chip, per-env status chips with PR links,
// promoted-by, promoted-at relative time, override indicator. Pagination: 20/page.
// Superseded/Failed bundles dimmed. "Load more" expands to show all.
import { useState } from 'react'
import type { Bundle } from '../types'
import { HealthChip } from './HealthChip'

interface Props {
  /** Bundles for this pipeline — managed by parent poll. */
  bundles: Bundle[]
  /** Initial page size — first N bundles shown. */
  pageSize?: number
}

const DEFAULT_PAGE_SIZE = 20

/** #503: Sort bundles newest-first by createdAt, fallback to name. */
export function sortBundlesNewestFirst(bundles: Bundle[]): Bundle[] {
  return [...bundles].sort((a, b) => {
    if (a.createdAt && b.createdAt) {
      return a.createdAt > b.createdAt ? -1 : a.createdAt < b.createdAt ? 1 : 0
    }
    return a.name > b.name ? -1 : 1
  })
}

/** #503: Return true if a bundle should be dimmed (terminal non-success states). */
export function isBundleDimmed(phase: string): boolean {
  return phase === 'Superseded' || phase === 'Failed'
}

/** #503: Format relative age from ISO string. */
export function formatBundleAge(iso: string | undefined): string {
  if (!iso) return '—'
  try {
    const d = new Date(iso)
    if (isNaN(d.getTime())) return '—'
    const diffSec = Math.floor((Date.now() - d.getTime()) / 1000)
    if (diffSec < 60) return `${diffSec}s ago`
    if (diffSec < 3600) return `${Math.floor(diffSec / 60)}m ago`
    if (diffSec < 86400) return `${Math.floor(diffSec / 3600)}h ago`
    return `${Math.floor(diffSec / 86400)}d ago`
  } catch { return '—' }
}

/** #503: Short version label from bundle name (last segment). */
export function shortBundleVersion(name: string): string {
  const parts = name.split('-')
  if (parts.length >= 2) {
    // Return last 2 segments for readability, e.g. "sha-9349a3f"
    return parts.slice(-2).join('-')
  }
  return name.slice(-8)
}

const phaseChipColor: Record<string, { bg: string; text: string; border: string }> = {
  Verified:      { bg: '#14532d', text: '#86efac', border: '#16a34a' },
  Promoting:     { bg: '#1e1b4b', text: 'var(--color-accent)', border: '#4338ca' },
  WaitingForMerge: { bg: '#1e1b4b', text: 'var(--color-accent)', border: '#4338ca' },
  HealthChecking:{ bg: '#1c1917', text: '#d6b4fc', border: '#7c3aed' },
  Failed:        { bg: '#7f1d1d', text: '#fca5a5', border: '#dc2626' },
  Pending:       { bg: 'var(--color-bg)', text: 'var(--color-text-faint)', border: 'var(--color-border)' },
}

function EnvChip({ name, phase, prURL }: { name: string; phase?: string; prURL?: string }) {
  const colors = phaseChipColor[phase ?? ''] ?? { bg: 'var(--color-bg)', text: 'var(--color-text-faint)', border: 'var(--color-border)' }
  const chip = (
    <span
      title={`${name}: ${phase ?? 'Pending'}${prURL ? '\n' + prURL : ''}`}
      style={{
        display: 'inline-flex',
        alignItems: 'center',
        gap: '3px',
        fontSize: '0.65rem',
        background: colors.bg,
        color: colors.text,
        border: `1px solid ${colors.border}`,
        borderRadius: '3px',
        padding: '1px 5px',
        fontFamily: 'monospace',
        cursor: prURL ? 'pointer' : 'default',
        textDecoration: 'none',
      }}
    >
      {name}
      {prURL && <span style={{ fontSize: '0.6em', opacity: 0.7 }}>↗</span>}
    </span>
  )
  if (prURL) {
    return (
      <a href={prURL} target="_blank" rel="noopener noreferrer" style={{ textDecoration: 'none' }}
        aria-label={`Open PR for ${name}`}>
        {chip}
      </a>
    )
  }
  return chip
}

export function BundleTimelineTable({ bundles, pageSize = DEFAULT_PAGE_SIZE }: Props) {
  const [showAll, setShowAll] = useState(false)
  const sorted = sortBundlesNewestFirst(bundles)
  const visible = showAll ? sorted : sorted.slice(0, pageSize)
  const hasMore = sorted.length > pageSize && !showAll

  if (sorted.length === 0) {
    return (
      <div style={{ padding: '1rem', color: 'var(--color-text-faint)', fontSize: '0.8rem', textAlign: 'center' }}>
        No bundles promoted yet.
      </div>
    )
  }

  return (
    <div>
      <table style={{
        width: '100%',
        borderCollapse: 'collapse',
        fontSize: '0.78rem',
      }}>
        <thead>
          <tr style={{ borderBottom: '1px solid #1e293b' }}>
            <th style={{ padding: '0.4rem 0.75rem', textAlign: 'left', color: 'var(--color-text-faint)', fontSize: '0.65rem', fontWeight: 600, textTransform: 'uppercase', letterSpacing: '0.04em' }}>
              Version
            </th>
            <th style={{ padding: '0.4rem 0.75rem', textAlign: 'left', color: 'var(--color-text-faint)', fontSize: '0.65rem', fontWeight: 600, textTransform: 'uppercase', letterSpacing: '0.04em' }}>
              Environments
            </th>
            <th style={{ padding: '0.4rem 0.75rem', textAlign: 'left', color: 'var(--color-text-faint)', fontSize: '0.65rem', fontWeight: 600, textTransform: 'uppercase', letterSpacing: '0.04em' }}>
              Author
            </th>
            <th style={{ padding: '0.4rem 0.75rem', textAlign: 'right', color: 'var(--color-text-faint)', fontSize: '0.65rem', fontWeight: 600, textTransform: 'uppercase', letterSpacing: '0.04em' }}>
              Age
            </th>
          </tr>
        </thead>
        <tbody>
          {visible.map(b => {
            const dimmed = isBundleDimmed(b.phase)
            const version = shortBundleVersion(b.name)
            return (
              <tr
                key={b.name}
                style={{
                  borderBottom: '1px solid #0f172a',
                  opacity: dimmed ? 0.45 : 1,
                }}
              >
                <td style={{ padding: '0.4rem 0.75rem', verticalAlign: 'middle' }}>
                  <div style={{ display: 'flex', alignItems: 'center', gap: '0.4rem' }}>
                    <HealthChip state={b.phase} size="sm" />
                    <span style={{
                      fontFamily: 'monospace',
                      fontSize: '0.75rem',
                      color: dimmed ? 'var(--color-text-faint)' : 'var(--color-text)',
                    }}>
                      {version}
                    </span>
                  </div>
                </td>
                <td style={{ padding: '0.4rem 0.75rem', verticalAlign: 'middle' }}>
                  <div style={{ display: 'flex', gap: '0.25rem', flexWrap: 'wrap' }}>
                    {b.environments && b.environments.length > 0
                      ? b.environments.map(env => (
                          <EnvChip key={env.name} name={env.name} phase={env.phase} prURL={env.prURL} />
                        ))
                      : <span style={{ color: 'var(--color-border)', fontSize: '0.7rem' }}>—</span>
                    }
                  </div>
                </td>
                <td style={{ padding: '0.4rem 0.75rem', verticalAlign: 'middle' }}>
                  <span style={{ color: dimmed ? 'var(--color-border)' : '#64748b', fontSize: '0.72rem' }}>
                    {b.provenance?.author ?? '—'}
                  </span>
                </td>
                <td style={{ padding: '0.4rem 0.75rem', verticalAlign: 'middle', textAlign: 'right' }}>
                  <span
                    title={b.createdAt}
                    style={{ color: 'var(--color-text-faint)', fontSize: '0.7rem', whiteSpace: 'nowrap' }}
                  >
                    {formatBundleAge(b.createdAt)}
                  </span>
                </td>
              </tr>
            )
          })}
        </tbody>
      </table>

      {/* Load more */}
      {hasMore && (
        <div style={{ padding: '0.5rem 0.75rem', borderTop: '1px solid #1e293b' }}>
          <button
            onClick={() => setShowAll(true)}
            style={{
              background: 'none',
              border: 'none',
              color: '#6366f1',
              cursor: 'pointer',
              fontSize: '0.75rem',
              padding: '0',
            }}
            aria-label={`Show ${sorted.length - pageSize} more bundles`}
          >
            + {sorted.length - pageSize} more bundles
          </button>
        </div>
      )}
    </div>
  )
}
