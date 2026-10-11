// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// stationMap/Segmented.tsx — a small radio group drawn as segments (the map's View and Zoom
// controls), the WAI-ARIA radio pattern: one Tab stop (the checked radio), arrow keys move and
// select, Home/End go to the ends. Its name prefixes each radio's ("View: Map", "Zoom: Map"), so
// two groups that share a word stay distinct, and it shows as a quiet prefix label.
import { useEffect, useRef } from 'react'

export interface SegmentedProps<T extends string> {
  /** The group's name, also the visible prefix: "View", "Zoom". */
  label: string
  items: Array<{ id: T; text: string }>
  value: T
  onChange: (id: T) => void
  className?: string
}

/** A group whose choice the keyboard just made: when the group is drawn again (the view
 *  switch moves from the map's bar to above the Table), its checked radio takes the focus. */
let pendingFocus: string | undefined

export function Segmented<T extends string>({ label, items, value, onChange, className }: SegmentedProps<T>) {
  const refs = useRef<Array<HTMLButtonElement | null>>([])
  useEffect(() => {
    if (pendingFocus !== label) return
    pendingFocus = undefined
    refs.current[items.findIndex(it => it.id === value)]?.focus()
  })
  const move = (e: React.KeyboardEvent, i: number) => {
    const last = items.length - 1
    const next = e.key === 'ArrowRight' || e.key === 'ArrowDown' ? (i === last ? 0 : i + 1)
      : e.key === 'ArrowLeft' || e.key === 'ArrowUp' ? (i === 0 ? last : i - 1)
        : e.key === 'Home' ? 0 : e.key === 'End' ? last : -1
    if (next < 0) return
    e.preventDefault()
    e.stopPropagation()
    refs.current[next]?.focus()
    pendingFocus = label
    onChange(items[next].id)
  }
  return (
    <div className={`segmented${className ? ` ${className}` : ''}`} role="radiogroup" aria-label={label}>
      <span className="segmented__label" aria-hidden="true">{label}</span>
      {items.map((it, i) => (
        <button key={it.id} ref={el => { refs.current[i] = el }} type="button" role="radio" aria-checked={it.id === value}
          aria-label={`${label}: ${it.text}`} tabIndex={it.id === value ? 0 : -1}
          className="segmented__item" onClick={() => onChange(it.id)} onKeyDown={e => move(e, i)}>
          {it.text}
        </button>
      ))}
    </div>
  )
}
