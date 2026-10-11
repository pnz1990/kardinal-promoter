// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Tabs.tsx — a tab list with roving focus (arrow keys, Home, End), and its panels.
import { useRef, type KeyboardEvent, type ReactNode } from 'react'
import { Badge } from './Chips'
import { cx } from './cx'

export interface TabItem {
  id: string
  label: string
  count?: number
}

export interface TabsProps {
  value: string
  onChange: (id: string) => void
  items: TabItem[]
  /** The tab list's accessible name, e.g. "Pipeline views". */
  label: string
  className?: string
}

export const tabId = (id: string) => `k-tab-${id}`
export const panelId = (id: string) => `k-tabpanel-${id}`

export function Tabs({ value, onChange, items, label, className }: TabsProps) {
  const refs = useRef<(HTMLButtonElement | null)[]>([])
  const move = (e: KeyboardEvent, i: number) => {
    const last = items.length - 1
    const next = e.key === 'ArrowRight' ? (i === last ? 0 : i + 1)
      : e.key === 'ArrowLeft' ? (i === 0 ? last : i - 1)
        : e.key === 'Home' ? 0 : e.key === 'End' ? last : -1
    if (next < 0) return
    e.preventDefault()
    refs.current[next]?.focus()
    onChange(items[next].id)
  }
  return (
    <div role="tablist" aria-label={label} className={cx('k-tabs', className)}>
      {items.map((t, i) => (
        <button
          key={t.id}
          ref={(el) => { refs.current[i] = el }}
          type="button"
          role="tab"
          id={tabId(t.id)}
          aria-selected={t.id === value}
          aria-controls={panelId(t.id)}
          tabIndex={t.id === value ? 0 : -1}
          className="k-tabs__tab"
          onClick={() => onChange(t.id)}
          onKeyDown={(e) => move(e, i)}
        >
          {t.label}
          {t.count !== undefined && <Badge count={t.count} />}
        </button>
      ))}
    </div>
  )
}

export function TabPanel({ id, className, children }: { id: string; className?: string; children?: ReactNode }) {
  return (
    <div role="tabpanel" id={panelId(id)} aria-labelledby={tabId(id)} tabIndex={0} className={cx('k-tabpanel', className)}>
      {children}
    </div>
  )
}
