// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// stage/stageErrors.ts — what a failed drawer or inbox action says.
import { actionErrorMessage } from '../components/ConfirmDialog'

/**
 * What a failed action says: how to fix it. A 403 carries the controller's
 * text, which names the permission and the role that grants it; a 409 the
 * conflict as the server put it.
 */
export function stageErrorMessage(action: string, err: unknown): string {
  const text = err instanceof Error ? err.message : String(err)
  const m = /^API error (\d+): ([\s\S]*)$/.exec(text)
  if (!m) return actionErrorMessage(action, err)
  const [, code, body] = m
  const detail = body.trim()
  switch (code) {
    case '403':
      return /role/.test(detail)
        ? `You are not allowed to ${action}: ${detail}`
        : `You are not allowed to ${action}${detail ? ` (${detail})` : ''}. Ask an admin for the kardinal role that grants it (promoter, approver or admin).`
    case '409':
      return `Could not ${action}: ${detail}. Refresh to see the current state, then try again.`
    case '401':
      return `Could not ${action}: your session ended. Sign in again, then retry.`
    default:
      return `Could not ${action}: ${detail}`
  }
}
