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

// components/EventsPanel.test.tsx — Unit tests for #527 K8s events stream.
import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'
import EventsPanel, { type StepEvent } from './EventsPanel'

const warningEvent: StepEvent = {
  type: 'Warning',
  reason: 'StepFailed',
  message: 'git push failed: 403 Forbidden',
  count: 3,
  firstTimestamp: new Date(Date.now() - 10 * 60 * 1000).toISOString(),
  lastTimestamp: new Date(Date.now() - 5 * 60 * 1000).toISOString(),
}

const normalEvent: StepEvent = {
  type: 'Normal',
  reason: 'StepProgressing',
  message: 'git-clone completed successfully',
  count: 1,
  firstTimestamp: new Date(Date.now() - 20 * 60 * 1000).toISOString(),
  lastTimestamp: new Date(Date.now() - 15 * 60 * 1000).toISOString(),
}

describe('EventsPanel', () => {
  it('renders empty state when no events', () => {
    render(<EventsPanel events={[]} stepName="my-step" namespace="default" />)
    expect(screen.getByTestId('events-panel-empty')).toBeInTheDocument()
    expect(screen.getByText(/No events recorded yet/)).toBeInTheDocument()
  })

  it('shows kubectl hint in empty state', () => {
    render(<EventsPanel events={[]} stepName="my-step" namespace="test-ns" />)
    const empty = screen.getByTestId('events-panel-empty')
    expect(empty.textContent).toContain('my-step')
    expect(empty.textContent).toContain('test-ns')
  })

  it('renders events when provided', () => {
    render(<EventsPanel events={[warningEvent, normalEvent]} />)
    const rows = screen.getAllByTestId('event-row')
    expect(rows).toHaveLength(2)
  })

  it('shows Warning type badge for warning events', () => {
    render(<EventsPanel events={[warningEvent]} />)
    const typeBadge = screen.getByTestId('event-type')
    expect(typeBadge).toHaveTextContent('Warning')
  })

  it('shows Normal type badge for normal events', () => {
    render(<EventsPanel events={[normalEvent]} />)
    const typeBadge = screen.getByTestId('event-type')
    expect(typeBadge).toHaveTextContent('Normal')
  })

  it('shows reason and message', () => {
    render(<EventsPanel events={[warningEvent]} />)
    expect(screen.getByText('StepFailed')).toBeInTheDocument()
    expect(screen.getByText(/git push failed/)).toBeInTheDocument()
  })

  it('shows count badge when count > 1', () => {
    render(<EventsPanel events={[warningEvent]} />)
    expect(screen.getByText('×3')).toBeInTheDocument()
  })

  it('does not show count badge when count is 1', () => {
    render(<EventsPanel events={[normalEvent]} />)
    expect(screen.queryByText('×1')).not.toBeInTheDocument()
  })

  it('shows events count in heading', () => {
    render(<EventsPanel events={[warningEvent, normalEvent]} />)
    expect(screen.getByText('(2)')).toBeInTheDocument()
  })

  it('renders empty state when events is null', () => {
    render(<EventsPanel events={null} />)
    expect(screen.getByTestId('events-panel-empty')).toBeInTheDocument()
  })
})
