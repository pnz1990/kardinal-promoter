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
import { useEffect, useState, type FormEvent } from 'react'
import { setTokenPrompt, type TokenPromptReason } from '../api/client'
import CopyButton from './CopyButton'
import '../ui'
import '../styles/TokenPrompt.css'
import { isInsecureNonLocalConnection, PORT_FORWARD_COMMAND } from './InsecureConnectionBanner'

interface PendingPrompt {
  id: number
  reason: TokenPromptReason
  resolve: (token: string) => void
}

const TOKEN_COMMAND = 'kubectl create token <service-account> -n <namespace> --audience kardinal-promoter'

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
    <TokenForm
      key={pending.id}
      reason={pending.reason}
      onSubmit={token => {
        setPending(null)
        pending.resolve(token)
      }}
    />
  )
}

interface TokenFormProps {
  reason: TokenPromptReason
  onSubmit: (token: string) => void
}

function TokenForm({ reason, onSubmit }: TokenFormProps) {
  const [token, setToken] = useState('')
  const [emptyError, setEmptyError] = useState(false)
  const rejected = reason === 'rejected'
  const errorId = rejected || emptyError ? 'ui-token-error' : undefined
  // The page's insecure-connection banner is behind this dialog's backdrop,
  // and the token is typed here: warn in the dialog itself.
  const insecure = isInsecureNonLocalConnection()

  const handleSubmit = (e: FormEvent) => {
    e.preventDefault()
    const value = token.trim()
    if (!value) {
      setEmptyError(true)
      return
    }
    onSubmit(value)
  }

  return (
    <div
      role="dialog"
      aria-modal="true"
      aria-labelledby="ui-token-title"
      aria-describedby={insecure ? 'ui-token-desc ui-token-insecure' : 'ui-token-desc'}
      className="k-scrim token-prompt"
    >
      <div className="k-dialog token-prompt__panel">
        <h2 id="ui-token-title" className="k-dialog__title">
          Sign in to kardinal
        </h2>
        <p id="ui-token-desc" className="k-dialog__description">
          This controller requires a bearer token. Use the UI token from your administrator
          or a Kubernetes token for an account with access to kardinal resources.
        </p>

        {insecure && (
          <div id="ui-token-insecure" className="k-alert k-alert--blocked token-prompt__insecure">
            <div className="token-prompt__insecure-title">
              <span aria-hidden="true">⚠ </span>
              This page is on plain HTTP, so the token is sent unencrypted.
            </div>
            <div>
              Sign in over HTTPS, or run this and open{' '}
              <a href="http://localhost:8082/ui/" className="token-prompt__link">http://localhost:8082/ui/</a>:
            </div>
            <div className="token-prompt__command token-prompt__command--inline">
              <code className="token-prompt__code">{PORT_FORWARD_COMMAND}</code>
              <CopyButton text={PORT_FORWARD_COMMAND} title="Copy port-forward command" />
            </div>
          </div>
        )}

        <div className="token-prompt__hint">
          Create a token for a ServiceAccount:
        </div>
        <div className="token-prompt__command">
          <code className="token-prompt__code token-prompt__code--command">{TOKEN_COMMAND}</code>
          <CopyButton text={TOKEN_COMMAND} title="Copy command" />
        </div>

        <form onSubmit={handleSubmit}>
          <label htmlFor="ui-token" className="token-prompt__label">
            Token
          </label>
          <input
            id="ui-token"
            type="password"
            value={token}
            onChange={e => { setToken(e.target.value); setEmptyError(false) }}
            autoFocus
            autoComplete="off"
            spellCheck={false}
            aria-invalid={errorId ? true : undefined}
            aria-describedby={errorId}
            className="token-prompt__input"
          />
          {errorId && (
            <div id={errorId} role="alert" className="token-prompt__error">
              {emptyError
                ? 'Enter a token.'
                : 'The controller rejected this token. Check that it is complete and has not expired.'}
            </div>
          )}

          <div className="token-prompt__footer">
            <span className="token-prompt__note">
              Kept in this browser tab only. Closing the tab clears it.
            </span>
            <button type="submit" className="k-button k-button--primary k-button--md">
              Sign in
            </button>
          </div>
        </form>
      </div>
    </div>
  )
}
