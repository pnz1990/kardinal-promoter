// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// FreezeBanner.test.tsx — the freeze banner, Unfreeze, and the Freeze menu (#1843).

import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { Freeze } from '../types'

const api = vi.hoisted(() => ({ freeze: vi.fn(), unfreeze: vi.fn(), listFreezes: vi.fn() }))
vi.mock('../api/client', () => ({ api }))
const perm = vi.hoisted(() => ({ allowed: true, known: true }))
vi.mock('../access/access', () => ({ useAccessIn: () => ({ can: () => perm, identity: undefined }) }))

import { FreezeBanner, FreezeMenu, freezeSentence } from './FreezeBanner'

const agents: Freeze = {
  name: 'kardinal-freeze-agents', actors: 'agents', namespaces: [], scope: 'agents, every namespace', active: true,
  since: '2026-10-14T09:00:00Z', createdBy: 'alice', reason: 'INC-4211', exemptions: 1,
}

describe('FreezeBanner', () => {
  beforeEach(() => { api.unfreeze.mockReset(); api.freeze.mockReset(); perm.allowed = true; perm.known = true })

  it('says who froze whom, where, since when and why', () => {
    expect(freezeSentence(agents)).toBe(
      'Agents are frozen in every namespace by alice since 2026-10-14T09:00:00Z: INC-4211 (1 exemption in effect)')
    expect(freezeSentence({ ...agents, actors: 'everyone', namespaces: ['payments'], until: '2026-10-14T11:00:00Z', exemptions: 0 }))
      .toBe('The line is stopped for everyone in payments by alice since 2026-10-14T09:00:00Z until 2026-10-14T11:00:00Z: INC-4211')
  })

  it('shows only active freezes, and nothing without one', () => {
    const { container, rerender } = render(<FreezeBanner freezes={[{ ...agents, active: false }]} onChange={() => {}} />)
    expect(container.textContent).toBe('')
    rerender(<FreezeBanner freezes={[agents]} onChange={() => {}} />)
    expect(screen.getByRole('alert')).toHaveTextContent('Agents are frozen')
  })

  it('unfreezes after asking', async () => {
    const user = userEvent.setup()
    const onChange = vi.fn()
    api.unfreeze.mockResolvedValue({ message: 'lifted freeze kardinal-freeze-agents' })
    render(<FreezeBanner freezes={[agents]} onChange={onChange} />)
    await user.click(screen.getByRole('button', { name: 'Unfreeze' }))
    const dialog = screen.getByRole('dialog', { name: 'Lift kardinal-freeze-agents?' })
    await user.click(within(dialog).getByRole('button', { name: 'Unfreeze' }))
    expect(api.unfreeze).toHaveBeenCalledWith('kardinal-freeze-agents')
    expect(onChange).toHaveBeenCalledOnce()
  })

  it('names the command for those who may not unfreeze', () => {
    perm.allowed = false
    render(<FreezeBanner freezes={[agents]} onChange={() => {}} />)
    expect(screen.queryByRole('button', { name: 'Unfreeze' })).not.toBeInTheDocument()
    expect(screen.getByRole('alert')).toHaveTextContent('kardinal unfreeze kardinal-freeze-agents')
  })
})

describe('FreezeMenu', () => {
  beforeEach(() => { api.freeze.mockReset(); perm.allowed = true; perm.known = true })

  it('freezes agents with a reason and a duration', async () => {
    const user = userEvent.setup()
    const onChange = vi.fn()
    api.freeze.mockResolvedValue({ message: 'frozen' })
    render(<FreezeMenu onChange={onChange} />)
    await user.click(screen.getByRole('button', { name: 'Freeze' }))
    await user.click(screen.getByRole('button', { name: 'Freeze agents…' }))
    const dialog = screen.getByRole('dialog', { name: 'Freeze agents?' })
    await user.click(within(dialog).getByRole('button', { name: 'Freeze agents' }))
    expect(api.freeze).not.toHaveBeenCalled()
    expect(within(dialog).getByRole('alert')).toHaveTextContent('A reason is required')
    await user.type(within(dialog).getByLabelText('Reason (required)'), 'INC-4211')
    await user.selectOptions(within(dialog).getByLabelText('Ends'), '240')
    await user.click(within(dialog).getByRole('button', { name: 'Freeze agents' }))
    expect(api.freeze).toHaveBeenCalledWith({ actors: 'agents', reason: 'INC-4211', minutes: 240 })
    expect(onChange).toHaveBeenCalledOnce()
  })

  it('stops the line only after typing stop', async () => {
    const user = userEvent.setup()
    api.freeze.mockResolvedValue({ message: 'frozen' })
    render(<FreezeMenu onChange={() => {}} />)
    await user.click(screen.getByRole('button', { name: 'Freeze' }))
    await user.click(screen.getByRole('button', { name: 'Stop the line…' }))
    const dialog = screen.getByRole('dialog', { name: 'Stop the line for everyone?' })
    await user.type(within(dialog).getByLabelText('Reason (required)'), 'outage')
    await user.click(within(dialog).getByRole('button', { name: 'Stop the line' }))
    expect(api.freeze).not.toHaveBeenCalled()
    await user.type(within(dialog).getByLabelText('Type stop to confirm'), 'stop')
    await user.click(within(dialog).getByRole('button', { name: 'Stop the line' }))
    expect(api.freeze).toHaveBeenCalledWith({ actors: 'everyone', reason: 'outage', minutes: undefined })
  })

  it('is not offered to those who may not freeze', () => {
    perm.allowed = false
    const { container } = render(<FreezeMenu onChange={() => {}} />)
    expect(container.textContent).toBe('')
  })
})
