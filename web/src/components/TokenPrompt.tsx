// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// components/TokenPrompt.tsx — bearer token sign-in for UI auth.
// Registers itself with the API client and opens when a UI API request gets
// 401 (--ui-auth-token or --ui-tokenreview-auth). The entered token is kept
// in sessionStorage by the client and the failed requests are retried.
import { lazy, Suspense, useEffect, useState } from 'react'
// The form's styles stay in the entry stylesheet: one stylesheet compresses better.
import '../styles/TokenPrompt.css'
import { setTokenPrompt, type TokenPromptReason } from '../api/client'

interface PendingPrompt {
  id: number
  reason: TokenPromptReason
  resolve: (token: string) => void
}

const TokenForm = lazy(() => import('./TokenForm'))

/** TokenPrompt renders nothing until the controller asks for a token. */
export function TokenPrompt() {
  const [pending, setPending] = useState<PendingPrompt | null>(null)

  useEffect(() => {
    let seq = 0
    let rejectCurrent: ((err: Error) => void) | null = null
    const unregister = setTokenPrompt(reason => new Promise<string>((resolve, reject) => {
      rejectCurrent = reject
      seq += 1
      setPending({ id: seq, reason, resolve })
    }))
    return () => {
      unregister()
      rejectCurrent?.(new Error('token prompt closed'))
    }
  }, [])

  if (!pending) return null
  return (
    <Suspense fallback={null}>
    <TokenForm
      key={pending.id}
      reason={pending.reason}
      onSubmit={token => {
        setPending(null)
        pending.resolve(token)
      }}
    />
    </Suspense>
  )
}

