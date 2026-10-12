// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// stage/ActionButton.tsx — a drawer action's button. When the person at the
// UI may not take the action (U7), it stays in place and focusable but does
// nothing (aria-disabled), and says why: as its tooltip and to a screen
// reader. A visible note under the actions repeats it for touch screens.
import { useId } from 'react'
import type { StageActionKind } from './stageModel'

export function ActionButton({ kind, label, className, restricted, onClick }: {
  kind: StageActionKind
  label: string
  className: string
  /** Why the action is not available; undefined when it is. */
  restricted?: string
  onClick: () => void
}) {
  const id = useId()
  return (
    <>
      <button type="button" data-action={kind} className={className}
        aria-disabled={restricted ? true : undefined}
        aria-describedby={restricted ? id : undefined}
        title={restricted}
        onClick={() => { if (!restricted) onClick() }}>
        {label}
      </button>
      {restricted && <span id={id} className="sr-only">{restricted}</span>}
    </>
  )
}
