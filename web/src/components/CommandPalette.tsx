// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// CommandPalette.tsx — ⌘K / Ctrl+K: one list to go anywhere (pipelines, environments,
// versions) or do something (theme, density, refresh, shortcuts, views). Type to filter;
// ↑/↓ move, Enter runs, Esc closes. It never changes a promotion by itself: actions that
// do (pause, promote, roll back, approve) keep their own confirm dialogs. A command the
// person may not run stays listed, inert, with why.
import { useEffect, useId, useMemo, useRef, useState, type KeyboardEvent } from 'react'
import { Dialog } from '../ui'
import '../styles/CommandPalette.css'

export interface PaletteCommand {
  id: string
  /** What the row says, e.g. "checkout › prod". */
  label: string
  /** The group: Go to or Do. */
  group: 'Go to' | 'Do'
  /** A short note on the right, e.g. the namespace or a state. */
  hint?: string
  /** Extra words the search matches (the namespace, a version). */
  keywords?: string
  /** Why the command may not run for this person (U7): shown in place of running it. */
  disabledReason?: string
  run: () => void
}

export interface CommandPaletteProps {
  open: boolean
  onClose: () => void
  commands: PaletteCommand[]
}

/** Scores how well query matches text: every query character in order (lower is better), or -1. */
export function matchScore(text: string, query: string): number {
  const t = text.toLowerCase()
  const q = query.trim().toLowerCase()
  if (!q) return 0
  const direct = t.indexOf(q)
  if (direct >= 0) return direct === 0 || /[\s›/._-]/.test(t[direct - 1]) ? direct : 100 + direct
  let at = -1
  let gaps = 0
  for (const ch of q) {
    if (ch === ' ') continue
    const next = t.indexOf(ch, at + 1)
    if (next < 0) return -1
    gaps += next - at - 1
    at = next
  }
  return 1000 + gaps
}

/** The commands that match query, best first; the order given breaks ties. */
export function filterCommands(commands: PaletteCommand[], query: string): PaletteCommand[] {
  return commands
    .map((c, i) => ({ c, i, s: matchScore(`${c.label} ${c.keywords ?? ''} ${c.hint ?? ''}`, query) }))
    .filter((x) => x.s >= 0)
    .sort((a, b) => a.s - b.s || a.i - b.i)
    .map((x) => x.c)
}

const LIMIT = 50

export function CommandPalette({ open, onClose, commands }: CommandPaletteProps) {
  return open ? <PaletteBody onClose={onClose} commands={commands} /> : null
}

function PaletteBody({ onClose, commands }: Omit<CommandPaletteProps, 'open'>) {
  const [query, setQuery] = useState('')
  const [active, setActive] = useState(0)
  const inputRef = useRef<HTMLInputElement>(null)
  const listRef = useRef<HTMLUListElement>(null)
  const listId = useId()
  const matches = useMemo(() => filterCommands(commands, query), [commands, query])
  // Grouped (Go to, then Do), best first within each, so the list reads in the order ↑/↓ move.
  const shown = [...matches.filter((c) => c.group === 'Go to'), ...matches.filter((c) => c.group === 'Do')].slice(0, LIMIT)
  const current = Math.min(active, Math.max(shown.length - 1, 0))

  useEffect(() => { setActive(0) }, [query])
  useEffect(() => {
    listRef.current?.querySelector<HTMLElement>(`[data-index="${current}"]`)?.scrollIntoView?.({ block: 'nearest' })
  }, [current])

  const run = (c: PaletteCommand | undefined) => {
    if (!c || c.disabledReason) return
    onClose()
    c.run()
  }
  const onKeyDown = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key === 'ArrowDown') { e.preventDefault(); setActive((current + 1) % Math.max(shown.length, 1)) }
    else if (e.key === 'ArrowUp') { e.preventDefault(); setActive((current - 1 + shown.length) % Math.max(shown.length, 1)) }
    else if (e.key === 'Home') { e.preventDefault(); setActive(0) }
    else if (e.key === 'End') { e.preventDefault(); setActive(Math.max(shown.length - 1, 0)) }
    else if (e.key === 'Enter') { e.preventDefault(); run(shown[current]) }
  }
  const optionId = (i: number) => `${listId}-${i}`
  const count = matches.length === 0
    ? 'No matches'
    : `${matches.length} ${matches.length === 1 ? 'match' : 'matches'}${matches.length > LIMIT ? `, the first ${LIMIT} shown` : ''}`

  const groups = [...new Set(shown.map((c) => c.group))]
  return (
    <Dialog open title="Go to or do" onClose={onClose} initialFocus={inputRef} className="palette">
      <input
        ref={inputRef}
        className="palette__input"
        type="text"
        role="combobox"
        aria-expanded="true"
        aria-controls={listId}
        aria-autocomplete="list"
        aria-activedescendant={shown.length ? optionId(current) : undefined}
        aria-label="Search pipelines, environments, versions and actions"
        placeholder="Search pipelines, environments, versions and actions…"
        value={query}
        onChange={(e) => setQuery(e.target.value)}
        onKeyDown={onKeyDown}
      />
      <p className="palette__count" role="status" aria-live="polite">{count}</p>
      <ul ref={listRef} id={listId} role="listbox" aria-label="Results" className="palette__list">
        {groups.map((g) => (
          <li key={g} role="presentation" className="palette__section">
            <span className="palette__group" id={`${listId}-${g === 'Do' ? 'do' : 'go'}`}>{g}</span>
            <ul role="group" aria-labelledby={`${listId}-${g === 'Do' ? 'do' : 'go'}`} className="palette__options">
              {shown.map((c, i) => c.group !== g ? null : (
                <li
                  key={c.id}
                  id={optionId(i)}
                  data-index={i}
                  role="option"
                  aria-selected={i === current}
                  aria-disabled={c.disabledReason ? true : undefined}
                  className="palette__option"
                  onMouseMove={() => setActive(i)}
                  onClick={() => run(c)}
                >
                  <span className="palette__label">{c.label}</span>
                  {c.hint && <span className="palette__hint">{c.hint}</span>}
                  {c.disabledReason && <span className="palette__why">{c.disabledReason}</span>}
                </li>
              ))}
            </ul>
          </li>
        ))}
      </ul>
      <p className="palette__keys" aria-hidden="true">↑ ↓ to move · Enter to open · Esc to close</p>
    </Dialog>
  )
}
