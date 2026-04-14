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

// components/ActionBar.test.tsx — Tests for #506 in-UI actions.
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { ActionBar, GateActionButton } from './ActionBar'
import * as apiModule from '../api/client'

// Mock the api module
vi.mock('../api/client', () => ({
  api: {
    pause: vi.fn(),
    resume: vi.fn(),
    approveGate: vi.fn(),
    listPipelines: vi.fn(),
    listBundles: vi.fn(),
    getGraph: vi.fn(),
    getSteps: vi.fn(),
    listGates: vi.fn(),
    promote: vi.fn(),
    rollback: vi.fn(),
    validateCEL: vi.fn(),
  },
}))

const mockApi = apiModule.api as ReturnType<typeof vi.fn> & typeof apiModule.api

beforeEach(() => {
  vi.clearAllMocks()
})

// ─── ActionBar ────────────────────────────────────────────────────────────────

describe('ActionBar', () => {
  it('renders Pause button when pipeline is not paused', () => {
    render(
      <ActionBar
        pipelineName="my-app"
        namespace="default"
        paused={false}
        onRefresh={() => {}}
      />
    )
    expect(screen.getByRole('button', { name: /pause pipeline/i })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /resume/i })).not.toBeInTheDocument()
  })

  it('renders Resume button when pipeline is paused', () => {
    render(
      <ActionBar
        pipelineName="my-app"
        namespace="default"
        paused={true}
        onRefresh={() => {}}
      />
    )
    expect(screen.getByRole('button', { name: /resume pipeline/i })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /pause pipeline/i })).not.toBeInTheDocument()
  })

  it('shows confirmation dialog when Pause is clicked', () => {
    render(
      <ActionBar pipelineName="my-app" namespace="default" paused={false} onRefresh={() => {}} />
    )
    fireEvent.click(screen.getByRole('button', { name: /pause pipeline/i }))
    expect(screen.getByRole('dialog')).toBeInTheDocument()
    expect(screen.getByText(/pause pipeline\?/i)).toBeInTheDocument()
  })

  it('shows confirmation dialog when Resume is clicked', () => {
    render(
      <ActionBar pipelineName="my-app" namespace="default" paused={true} onRefresh={() => {}} />
    )
    fireEvent.click(screen.getByRole('button', { name: /resume pipeline/i }))
    expect(screen.getByRole('dialog')).toBeInTheDocument()
    expect(screen.getByText(/resume pipeline\?/i)).toBeInTheDocument()
  })

  it('cancels and closes dialog without calling API', () => {
    render(
      <ActionBar pipelineName="my-app" namespace="default" paused={false} onRefresh={() => {}} />
    )
    fireEvent.click(screen.getByRole('button', { name: /pause pipeline/i }))
    fireEvent.click(screen.getByRole('button', { name: /cancel/i }))
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(mockApi.pause).not.toHaveBeenCalled()
  })

  it('calls api.pause and onRefresh on confirm', async () => {
    const onRefresh = vi.fn()
    ;(mockApi.pause as ReturnType<typeof vi.fn>).mockResolvedValue({ message: 'paused' })
    render(
      <ActionBar pipelineName="my-app" namespace="default" paused={false} onRefresh={onRefresh} />
    )
    // Open confirm dialog
    fireEvent.click(screen.getByRole('button', { name: /pause pipeline/i }))
    // The dialog renders — click the "Pause pipeline" button inside the dialog
    const dialog = screen.getByRole('dialog')
    const confirmBtn = dialog.querySelector('button[aria-label="Pause pipeline"]')!
    fireEvent.click(confirmBtn)
    await waitFor(() => {
      expect(mockApi.pause).toHaveBeenCalledWith('my-app', 'default')
      expect(onRefresh).toHaveBeenCalled()
    })
  })

  it('calls api.resume and onRefresh on confirm', async () => {
    const onRefresh = vi.fn()
    ;(mockApi.resume as ReturnType<typeof vi.fn>).mockResolvedValue({ message: 'resumed' })
    render(
      <ActionBar pipelineName="my-app" namespace="default" paused={true} onRefresh={onRefresh} />
    )
    // Open confirm dialog
    fireEvent.click(screen.getByRole('button', { name: /resume pipeline/i }))
    const dialog = screen.getByRole('dialog')
    const confirmBtn = dialog.querySelector('button[aria-label="Resume pipeline"]')!
    fireEvent.click(confirmBtn)
    await waitFor(() => {
      expect(mockApi.resume).toHaveBeenCalledWith('my-app', 'default')
      expect(onRefresh).toHaveBeenCalled()
    })
  })

  it('shows inline error when API call fails', async () => {
    ;(mockApi.pause as ReturnType<typeof vi.fn>).mockRejectedValue(new Error('network error'))
    render(
      <ActionBar pipelineName="my-app" namespace="default" paused={false} onRefresh={() => {}} />
    )
    // Open confirm dialog
    fireEvent.click(screen.getByRole('button', { name: /pause pipeline/i }))
    const dialog = screen.getByRole('dialog')
    const confirmBtn = dialog.querySelector('button[aria-label="Pause pipeline"]')!
    fireEvent.click(confirmBtn)
    await waitFor(() => {
      // Error appears in toolbar and/or dialog — at least one alert is present
      expect(screen.getAllByRole('alert').length).toBeGreaterThan(0)
    })
  })
})

