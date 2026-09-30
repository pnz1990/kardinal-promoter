// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// components/ActionBar.pause.test.tsx — C10a-web-01: the pause and resume
// dialogs describe what pause does (hold at safe points), not a stop of every
// in-flight promotion.
import { describe, it, expect, vi } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import { ActionBar } from './ActionBar'

vi.mock('../api/client', () => ({
  api: { pause: vi.fn(), resume: vi.fn() },
}))

describe('ActionBar pause dialogs', () => {
  it('pause says which steps hold and which continue', () => {
    render(<ActionBar pipelineName="my-app" namespace="default" paused={false} onRefresh={() => {}} />)
    fireEvent.click(screen.getByRole('button', { name: /pause pipeline/i }))
    const dialog = screen.getByRole('dialog')
    expect(dialog.textContent).not.toMatch(/stop all in-flight/i)
    expect(dialog.textContent).toMatch(/no new promotion step starts/i)
    expect(dialog.textContent).toMatch(/waiting for a PR merge or running health checks continue/i)
  })

  it('resume says held steps continue', () => {
    render(<ActionBar pipelineName="my-app" namespace="default" paused={true} onRefresh={() => {}} />)
    fireEvent.click(screen.getByRole('button', { name: /resume pipeline/i }))
    const dialog = screen.getByRole('dialog')
    expect(dialog.textContent).not.toMatch(/restart/i)
    expect(dialog.textContent).toMatch(/continue where they stopped/i)
  })
})
