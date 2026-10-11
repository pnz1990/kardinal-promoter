// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// components/BundleDiffPanel.tsx — Side-by-side bundle field comparison.
// #338: Shows key differences between two Bundle objects. Triggered by
// shift-clicking a bundle in BundleTimeline and pressing 'Compare'.
// Compares what a bundle deploys (images) and where it got to (environments),
// plus its provenance. Created is shown but never counts as a difference:
// two bundles are always created at different times.
//
// Adapted from kro-ui InstanceTable.tsx SpecDiffPanel pattern.
import { displayState } from '../displayStates.gen'
import { useRef } from 'react'
import type { Bundle, ImageRef } from '../types'
import { useModalFocus } from '../useModalFocus'
import { isCIRunURL } from '../prLink'
import '../ui'
import '../styles/BundleDiffPanel.css'

interface Props {
  bundleA: Bundle
  bundleB: Bundle
  onClose: () => void
}

type FieldRow = {
  label: string
  valueA: string | null
  valueB: string | null
  /** If true, highlight this row as changed */
  changed: boolean
  /** Show the whole value, one item per line (images, environments). */
  full?: boolean
}

function imageRef(i: ImageRef): string {
  return `${i.repository ?? ''}${i.tag ? `:${i.tag}` : ''}${i.digest ? `@${i.digest}` : ''}`
}

function imagesOf(b: Bundle): string | null {
  const refs = (b.images ?? []).map(imageRef).filter(Boolean)
  return refs.length ? refs.join('\n') : null
}

function environmentsOf(b: Bundle): string | null {
  const envs = (b.environments ?? []).map(e => `${e.name}: ${displayState('step', e.phase || 'Pending').label}`)
  return envs.length ? envs.join('\n') : null
}

function truncate(s: string | undefined, max = 40): string {
  if (!s) return '—'
  return s.length > max ? s.slice(0, max - 1) + '…' : s
}

export function buildRows(a: Bundle, b: Bundle): FieldRow[] {
  const imagesA = imagesOf(a)
  const imagesB = imagesOf(b)
  const envsA = environmentsOf(a)
  const envsB = environmentsOf(b)
  const rows: FieldRow[] = [
    { label: 'Images', valueA: imagesA, valueB: imagesB, changed: imagesA !== imagesB, full: true },
    { label: 'Environments', valueA: envsA, valueB: envsB, changed: envsA !== envsB, full: true },
    {
      label: 'State',
      valueA: a.phase ? displayState('bundle', a.phase).label : null,
      valueB: b.phase ? displayState('bundle', b.phase).label : null,
      changed: a.phase !== b.phase,
    },
    {
      label: 'Type',
      valueA: a.type ?? null,
      valueB: b.type ?? null,
      changed: a.type !== b.type,
    },
    {
      label: 'Created',
      valueA: a.createdAt ? new Date(a.createdAt).toLocaleString() : null,
      valueB: b.createdAt ? new Date(b.createdAt).toLocaleString() : null,
      changed: false,
    },
    {
      label: 'Author',
      valueA: a.provenance?.author ?? null,
      valueB: b.provenance?.author ?? null,
      changed: a.provenance?.author !== b.provenance?.author,
    },
    {
      label: 'Commit SHA',
      valueA: a.provenance?.commitSHA ? a.provenance.commitSHA.slice(0, 12) : null,
      valueB: b.provenance?.commitSHA ? b.provenance.commitSHA.slice(0, 12) : null,
      changed: a.provenance?.commitSHA !== b.provenance?.commitSHA,
    },
    {
      label: 'CI Run',
      valueA: a.provenance?.ciRunURL ?? null,
      valueB: b.provenance?.ciRunURL ?? null,
      changed: a.provenance?.ciRunURL !== b.provenance?.ciRunURL,
    },
  ]
  return rows
}

export function BundleDiffPanel({ bundleA, bundleB, onClose }: Props) {
  const rows = buildRows(bundleA, bundleB)
  const changedCount = rows.filter(r => r.changed).length
  const panelRef = useRef<HTMLDivElement>(null)
  useModalFocus(panelRef, onClose)

  return (
    <div
      className="k-scrim"
      onClick={onClose}
      role="dialog"
      aria-modal="true"
      aria-label="Version comparison"
    >
      <div ref={panelRef} onClick={e => e.stopPropagation()} className="k-dialog bundle-diff">
        {/* Header */}
        <div className="bundle-diff__header">
          <div>
            <h2 className="bundle-diff__title">Compare versions</h2>
            <p className="bundle-diff__summary">
              {changedCount === 1 ? '1 field differs' : `${changedCount} fields differ`} between these versions
            </p>
          </div>
          <button type="button" onClick={onClose} aria-label="Close comparison" className="k-icon-button k-icon-button--sm">×</button>
        </div>

        {/* Column headers */}
        <div className="bundle-diff__row bundle-diff__row--heads">
          <div />
          <div className="bundle-diff__col-head">Version A</div>
          <div className="bundle-diff__col-head">Version B</div>
        </div>

        {/* Bundle names */}
        <div className="bundle-diff__row bundle-diff__row--names">
          <span className="bundle-diff__label">Name</span>
          <span className="bundle-diff__name" title={bundleA.name}>{truncate(bundleA.name, 28)}</span>
          <span className="bundle-diff__name" title={bundleB.name}>{truncate(bundleB.name, 28)}</span>
        </div>

        {/* Diff rows */}
        {rows.map(row => (
          <div key={row.label} className="bundle-diff__row bundle-diff__row--field" data-changed={row.changed || undefined}>
            <span className="bundle-diff__label">
              {row.changed ? '▸ ' : ''}{row.label}
            </span>
            <DiffCell value={row.valueA} changed={row.changed} full={row.full} isLink={row.label === 'CI Run'} />
            <DiffCell value={row.valueB} changed={row.changed} full={row.full} isLink={row.label === 'CI Run'} />
          </div>
        ))}

        {changedCount === 0 && (
          <p className="bundle-diff__same">
            ✓ No differences found between these versions.
          </p>
        )}

        <div className="bundle-diff__footer">
          <button type="button" onClick={onClose} className="k-button k-button--secondary k-button--md">
            Close
          </button>
        </div>
      </div>
    </div>
  )
}

function DiffCell({ value, changed, isLink, full }: { value: string | null; changed: boolean; isLink?: boolean; full?: boolean }) {
  // A CI run URL that isCIRunURL rejects is not shown: it can hold credentials.
  if (!value || (isLink && !isCIRunURL(value))) {
    return <span className="bundle-diff__value bundle-diff__value--none">—</span>
  }
  if (full) {
    return (
      <span className="bundle-diff__value bundle-diff__value--full" data-changed={changed || undefined}>
        {value}
      </span>
    )
  }
  if (isLink) {
    return (
      <a href={value} target="_blank" rel="noopener noreferrer" className="bundle-diff__value bundle-diff__value--link" title={value}>
        {truncate(value, 30)}
      </a>
    )
  }
  return (
    <span className="bundle-diff__value" data-changed={changed || undefined} title={value}>
      {truncate(value, 30)}
    </span>
  )
}
