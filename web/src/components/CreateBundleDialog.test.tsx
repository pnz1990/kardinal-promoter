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

// components/CreateBundleDialog.test.tsx — Tests for #917 Create Bundle dialog.
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { CreateBundleDialog, CreateBundleButton } from './CreateBundleDialog'
import * as apiModule from '../api/client'

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
    createBundle: vi.fn(),
    getStepEvents: vi.fn(),
  },
}))

const mockApi = apiModule.api as ReturnType<typeof vi.fn> & typeof apiModule.api

beforeEach(() => {
  vi.clearAllMocks()
})

// ─── CreateBundleDialog ───────────────────────────────────────────────────────

describe('CreateBundleDialog', () => {
  it('renders the dialog with required and optional fields', () => {
    render(
      <CreateBundleDialog
        pipelineName="nginx-demo"
        namespace="default"
        onDone={() => {}}
        onCancel={() => {}}
      />
    )
    expect(screen.getByRole('dialog')).toBeInTheDocument()
    expect(screen.getByLabelText(/container image/i)).toBeInTheDocument()
    expect(screen.getByLabelText(/commit sha/i)).toBeInTheDocument()
    expect(screen.getByLabelText(/author/i)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /create bundle/i })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /cancel bundle creation/i })).toBeInTheDocument()
  })

  it('shows validation error and does not call API when image is empty on submit', async () => {
    render(
      <CreateBundleDialog
        pipelineName="nginx-demo"
        namespace="default"
        onDone={() => {}}
        onCancel={() => {}}
      />
    )
    fireEvent.click(screen.getByRole('button', { name: /create bundle/i }))
    await waitFor(() => {
      expect(screen.getByRole('alert')).toBeInTheDocument()
    })
    expect(mockApi.createBundle).not.toHaveBeenCalled()
  })

  it('calls api.createBundle once with correct args on valid submit', async () => {
    const onDone = vi.fn()
    ;(mockApi.createBundle as ReturnType<typeof vi.fn>).mockResolvedValueOnce({ bundle: 'nginx-demo-abc', message: 'bundle created' })

    render(
      <CreateBundleDialog
        pipelineName="nginx-demo"
        namespace="default"
        onDone={onDone}
        onCancel={() => {}}
      />
    )

    fireEvent.change(screen.getByLabelText(/container image/i), {
      target: { value: 'ghcr.io/example/app:sha-abc1234' },
    })
    fireEvent.change(screen.getByLabelText(/commit sha/i), {
      target: { value: 'abc1234' },
    })
    fireEvent.change(screen.getByLabelText(/author/i), {
      target: { value: 'alice' },
    })
    fireEvent.click(screen.getByRole('button', { name: /create bundle/i }))

    await waitFor(() => {
      expect(mockApi.createBundle).toHaveBeenCalledOnce()
    })
    expect(mockApi.createBundle).toHaveBeenCalledWith(
      'nginx-demo',
      'ghcr.io/example/app:sha-abc1234',
      'abc1234',
      'alice',
      'default',
    )
    expect(onDone).toHaveBeenCalledOnce()
  })

  it('displays API error inline and stays open on failure', async () => {
    ;(mockApi.createBundle as ReturnType<typeof vi.fn>).mockRejectedValueOnce(new Error('API error 500: failed to create bundle'))

    const onDone = vi.fn()
    render(
      <CreateBundleDialog
        pipelineName="nginx-demo"
        namespace="default"
        onDone={onDone}
        onCancel={() => {}}
      />
    )

    fireEvent.change(screen.getByLabelText(/container image/i), {
      target: { value: 'ghcr.io/example/app:sha-abc1234' },
    })
    fireEvent.click(screen.getByRole('button', { name: /create bundle/i }))

    await waitFor(() => {
      expect(screen.getByRole('alert')).toBeInTheDocument()
    })
    expect(screen.getByRole('alert')).toHaveTextContent('API error 500')
    expect(onDone).not.toHaveBeenCalled()
    // Dialog remains open
    expect(screen.getByRole('dialog')).toBeInTheDocument()
  })

  it('disables buttons while loading', async () => {
    let resolveCreate!: (v: { bundle: string; message: string }) => void
    ;(mockApi.createBundle as ReturnType<typeof vi.fn>).mockImplementationOnce(
      () => new Promise<{ bundle: string; message: string }>(res => { resolveCreate = res })
    )

    render(
      <CreateBundleDialog
        pipelineName="nginx-demo"
        namespace="default"
        onDone={() => {}}
        onCancel={() => {}}
      />
    )
    fireEvent.change(screen.getByLabelText(/container image/i), {
      target: { value: 'ghcr.io/example/app:sha-abc1234' },
    })
    fireEvent.click(screen.getByRole('button', { name: /create bundle/i }))

    await waitFor(() => {
      expect(screen.getByRole('button', { name: /create bundle/i })).toBeDisabled()
      expect(screen.getByRole('button', { name: /cancel bundle creation/i })).toBeDisabled()
    })

    // Resolve to clean up
    resolveCreate({ bundle: 'b', message: 'ok' })
  })

  it('calls onCancel when Cancel is clicked', () => {
    const onCancel = vi.fn()
    render(
      <CreateBundleDialog
        pipelineName="nginx-demo"
        namespace="default"
        onDone={() => {}}
        onCancel={onCancel}
      />
    )
    fireEvent.click(screen.getByRole('button', { name: /cancel bundle creation/i }))
    expect(onCancel).toHaveBeenCalledOnce()
  })
})

// ─── CreateBundleButton ───────────────────────────────────────────────────────

describe('CreateBundleButton', () => {
  it('renders a button with aria-label for accessibility', () => {
    render(
      <CreateBundleButton
        pipelineName="nginx-demo"
        namespace="default"
        onRefresh={() => {}}
      />
    )
    expect(screen.getByRole('button', { name: /create bundle for nginx-demo/i })).toBeInTheDocument()
  })

  it('opens the CreateBundleDialog when clicked', () => {
    render(
      <CreateBundleButton
        pipelineName="nginx-demo"
        namespace="default"
        onRefresh={() => {}}
      />
    )
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: /create bundle for nginx-demo/i }))
    expect(screen.getByRole('dialog')).toBeInTheDocument()
  })

  it('calls onRefresh and closes dialog after successful creation', async () => {
    const onRefresh = vi.fn()
    ;(mockApi.createBundle as ReturnType<typeof vi.fn>).mockResolvedValueOnce({ bundle: 'nginx-demo-xyz', message: 'ok' })

    render(
      <CreateBundleButton
        pipelineName="nginx-demo"
        namespace="default"
        onRefresh={onRefresh}
      />
    )

    fireEvent.click(screen.getByRole('button', { name: /create bundle for nginx-demo/i }))
    fireEvent.change(screen.getByLabelText(/container image/i), {
      target: { value: 'ghcr.io/example/app:v1.0' },
    })
    // Click the dialog submit button (aria-label="Create bundle")
    fireEvent.click(screen.getByRole('button', { name: 'Create bundle' }))

    await waitFor(() => {
      expect(onRefresh).toHaveBeenCalledOnce()
    })
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })
})
