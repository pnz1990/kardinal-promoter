// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// MenuButton.tsx — an overflow of secondary actions behind one button (a disclosure, not an
// ARIA menu: its contents are ordinary buttons with their own confirm dialogs). Opening it
// focuses the first control; Esc or a click outside closes it, and focus returns to the button.
import { useEffect, useId, useRef, useState, type ReactNode } from 'react'
import { FOCUSABLE } from '../useModalFocus'
import { Icon, type IconName } from './Icon'
import { cx } from './cx'

export interface MenuButtonProps {
  /** The button's name ("More actions"). */
  label: string
  icon?: IconName
  className?: string
  children: ReactNode
}

export function MenuButton({ label, icon = 'chevron', className, children }: MenuButtonProps) {
  const [open, setOpen] = useState(false)
  const id = useId()
  const rootRef = useRef<HTMLDivElement>(null)
  const buttonRef = useRef<HTMLButtonElement>(null)
  const panelRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (!open) return
    panelRef.current?.querySelector<HTMLElement>(FOCUSABLE)?.focus()
    const onDown = (e: MouseEvent) => {
      // A dialog an action opened (rendered outside the panel) is not "outside".
      const t = e.target as Node
      if (rootRef.current?.contains(t) || (t instanceof Element && t.closest('[role="dialog"]'))) return
      setOpen(false)
    }
    document.addEventListener('mousedown', onDown)
    return () => document.removeEventListener('mousedown', onDown)
  }, [open])

  return (
    <div ref={rootRef} className={cx('k-menu', className)}>
      <button
        ref={buttonRef}
        type="button"
        className="k-button k-button--secondary k-button--md"
        aria-expanded={open}
        aria-controls={id}
        onClick={() => setOpen((o) => !o)}
      >
        {label}
        <Icon name={icon} className="k-menu__caret" />
      </button>
      <div
        ref={panelRef}
        id={id}
        role="group"
        aria-label={label}
        className="k-menu__panel"
        hidden={!open}
        onKeyDown={(e) => {
          if (e.key === 'Escape' && !document.querySelector('[aria-modal="true"]')) {
            e.stopPropagation()
            setOpen(false)
            buttonRef.current?.focus()
          }
        }}
      >
        {children}
      </div>
    </div>
  )
}
