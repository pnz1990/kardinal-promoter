// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// RejectedLiveBanner.tsx — a Rejected bundle whose change is live in an
// environment stays the current bundle there (lifecycle.RejectedLiveStep in
// the controller). Rejecting stops promotions; it does not revert what was
// merged, so the header says so and names the rollback to run.

import type { Bundle } from '../types'
import '../styles/RejectedLiveBanner.css'

export function RejectedLiveBanner({ bundle }: { bundle: Bundle }) {
  const envs = bundle.rejectedLiveEnvironments ?? []
  if (bundle.phase !== 'Rejected' || envs.length === 0) return null
  return (
    <div
      role="alert"
      data-testid="rejected-live-banner"
      className="rejected-live-banner"
    >
      Rejected change is live in {envs.join(', ')}; roll back:{' '}
      {envs.map((env, i) => (
        <span key={env}>
          {i > 0 && ', '}
          <code>kardinal rollback {bundle.pipeline} --env {env}</code>
        </span>
      ))}
    </div>
  )
}
