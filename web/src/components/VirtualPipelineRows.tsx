// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// components/VirtualPipelineRows.tsx — the sidebar's virtual scrolling (#815) for flat lists of
// more than 50 pipelines. A chunk of its own (with @tanstack/react-virtual), so the first load of
// a cluster with fewer pipelines does not carry it; PipelineList shows the first rows in place
// while it loads.
import type { ReactNode, RefObject } from 'react'
import { useVirtualizer } from '@tanstack/react-virtual'
import type { Pipeline } from '../types'

/** A row's estimated height. */
const ROW_ESTIMATE = 52

interface Props {
  pipelines: Pipeline[]
  scroller: RefObject<HTMLDivElement | null>
  row: (p: Pipeline) => ReactNode
}

export default function VirtualPipelineRows({ pipelines, scroller, row }: Props) {
  const virtualizer = useVirtualizer({
    count: pipelines.length,
    getScrollElement: () => scroller.current,
    estimateSize: () => ROW_ESTIMATE,
    overscan: 5,
  })
  return (
    <ul
      role="list"
      aria-label="Pipelines"
      className="pipeline-list__list pipeline-list__list--virtual"
      style={{ height: `${virtualizer.getTotalSize()}px` }}
    >
      {virtualizer.getVirtualItems().map(virtualItem => {
        const p = pipelines[virtualItem.index]
        if (!p) return null
        return (
          <li
            key={virtualItem.key}
            className="pipeline-list__item pipeline-list__item--virtual"
            style={{ transform: `translateY(${virtualItem.start}px)` }}
            data-index={virtualItem.index}
            ref={virtualizer.measureElement}
          >
            {row(p)}
          </li>
        )
      })}
    </ul>
  )
}