// ─── GateActionButton ─────────────────────────────────────────────────────────

describe('GateActionButton', () => {
  it('renders Override button', () => {
    render(
      <GateActionButton gateName="no-weekend" gateNamespace="default" onRefresh={() => {}} />
    )
    expect(screen.getByRole('button', { name: /override gate/i })).toBeInTheDocument()
  })

  it('opens override dialog when clicked', () => {
    render(
      <GateActionButton gateName="no-weekend" gateNamespace="default" onRefresh={() => {}} />
    )
    fireEvent.click(screen.getByRole('button', { name: /override gate/i }))
    expect(screen.getByRole('dialog')).toBeInTheDocument()
    expect(screen.getByText(/override gate: no-weekend/i)).toBeInTheDocument()
  })

  it('requires reason text before submit is enabled', () => {
    render(
      <GateActionButton gateName="no-weekend" gateNamespace="default" onRefresh={() => {}} />
    )
    fireEvent.click(screen.getByRole('button', { name: /override gate/i }))
    const submitBtn = screen.getByRole('button', { name: /confirm gate override/i })
    // Reason is empty — button should be disabled
    expect(submitBtn).toBeDisabled()
  })

  it('calls api.approveGate with reason and onRefresh', async () => {
    const onRefresh = vi.fn()
    ;(mockApi.approveGate as ReturnType<typeof vi.fn>).mockResolvedValue({ message: 'overridden' })
    render(
      <GateActionButton gateName="no-weekend" gateNamespace="platform-policies" onRefresh={onRefresh} />
    )
    fireEvent.click(screen.getByRole('button', { name: /override gate/i }))
    // Fill in reason
    const textarea = screen.getByRole('textbox')
    fireEvent.change(textarea, { target: { value: 'Emergency hotfix' } })
    fireEvent.click(screen.getByRole('button', { name: /confirm gate override/i }))
    await waitFor(() => {
      expect(mockApi.approveGate).toHaveBeenCalledWith(
        'no-weekend', 'platform-policies', 'Emergency hotfix', 60
      )
      expect(onRefresh).toHaveBeenCalled()
    })
  })

  it('closes dialog on cancel without API call', () => {
    render(
      <GateActionButton gateName="no-weekend" gateNamespace="default" onRefresh={() => {}} />
    )
    fireEvent.click(screen.getByRole('button', { name: /override gate/i }))
    fireEvent.click(screen.getByRole('button', { name: /cancel gate override/i }))
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(mockApi.approveGate).not.toHaveBeenCalled()
  })
})
