// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// components/PipelineTabs.tsx — the pipeline page's views (DESIGN-SYSTEM §3.5):
// Map (the station map and the bundle rail), Gates, History and Metrics.
// Arrow keys move between tabs (WAI-ARIA tabs, automatic activation).

import { useRef } from 'react'
import '../styles/PipelineTabs.css'

export type PipelineTab = 'map' | 'gates' | 'history' | 'metrics'

export const PIPELINE_TABS: PipelineTab[] = ['map', 'gates', 'history', 'metrics']

export const PIPELINE_TAB_LABEL: Record<PipelineTab, string> = { map: 'Map', gates: 'Gates', history: 'History', metrics: 'Metrics' }

/** A tab from the URL, the map when unknown. */
export function pipelineTabOf(value: string | undefined): PipelineTab {
  return (PIPELINE_TABS as string[]).includes(value ?? '') ? value as PipelineTab : 'map'
}

/** One tab: the API of the U5 <Tabs> primitive (UI-PRIMITIVES.md), which replaces this component. */
export interface TabItem<T extends string> {
  id: T
  label: string
  count?: number
  /** The count's tone: "blocked" shows it as a blocking count. */
  countTone?: 'blocked'
  countLabel?: string
}

interface Props {
  value: PipelineTab
  onChange: (tab: PipelineTab) => void
  items: Array<TabItem<PipelineTab>>
  label: string
}

export default function PipelineTabs({ value: tab, onChange: onTab, items, label }: Props) {
  const refs = useRef<Partial<Record<PipelineTab, HTMLButtonElement | null>>>({})
  const move = (to: number) => {
    const next = PIPELINE_TABS[(to + PIPELINE_TABS.length) % PIPELINE_TABS.length]
    onTab(next)
    refs.current[next]?.focus()
  }
  return (
    <div className="pipeline-tabs" role="tablist" aria-label={label}
      onKeyDown={e => {
        const i = PIPELINE_TABS.indexOf(tab)
        if (e.key === 'ArrowRight') { e.preventDefault(); move(i + 1) }
        else if (e.key === 'ArrowLeft') { e.preventDefault(); move(i - 1) }
        else if (e.key === 'Home') { e.preventDefault(); move(0) }
        else if (e.key === 'End') { e.preventDefault(); move(PIPELINE_TABS.length - 1) }
      }}>
      {items.map(({ id: t, label: text, count, countTone, countLabel }) => (
        <button key={t} type="button" role="tab" id={`pipeline-tab-${t}-tab`} aria-controls={`pipeline-tab-${t}`}
          aria-selected={tab === t} tabIndex={tab === t ? 0 : -1} className="pipeline-tabs__tab"
          ref={el => { refs.current[t] = el }} onClick={() => onTab(t)}>
          {text}
          {count !== undefined && count > 0 && (
            <span className="pipeline-tabs__count" data-tone={countTone}>{countLabel ?? count}</span>
          )}
        </button>
      ))}
    </div>
  )
}
