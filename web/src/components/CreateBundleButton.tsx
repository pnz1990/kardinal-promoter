// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// components/CreateBundleButton.tsx — the "Ship a version" button. The dialog it opens
// (CreateBundleDialog) is a chunk of its own, loaded on the first open, so the pipeline page's
// first load does not carry the form.
import { lazy, Suspense, useState, useEffect, useId, useRef } from 'react'

const CreateBundleDialog = lazy(() => import('./CreateBundleDialog').then(m => ({ default: m.CreateBundleDialog })))

interface CreateBundleButtonProps {
  /** Pipeline name. */
  pipelineName: string
  /** Pipeline namespace. */
  namespace: string
  /** Called after a Bundle is successfully created — triggers a re-poll. */
  onRefresh: () => void
  /** primary, the page's main action; secondary while the pipeline is paused (Resume is). */
  variant?: 'primary' | 'secondary'
  /** The button's words (default "Ship a version"); its name adds "of <pipeline>". */
  label?: string
  /** Why the caller may not ship (U7: the role that grants it); the button is then inert. */
  restricted?: string
  /** Opens the dialog when it changes (the s shortcut); 0 does nothing. */
  openSignal?: number
}

/**
 * CreateBundleButton renders the "Ship a version" button (the primary action) in the pipeline header.
 * Clicking opens the CreateBundleDialog.
 */
export function CreateBundleButton({ pipelineName, namespace, onRefresh, variant = 'primary', label = 'Ship a version', restricted, openSignal = 0 }: CreateBundleButtonProps) {
  const [showDialog, setShowDialog] = useState(false)
  const restrictedId = useId()
  const buttonRef = useRef<HTMLButtonElement>(null)
  useEffect(() => {
    if (openSignal > 0 && !restricted) {
      // Focus returns to the button when the dialog closes, as after a click.
      buttonRef.current?.focus()
      setShowDialog(true)
    }
  }, [openSignal]) // restricted is read when the signal changes

  return (
    <>
      <button
        ref={buttonRef}
        type="button"
        onClick={() => { if (!restricted) setShowDialog(true) }}
        aria-disabled={restricted ? true : undefined}
        aria-describedby={restricted ? restrictedId : undefined}
        title={restricted}
        aria-label={`${label} of ${pipelineName}`}
        className={`k-button k-button--${variant} k-button--md`}
      >
        {label}
      </button>
      {restricted && <span id={restrictedId} className="sr-only">{restricted}</span>}

      {showDialog && (<Suspense fallback={null}>
        <CreateBundleDialog
          pipelineName={pipelineName}
          namespace={namespace}
          onDone={() => {
            setShowDialog(false)
            onRefresh()
          }}
          onCancel={() => setShowDialog(false)}
        />
      </Suspense>)}
    </>
  )
}
