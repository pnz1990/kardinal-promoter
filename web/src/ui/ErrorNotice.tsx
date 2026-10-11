// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// ErrorNotice.tsx — a failed read: what it means (errorMessage.ts), the client's message as
// quiet detail, and Retry when the caller can try again.
import { humanError } from '../errorMessage'
import { Button } from './Button'
import { cx } from './cx'

export interface ErrorNoticeProps {
  /** The client's message, e.g. "API error 503: Service Unavailable". */
  error: string
  /** Shows Retry, which calls this. */
  onRetry?: () => void
  className?: string
}

export function ErrorNotice({ error, onRetry, className }: ErrorNoticeProps) {
  const { message, detail } = humanError(error)
  return (
    <div role="alert" className={cx('k-error', className)}>
      <p className="k-error__message">{message}</p>
      <p className="k-error__detail">{detail}</p>
      {onRetry && <Button size="sm" variant="secondary" onClick={onRetry}>Retry</Button>}
    </div>
  )
}
