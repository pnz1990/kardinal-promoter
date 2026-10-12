// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// stage/signIn.tsx — an approval from the UI names who decided, so without
// sign-in (no TokenReview: POST /access names no user) Approve, Decline and
// Revoke mine are inert everywhere, and each surface says why once, with the
// command that works without it (the same words as the buttons' tooltips,
// actionAccess.signInReason).
import { useAccess } from '../access/access'
import { approveCommand, signInLead, type Decision } from '../access/actionAccess'
import CopyButton from '../components/CopyButton'
import type { AccessCheck } from '../types'
import '../styles/SignInNote.css'

export { approveCommand }

const APPROVE: AccessCheck = { verb: 'create', resource: 'approvals' }

/** Whether approving from the UI needs sign-in: the controller answered and named no user. */
export function useSignInNeeded(namespace: string | undefined): boolean {
  const { identity } = useAccess(namespace, namespace ? [APPROVE] : [])
  return identity === null
}

/** The one line under an inert Approve: why, and the command to run instead. */
export function SignInNote({ command, decision = 'approve' }: { command: string; decision?: Decision }) {
  return (
    <p className="sign-in-note">
      <span>{signInLead(decision)} </span>
      <code className="sign-in-note__cli">{command}</code>
      <CopyButton text={command} title={`Copy ${command}`} />
    </p>
  )
}
