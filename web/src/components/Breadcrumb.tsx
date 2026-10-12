// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Breadcrumb.tsx — where you are: Pipelines › <namespace> › <pipeline> › <environment or
// version>. Each step but the last is a link to its route; the last is the current page.
import { serializeHash, EMPTY_URL_STATE, type UrlState } from '../useUrlState'
import '../styles/Breadcrumb.css'

export interface BreadcrumbProps {
  route: UrlState
  /** The namespace when the route does not name it (old links). */
  namespace?: string
}

export function Breadcrumb({ route, namespace }: BreadcrumbProps) {
  const ns = route.ns ?? namespace
  const keep = { filter: route.filter, q: route.q }
  const crumbs: { label: string; href?: string; kind?: string }[] = [
    { label: 'Pipelines', href: serializeHash({ ...EMPTY_URL_STATE, ...keep }) },
  ]
  if (route.pipeline) {
    if (ns) crumbs.push({ label: ns, kind: 'namespace' })
    const pipelineHref = serializeHash({ ...EMPTY_URL_STATE, ...keep, pipeline: route.pipeline, ns })
    crumbs.push({ label: route.pipeline, href: route.env || route.version ? pipelineHref : undefined })
    if (route.env) crumbs.push({ label: route.env, kind: 'environment' })
    else if (route.version) crumbs.push({ label: route.version, kind: 'version' })
  }
  if (!route.pipeline && route.approvals) crumbs.push({ label: 'Approvals' })
  return (
    <nav aria-label="Breadcrumb" className="breadcrumb">
      <ol className="breadcrumb__list">
        {crumbs.map((c, i) => {
          const last = i === crumbs.length - 1
          return (
            <li key={`${i}-${c.label}`} className="breadcrumb__item" data-kind={c.kind}>
              {c.href && !last
                ? <a href={c.href} className="breadcrumb__link">{c.label}</a>
                : <span className="breadcrumb__here" aria-current={last ? 'page' : undefined}>{c.label}</span>}
            </li>
          )
        })}
      </ol>
    </nav>
  )
}
