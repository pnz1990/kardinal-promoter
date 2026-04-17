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

// components/ErrorBoundary.tsx — React error boundary for async components (#747).
//
// Usage:
//   <ErrorBoundary fallbackMessage="Graph failed to load">
//     <DAGView ... />
//   </ErrorBoundary>
//
// Design:
// - Class component required for componentDidCatch (React lifecycle).
// - Retry increments a key on the child, forcing a remount.
// - Never exposes raw stack traces in the fallback UI.

import { Component, type ErrorInfo, type ReactNode } from 'react'

interface Props {
  /** Message shown in the error fallback card. */
  fallbackMessage: string
  /** Content to render when no error has occurred. */
  children: ReactNode
}

interface State {
  /** Whether a render error has been caught. */
  hasError: boolean
  /** Key incremented on retry to force child remount. */
  retryKey: number
}

/**
 * ErrorBoundary wraps a subtree and catches render-phase errors.
 * On error: shows a fallback card with the provided message and a Retry button.
 * Retry resets the error state and remounts the child via key increment.
 */
export class ErrorBoundary extends Component<Props, State> {
  constructor(props: Props) {
    super(props)
    this.state = { hasError: false, retryKey: 0 }
  }

  static getDerivedStateFromError(): Partial<State> {
    return { hasError: true }
  }

  componentDidCatch(error: Error, info: ErrorInfo): void {
    // Log to console — never expose raw stack traces in the UI.
    console.error('[ErrorBoundary] Caught render error:', error, info.componentStack)
  }

  private handleRetry = (): void => {
    this.setState(prev => ({ hasError: false, retryKey: prev.retryKey + 1 }))
  }

  render(): ReactNode {
    if (this.state.hasError) {
      return (
        <div
          className="error-boundary-fallback"
          role="alert"
          style={{
            display: 'flex',
            flexDirection: 'column',
            alignItems: 'center',
            justifyContent: 'center',
            padding: '2rem',
            gap: '1rem',
            color: 'var(--color-text-secondary, #888)',
            fontSize: '0.875rem',
          }}
        >
          <span>{this.props.fallbackMessage}</span>
          <button
            onClick={this.handleRetry}
            style={{
              padding: '0.375rem 0.75rem',
              borderRadius: '4px',
              border: '1px solid var(--color-border, #444)',
              background: 'transparent',
              color: 'var(--color-text-primary, inherit)',
              cursor: 'pointer',
              fontSize: '0.875rem',
            }}
          >
            Retry
          </button>
        </div>
      )
    }

    // Use retryKey to force child remount on retry.
    return (
      <ErrorBoundaryInner key={this.state.retryKey}>
        {this.props.children}
      </ErrorBoundaryInner>
    )
  }
}

/** Inner wrapper to carry the retryKey — allows child remount on key change. */
function ErrorBoundaryInner({ children }: { children: ReactNode }): ReactNode {
  return <>{children}</>
}
