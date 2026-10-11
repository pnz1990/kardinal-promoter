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

// components/DAGTooltip.tsx — Portal-rendered rich tooltip for DAG node hover.
// Adapted from kro-ui DAGTooltip.tsx — simplified for kardinal's domain.
//
// Key behaviors:
// - React portal rendered above the SVG, never clipped by SVG bounds
// - Viewport-clamped: tooltip never renders off-screen; the position is
//   recomputed when the content changes (state, message, PR)
// - DAGView delays the hide by 150ms so the cursor can travel from node to tooltip
// - Content adapts per node type (PromotionStep vs PolicyGate)
// #526

import { displayState } from '../displayStates.gen'
import { useRef, useEffect, useState } from 'react'
import { createPortal } from 'react-dom'
import type { GraphNode } from '../types'
import { healthChipColors, kardinalStateToHealth } from './HealthChip'
import { isHttpURL } from '../prLink'
import '../styles/DAGTooltip.css'

export interface DAGTooltipTarget {
  node: GraphNode
  /** Viewport-relative bounding rect of the hovered node. */
  rect: DOMRect
}

interface Props {
  /** Tooltip target. Pass null to hide. */
  target: DAGTooltipTarget | null
  /** Called when cursor enters the tooltip (cancel hide timer). */
  onMouseEnter?: () => void
  /** Called when cursor leaves the tooltip (start hide timer). */
  onMouseLeave?: () => void
}

/** Format ISO timestamp to relative time string. */
function relativeTime(iso: string | undefined): string {
  if (!iso) return ''
  try {
    const d = new Date(iso)
    if (isNaN(d.getTime())) return iso
    const diffSec = Math.floor((Date.now() - d.getTime()) / 1000)
    if (diffSec < 60) return `${diffSec}s ago`
    if (diffSec < 3600) return `${Math.floor(diffSec / 60)}m ago`
    if (diffSec < 86400) return `${Math.floor(diffSec / 3600)}h ago`
    return d.toLocaleString(undefined, { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' })
  } catch {
    return iso
  }
}

/**
 * DAGTooltip — portal-rendered, viewport-clamped hover tooltip for DAG nodes.
 * Returns null when target is null.
 * #526
 */
export default function DAGTooltip({ target, onMouseEnter, onMouseLeave }: Props) {
  const tooltipRef = useRef<HTMLDivElement>(null)
  const [pos, setPos] = useState<{ left: number; top: number } | null>(null)
  const [visible, setVisible] = useState(false)

  useEffect(() => {
    if (!target || !tooltipRef.current) {
      setPos(null)
      setVisible(false)
      return
    }

    setVisible(false)
    const el = tooltipRef.current
    const { rect } = target

    // Position below-right of the node initially
    const initialLeft = rect.left + rect.width + 10
    const initialTop = rect.top

    const rafId = requestAnimationFrame(() => {
      const tipRect = el.getBoundingClientRect()
      const vw = window.innerWidth
      const vh = window.innerHeight
      const margin = 8

      let left = initialLeft
      let top = initialTop

      // Flip left if right edge overflows
      if (left + tipRect.width > vw - margin) {
        left = rect.left - tipRect.width - 10
      }
      if (left < margin) left = margin

      // Flip up if bottom edge overflows
      if (top + tipRect.height > vh - margin) {
        top = rect.top + rect.height - tipRect.height
      }
      if (top < margin) top = margin

      setPos({ left, top })
      setVisible(true)
    })

    return () => cancelAnimationFrame(rafId)
  }, [
    target?.node.id, target?.rect.left, target?.rect.top,
    // Content changes change the tooltip's size, so re-clamp it.
    target?.node.state, target?.node.message, target?.node.prURL,
  ])

  if (!target) return null

  const { node } = target
  const health = kardinalStateToHealth(node.state, node.type)
  const { text: stateColor } = healthChipColors(health)
  const isPolicyGate = node.type === 'PolicyGate'

  const tooltipEl = (
    <div
      ref={tooltipRef}
      data-testid="dag-tooltip"
      role="tooltip"
      onMouseEnter={onMouseEnter}
      onMouseLeave={onMouseLeave}
      className="dag-tooltip"
      data-visible={visible}
      style={{ left: pos?.left ?? 0, top: pos?.top ?? 0 }}
    >
      {/* Header: environment/gate name */}
      <div className="dag-tooltip__title">
        {isPolicyGate ? `🔒 ${node.label}` : node.environment}
      </div>

      {/* State */}
      <div className="dag-tooltip__state">
        <span className="dag-tooltip__caption">State:</span>
        <span className="dag-tooltip__state-value" style={{ color: stateColor }}>
          {node.state ? displayState(node.type === 'PolicyGate' ? 'gate' : 'step', node.state).label : '—'}
        </span>
      </div>

      {/* PromotionStep-specific */}
      {!isPolicyGate && (
        <>
          {node.message && (
            <div className="dag-tooltip__message">
              {node.message}
            </div>
          )}
          {isHttpURL(node.prURL) && (
            <div className="dag-tooltip__link-row">
              <a
                href={node.prURL}
                target="_blank"
                rel="noopener noreferrer"
                onClick={e => e.stopPropagation()}
                className="dag-tooltip__link"
              >
                View Pull Request ↗
              </a>
            </div>
          )}
        </>
      )}

      {/* PolicyGate-specific */}
      {isPolicyGate && (
        <>
          {node.expression && (
            <div className="dag-tooltip__expression">
              {node.expression}
            </div>
          )}
          {node.lastEvaluatedAt && (
            <div className="dag-tooltip__caption">
              Evaluated {relativeTime(node.lastEvaluatedAt)}
            </div>
          )}
          {node.message && (
            <div className="dag-tooltip__message dag-tooltip__message--after">
              {node.message}
            </div>
          )}
        </>
      )}
    </div>
  )

  return createPortal(tooltipEl, document.body)
}
