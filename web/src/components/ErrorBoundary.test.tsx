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

import React from 'react'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { ErrorBoundary } from './ErrorBoundary'

/** Component that throws when `shouldThrow` is true. */
function MaybeThrow({ shouldThrow }: { shouldThrow: boolean }): React.ReactElement | null {
  if (shouldThrow) throw new Error('Test render error')
  return <span>OK</span>
}

describe('ErrorBoundary', () => {
  beforeEach(() => {
    // Suppress console.error output from intentionally-thrown test errors.
    vi.spyOn(console, 'error').mockImplementation(() => {})
  })

  it('renders children normally when no error occurs', () => {
    render(
      <ErrorBoundary fallbackMessage="Something went wrong">
        <MaybeThrow shouldThrow={false} />
      </ErrorBoundary>
    )
    expect(screen.getByText('OK')).toBeInTheDocument()
    expect(screen.queryByText('Something went wrong')).not.toBeInTheDocument()
  })

  it('shows fallback message when child throws', () => {
    render(
      <ErrorBoundary fallbackMessage="Graph failed to load">
        <MaybeThrow shouldThrow={true} />
      </ErrorBoundary>
    )
    expect(screen.getByText('Graph failed to load')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Retry' })).toBeInTheDocument()
    // Raw error details must NOT be in the DOM.
    expect(screen.queryByText(/Test render error/)).not.toBeInTheDocument()
  })

  it('shows correct fallback message per boundary', () => {
    render(
      <ErrorBoundary fallbackMessage="Timeline unavailable">
        <MaybeThrow shouldThrow={true} />
      </ErrorBoundary>
    )
    expect(screen.getByText('Timeline unavailable')).toBeInTheDocument()
  })

  it('renders fallback in an alert role for accessibility', () => {
    render(
      <ErrorBoundary fallbackMessage="Details unavailable">
        <MaybeThrow shouldThrow={true} />
      </ErrorBoundary>
    )
    expect(screen.getByRole('alert')).toBeInTheDocument()
  })

  it('retry button resets the boundary and remounts the child', async () => {
    const user = userEvent.setup()

    const { rerender } = render(
      <ErrorBoundary fallbackMessage="Graph failed to load">
        <MaybeThrow shouldThrow={true} />
      </ErrorBoundary>
    )

    // Boundary has caught the error — fallback visible.
    expect(screen.getByText('Graph failed to load')).toBeInTheDocument()

    // Rerender with shouldThrow=false so that after retry the child succeeds.
    rerender(
      <ErrorBoundary fallbackMessage="Graph failed to load">
        <MaybeThrow shouldThrow={false} />
      </ErrorBoundary>
    )

    await user.click(screen.getByRole('button', { name: 'Retry' }))

    // After retry: child renders normally.
    expect(screen.getByText('OK')).toBeInTheDocument()
    expect(screen.queryByText('Graph failed to load')).not.toBeInTheDocument()
  })
})
