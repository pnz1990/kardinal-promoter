// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

import { useState } from 'react'
import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import axe from 'axe-core'
import { CommandPalette, filterCommands, matchScore, type PaletteCommand } from './CommandPalette'

const cmd = (id: string, label: string, group: PaletteCommand['group'] = 'Go to', extra: Partial<PaletteCommand> = {}): PaletteCommand =>
  ({ id, label, group, run: vi.fn(), ...extra })

describe('matching', () => {
  it('ranks a word start before a substring before a subsequence', () => {
    expect(matchScore('checkout › prod', 'prod')).toBeLessThan(matchScore('checkoutproduce', 'prod'))
    expect(matchScore('checkoutproduce', 'prod')).toBeLessThan(matchScore('pr-odd', 'prod'))
    expect(matchScore('payments', 'zzz')).toBe(-1)
    expect(matchScore('anything', '  ')).toBe(0)
  })

  it('keeps the given order for ties and searches keywords and hints', () => {
    const cs = [cmd('a', 'checkout'), cmd('b', 'checkout › prod', 'Go to', { hint: 'team-a' }), cmd('c', 'Version v1', 'Go to', { keywords: 'abc1234' })]
    expect(filterCommands(cs, 'check').map((c) => c.id)).toEqual(['a', 'b'])
    expect(filterCommands(cs, 'team-a').map((c) => c.id)).toEqual(['b'])
    expect(filterCommands(cs, 'abc12').map((c) => c.id)).toEqual(['c'])
  })
})

describe('CommandPalette', () => {
  function Harness({ commands }: { commands: PaletteCommand[] }) {
    const [open, setOpen] = useState(false)
    return (
      <>
        <button onClick={() => setOpen(true)}>Open palette</button>
        <CommandPalette open={open} onClose={() => setOpen(false)} commands={commands} />
      </>
    )
  }

  it('filters, moves with the arrows, runs with Enter, closes and gives focus back', async () => {
    const user = userEvent.setup()
    const prod = cmd('p', 'checkout › prod')
    const theme = cmd('t', 'Switch the theme to Light', 'Do')
    render(<Harness commands={[cmd('f', 'Pipelines'), cmd('c', 'checkout'), prod, theme]} />)
    const trigger = screen.getByRole('button', { name: 'Open palette' })
    await user.click(trigger)
    const input = screen.getByRole('combobox', { name: /Search pipelines/ })
    expect(input).toHaveFocus()
    expect(screen.getByRole('status')).toHaveTextContent('4 matches')
    expect(screen.getByRole('group', { name: 'Go to' })).toBeInTheDocument()
    expect(screen.getByRole('group', { name: 'Do' })).toBeInTheDocument()

    await user.type(input, 'check')
    expect(screen.getByRole('status')).toHaveTextContent('2 matches')
    expect(screen.getAllByRole('option').map((o) => o.textContent)).toEqual(['checkout', 'checkout › prod'])
    expect(input).toHaveAttribute('aria-activedescendant', screen.getAllByRole('option')[0].id)
    await user.keyboard('{ArrowDown}')
    expect(screen.getAllByRole('option')[1]).toHaveAttribute('aria-selected', 'true')
    await user.keyboard('{Enter}')
    expect(prod.run).toHaveBeenCalledOnce()
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(trigger).toHaveFocus()
  })

  it('says when nothing matches, and Esc closes it without running anything', async () => {
    const user = userEvent.setup()
    const c = cmd('c', 'checkout')
    render(<Harness commands={[c]} />)
    await user.click(screen.getByRole('button', { name: 'Open palette' }))
    await user.type(screen.getByRole('combobox'), 'zzz')
    expect(screen.getByRole('status')).toHaveTextContent('No matches')
    await user.keyboard('{Enter}{Escape}')
    expect(c.run).not.toHaveBeenCalled()
    expect(screen.queryByRole('dialog')).toBeNull()
  })

  it('shows at most 50 and says how many matched', async () => {
    const user = userEvent.setup()
    render(<Harness commands={Array.from({ length: 80 }, (_, i) => cmd(`e${i}`, `env-${i}`))} />)
    await user.click(screen.getByRole('button', { name: 'Open palette' }))
    expect(screen.getAllByRole('option')).toHaveLength(50)
    expect(screen.getByRole('status')).toHaveTextContent('80 matches, the first 50 shown')
  })

  it('has no axe violations', async () => {
    const user = userEvent.setup()
    render(<Harness commands={[cmd('c', 'checkout'), cmd('t', 'Refresh now', 'Do')]} />)
    await user.click(screen.getByRole('button', { name: 'Open palette' }))
    const result = await axe.run(document.body, { runOnly: ['wcag2a', 'wcag2aa'], rules: { 'color-contrast': { enabled: false } } })
    expect(result.violations.map((v) => v.id)).toEqual([])
  })
})
