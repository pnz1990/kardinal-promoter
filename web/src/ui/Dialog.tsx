// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Dialog.tsx — Dialog, ConfirmDialog and Drawer. A modal traps focus (useModalFocus),
// closes on Esc and on a click outside, and gives focus back to what opened it.
import { forwardRef, useId, useRef, type CSSProperties, type ForwardedRef, type ReactNode, type RefObject } from 'react'
import { useModalFocus } from '../useModalFocus'
import { Button, IconButton } from './Button'
import { cx, mergeRefs } from './cx'

export interface DialogProps {
  open: boolean
  title: string
  description?: ReactNode
  onClose: () => void
  /** The control focused first; the first focusable one when unset. */
  initialFocus?: RefObject<HTMLElement | null>
  /** The buttons, right-aligned under the content. */
  footer?: ReactNode
  /** While true, Esc and a click outside do not close it. */
  busy?: boolean
  className?: string
  children?: ReactNode
}

/** The ref is the dialog element (role="dialog"). */
export const Dialog = forwardRef<HTMLDivElement, DialogProps>(function Dialog(props, ref) {
  if (!props.open) return null
  return <DialogBody {...props} forwardedRef={ref} />
})

function DialogBody({ title, description, onClose, initialFocus, footer, busy, className, children, forwardedRef }: DialogProps & { forwardedRef?: ForwardedRef<HTMLDivElement> }) {
  const panelRef = useRef<HTMLDivElement>(null)
  const titleId = useId()
  const descId = useId()
  const close = () => { if (!busy) onClose() }
  useModalFocus(panelRef, close, initialFocus)
  return (
    <div className="k-scrim" onClick={(e) => { if (e.target === e.currentTarget) close() }}>
      <div
        ref={mergeRefs(panelRef, forwardedRef)}
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        aria-describedby={description ? descId : undefined}
        className={cx('k-dialog', className)}
      >
        <h2 id={titleId} className="k-dialog__title">{title}</h2>
        {description && <div id={descId} className="k-dialog__description">{description}</div>}
        {children}
        {footer && <div className="k-dialog__footer">{footer}</div>}
      </div>
    </div>
  )
}

export interface ConfirmDialogProps {
  open: boolean
  /** The question, e.g. "Roll back prod?". */
  title: string
  /** What happens when the user confirms. */
  body: ReactNode
  /** The confirm button: the action's verb ("Roll back prod"). */
  confirmLabel: string
  tone?: 'default' | 'destructive'
  onConfirm: () => void
  onCancel: () => void
  /** The request is running: both buttons are disabled. */
  busy?: boolean
  /** What failed and how to fix it, above the buttons. */
  error?: string
  children?: ReactNode
}

export function ConfirmDialog({ open, title, body, confirmLabel, tone = 'default', onConfirm, onCancel, busy, error, children }: ConfirmDialogProps) {
  const cancelRef = useRef<HTMLButtonElement>(null)
  return (
    <Dialog
      open={open}
      title={title}
      description={body}
      onClose={onCancel}
      initialFocus={cancelRef}
      busy={busy}
      footer={
        <>
          <Button ref={cancelRef} variant="ghost" onClick={onCancel} disabled={busy}>Cancel</Button>
          <Button variant={tone === 'destructive' ? 'destructive' : 'primary'} onClick={onConfirm} busy={busy} aria-label={confirmLabel}>
            {busy ? 'Working…' : confirmLabel}
          </Button>
        </>
      }
    >
      {children}
      {error && <div role="alert" className="k-alert k-alert--failed">{error}</div>}
    </Dialog>
  )
}

export interface DrawerProps {
  open: boolean
  title: ReactNode
  /** The accessible name when the title is not plain text. */
  label?: string
  onClose: () => void
  /** Modal (focus trapped, scrim): narrow layouts. Otherwise a complementary region beside the page. */
  modal?: boolean
  /** A CSS width; the --k-layout-drawer token by default. */
  width?: string
  className?: string
  children?: ReactNode
}

/** The ref is the drawer element (the <aside>). */
export const Drawer = forwardRef<HTMLElement, DrawerProps>(function Drawer(props, ref) {
  if (!props.open) return null
  return props.modal ? <ModalDrawer {...props} forwardedRef={ref} /> : <DrawerPanel {...props} forwardedRef={ref} />
})

type DrawerPanelProps = DrawerProps & { forwardedRef?: ForwardedRef<HTMLElement> }

function DrawerPanel({ title, label, onClose, className, children, width, modal, forwardedRef }: DrawerPanelProps) {
  const titleId = useId()
  return (
    <aside
      ref={forwardedRef}
      role={modal ? 'dialog' : 'complementary'}
      aria-modal={modal || undefined}
      aria-labelledby={label ? undefined : titleId}
      aria-label={label}
      className={cx('k-drawer', className)}
      style={width ? ({ '--drawer-width': width } as CSSProperties) : undefined}
    >
      <header className="k-drawer__header">
        <h2 id={titleId} className="k-drawer__title">{title}</h2>
        <IconButton label="Close" icon="close" size="sm" onClick={onClose} />
      </header>
      <div className="k-drawer__body">{children}</div>
    </aside>
  )
}

function ModalDrawer(props: DrawerPanelProps) {
  const ref = useRef<HTMLDivElement>(null)
  useModalFocus(ref, props.onClose)
  return (
    <div className="k-scrim k-scrim--drawer" onClick={(e) => { if (e.target === e.currentTarget) props.onClose() }}>
      <div ref={ref} className="k-drawer-frame"><DrawerPanel {...props} /></div>
    </div>
  )
}
