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

interface PendingPrompt {
  id: number
  reason: TokenPromptReason
  resolve: (token: string) => void
}

const TOKEN_COMMAND = 'kubectl create token <service-account> -n <namespace>'

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
      aria-describedby="ui-token-desc"
      style={{
        position: 'fixed', inset: 0,
        background: 'rgba(0,0,0,0.7)',
        display: 'flex', alignItems: 'center', justifyContent: 'center',
        zIndex: 2000,
      }}
    >
      <div
        style={{
          background: 'var(--color-bg)',
          border: '1px solid var(--color-border)',
          borderRadius: '8px',
          padding: '1.5rem',
          maxWidth: '460px',
          width: '90%',
        }}
      >
        <h2
          id="ui-token-title"
          style={{ margin: '0 0 0.25rem', fontSize: '1rem', fontWeight: 700, color: 'var(--color-text)' }}
        >
          Sign in to kardinal
        </h2>
        <p id="ui-token-desc" style={{ color: 'var(--color-text-muted)', fontSize: '0.8rem', margin: '0 0 1rem', lineHeight: 1.5 }}>
          This controller requires a bearer token. Use the UI token from your administrator
          or a Kubernetes token for an account with access to kardinal resources.
        </p>

        <div style={{ fontSize: '0.75rem', color: 'var(--color-text-muted)', marginBottom: '0.25rem' }}>
          Create a token for a ServiceAccount:
        </div>
        <div
          style={{
            display: 'flex', alignItems: 'center', gap: '0.5rem',
            background: 'var(--color-surface)', border: '1px solid var(--color-border-muted)',
            borderRadius: '6px', padding: '0.4rem 0.6rem', marginBottom: '1rem',
          }}
        >
          <code style={{ flex: 1, fontFamily: 'monospace', fontSize: '0.75rem', color: 'var(--color-code)', overflowWrap: 'anywhere' }}>
            {TOKEN_COMMAND}
          </code>
          <CopyButton text={TOKEN_COMMAND} title="Copy command" />
        </div>

        <form onSubmit={handleSubmit}>
          <label
            htmlFor="ui-token"
            style={{ display: 'block', fontSize: '0.75rem', color: 'var(--color-text-muted)', marginBottom: '0.25rem' }}
          >
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
            style={{
              width: '100%', boxSizing: 'border-box',
              background: 'var(--color-surface)',
              border: `1px solid ${errorId ? 'var(--color-error)' : 'var(--color-border)'}`,
              borderRadius: '6px', padding: '0.5rem 0.75rem',
              color: 'var(--color-text)', fontSize: '0.875rem',
              fontFamily: 'monospace',
              marginBottom: errorId ? '0.25rem' : '0.75rem',
            }}
          />
          {errorId && (
            <div
              id={errorId}
              role="alert"
              style={{ color: 'var(--color-error)', fontSize: '0.75rem', marginBottom: '0.75rem' }}
            >
              {emptyError
                ? 'Enter a token.'
                : 'The controller rejected this token. Check that it is complete and has not expired.'}
            </div>
          )}

          <div style={{ display: 'flex', alignItems: 'center', gap: '0.75rem', justifyContent: 'space-between', flexWrap: 'wrap' }}>
            <span style={{ fontSize: '0.7rem', color: 'var(--color-text-faint)' }}>
              Kept in this browser tab only. Closing the tab clears it.
            </span>
            <button
              type="submit"
              style={{
                padding: '0.45rem 1rem',
                background: 'var(--color-accent-bg)',
                border: '1px solid var(--color-accent)',
                borderRadius: '6px',
                color: 'var(--color-text)',
                cursor: 'pointer',
                fontSize: '0.875rem',
                fontWeight: 600,
              }}
            >
              Sign in
            </button>
          </div>
        </form>
      </div>
    </div>
  )
}
