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

// components/EmptyState.test.tsx — Unit tests for #530 improved empty state.
import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'
import EmptyState from './EmptyState'

describe('EmptyState', () => {
  it('renders the empty state panel', () => {
    render(<EmptyState />)
    expect(screen.getByTestId('empty-state')).toBeInTheDocument()
  })

  it('shows No pipelines found heading', () => {
    render(<EmptyState />)
    expect(screen.getByText('No pipelines found')).toBeInTheDocument()
  })

  it('shows Pipeline explanation text', () => {
    render(<EmptyState />)
    expect(screen.getByText(/Pipeline/)).toBeInTheDocument()
    expect(screen.getByText(/promotion environments/)).toBeInTheDocument()
  })

  it('shows a docs link', () => {
    render(<EmptyState />)
    const link = screen.getByTestId('docs-link')
    expect(link).toHaveAttribute('href', 'https://pnz1990.github.io/kardinal-promoter/')
    expect(link).toHaveAttribute('target', '_blank')
  })

  it('shows the quickstart kubectl command', () => {
    render(<EmptyState />)
    const cmd = screen.getByTestId('quickstart-command')
    expect(cmd.textContent).toContain('kubectl apply')
    expect(cmd.textContent).toContain('pipeline.yaml')
  })

  it('includes a copy button for the command', () => {
    render(<EmptyState />)
    expect(screen.getByTestId('copy-button')).toBeInTheDocument()
  })

  it('shows watching indicator', () => {
    render(<EmptyState />)
    expect(screen.getByTestId('watching-indicator')).toBeInTheDocument()
    expect(screen.getByTestId('watching-indicator').textContent).toContain('Watching for new pipelines')
  })
})
