// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// stage/signIn.tsx — an approval from the UI names who decided, so without
// sign-in (no TokenReview: POST /access names no user) Approve and Decline
// are inert everywhere, and each surface says why once, with the command
// that works without it.
import { useAccess } from '../access/access'
import CopyButton from '../components/CopyButton'
import type { AccessCheck } from '../types'
import '../styles/SignInNote.css'

const APPROVE: AccessCheck = { verb: 'create', resource: 'approvals' }

/** kardinal approve for an environment's incoming version (-n outside default). */
export function approveCommand(pipeline: string, env: string, namespace: string): string {
  const ns = namespace && namespace !== 'default' ? ` -n ${namespace}` : ''
  return `kardinal approve ${pipeline} ${env}${ns}`
}

/** Whether approving from the UI needs sign-in: the controller answered and named no user. */
export function useSignInNeeded(namespace: string | undefined): boolean {
  const { identity } = useAccess(namespace, namespace ? [APPROVE] : [])
  return identity === null
}

/** The one line under an inert Approve: why, and the command to run instead. */
export function SignInNote({ command }: { command: string }) {
  return (
    <p className="sign-in-note">
      <span>Approving needs sign-in with your Kubernetes identity — run </span>
      <code className="sign-in-note__cli">{command}</code>
      <CopyButton text={command} title={`Copy ${command}`} />
    </p>
  )
}
