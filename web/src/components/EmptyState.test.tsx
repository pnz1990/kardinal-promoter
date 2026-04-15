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

// EmptyState.test.tsx — Tests for the onboarding empty state (#530).
import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'
import { EmptyState } from './EmptyState'

describe('EmptyState — onboarding card (#530)', () => {
  it('renders with data-testid="empty-state"', () => {
    render(<EmptyState />)
    expect(screen.getByTestId('empty-state')).toBeInTheDocument()
  })

  it('shows one-sentence explanation', () => {
    render(<EmptyState />)
    expect(screen.getByTestId('empty-state-description')).toBeInTheDocument()
    expect(screen.getByText(/A Pipeline defines/i)).toBeInTheDocument()
  })

  it('shows the promotion environments description', () => {
    render(<EmptyState />)
    expect(screen.getByText(/test → uat → prod/i)).toBeInTheDocument()
  })

  it('shows the kubectl apply quickstart command', () => {
    render(<EmptyState />)
    expect(screen.getByTestId('quickstart-command')).toBeInTheDocument()
    expect(screen.getByText(/kubectl apply/i)).toBeInTheDocument()
  })

  it('shows a CopyButton for the quickstart command', () => {
    render(<EmptyState />)
    expect(screen.getByTestId('copy-button')).toBeInTheDocument()
  })

  it('shows a link to the docs', () => {
    render(<EmptyState />)
    const link = screen.getByTestId('docs-link')
    expect(link).toBeInTheDocument()
    expect(link).toHaveAttribute('href', 'https://pnz1990.github.io/kardinal-promoter/')
    expect(link).toHaveAttribute('target', '_blank')
  })

  it('shows "watching for new pipelines" indicator', () => {
    render(<EmptyState />)
    expect(screen.getByTestId('watching-indicator')).toBeInTheDocument()
    expect(screen.getByText(/Watching for new pipelines/i)).toBeInTheDocument()
  })

  it('has "No pipelines yet" heading', () => {
    render(<EmptyState />)
    expect(screen.getByText(/No pipelines yet/i)).toBeInTheDocument()
  })
})
