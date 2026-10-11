// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// KubernetesDetails.tsx — the one place the UI names Kubernetes kinds. The rest of the UI
// says stage, gate and version (the glossary, UX-AUDIT.md §4.3); a person who needs the
// object to run kubectl opens this disclosure.
import type { ReactNode } from 'react'
import { cx } from './cx'

export interface KubernetesDetailsProps {
  /** The Kubernetes kind, e.g. PromotionStep. */
  kind: string
  name: string
  namespace?: string
  /** More rows (label, value) after kind, name and namespace. */
  extra?: Array<[string, ReactNode]>
  className?: string
}

export function KubernetesDetails({ kind, name, namespace, extra = [], className }: KubernetesDetailsProps) {
  const rows: Array<[string, ReactNode]> = [['Kind', kind], ['Name', name], ...(namespace ? [['Namespace', namespace] as [string, ReactNode]] : []), ...extra]
  return (
    <details className={cx('k-k8s', className)}>
      <summary className="k-k8s__summary">Kubernetes details</summary>
      <dl className="k-k8s__list">
        {rows.map(([label, value]) => (
          <div key={label} className="k-k8s__row">
            <dt>{label}</dt>
            <dd>{value}</dd>
          </div>
        ))}
      </dl>
    </details>
  )
}
