// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// ui.test.tsx — the primitives: their roles, names and keyboard behaviour, and axe
// (WCAG 2.1 A/AA rules that need no layout) on each.
import { useRef, useState } from 'react'
import { describe, it, expect, vi } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import axe from 'axe-core'
import {
  Badge, Button, CodeBlock, CodeInline, ConfirmDialog, Dialog, Drawer, Icon, ICON_NAMES, IconButton, Kbd, MenuButton, Stack,
  StatusChip, TabPanel, Tabs, Tag, TONES, toneVars,
} from '.'

async function expectAccessible(container: Element) {
  // color-contrast needs layout (jsdom has none): contrast.test.ts checks the token pairs.
  const result = await axe.run(container, { runOnly: ['wcag2a', 'wcag2aa'], rules: { 'color-contrast': { enabled: false } } })
  expect(result.violations.map((v) => `${v.id}: ${v.nodes.map((n) => n.html).join(' | ')}`)).toEqual([])
}

describe('Button and IconButton', () => {
  it('render variants, sizes and icons, and are accessible', async () => {
    const onClick = vi.fn()
    const { container } = render(
      <div>
        <Button variant="primary" onClick={onClick}>Promote prod</Button>
        <Button variant="destructive" size="sm" icon="rollback">Roll back prod</Button>
        <Button variant="ghost" size="lg" busy>Pause</Button>
        <IconButton label="Close" icon="close" />
      </div>,
    )
    fireEvent.click(screen.getByRole('button', { name: 'Promote prod' }))
    expect(onClick).toHaveBeenCalledOnce()
    expect(screen.getByRole('button', { name: 'Promote prod' })).toHaveClass('k-button--primary', 'k-button--md')
    expect(screen.getByRole('button', { name: 'Roll back prod' })).toHaveClass('k-button--destructive', 'k-button--sm')
    const busy = screen.getByRole('button', { name: 'Pause' })
    expect(busy).toBeDisabled()
    expect(busy).toHaveAttribute('aria-busy', 'true')
    expect(screen.getByRole('button', { name: 'Close' })).toHaveAttribute('title', 'Close')
    expect(screen.getByRole('button', { name: 'Promote prod' })).toHaveAttribute('type', 'button')
    await expectAccessible(container)
  })
})

describe('StatusChip, Badge, Tag', () => {
  it('a chip shows its label and a decorative glyph, coloured by tone', async () => {
    const { container } = render(<div>{TONES.map((t) => <StatusChip key={t} tone={t} label={t} glyph="●" />)}</div>)
    for (const t of TONES) expect(screen.getByText(t)).toHaveClass(`k-chip--${t}`)
    expect(container.querySelector('.k-chip__glyph')).toHaveAttribute('aria-hidden', 'true')
    await expectAccessible(container)
  })

  it('every tone maps to tokens; failed is the only filled chip', () => {
    for (const t of TONES) {
      const v = toneVars(t)
      expect(v.fg).toMatch(/^var\(--k-/)
    }
    expect(toneVars('failed').bg).toBe('var(--k-status-failed-fill)')
    expect(toneVars('degraded').bg).toBe('transparent')
  })

  it('a badge caps at max', () => {
    render(<div><Badge count={3} /><Badge count={140} /><Badge count={12} max={9} tone="failed" /></div>)
    expect(screen.getByText('3')).toBeInTheDocument()
    expect(screen.getByText('99+')).toBeInTheDocument()
    expect(screen.getByText('9+')).toHaveClass('k-badge--failed')
  })

  it('a tag names the Bundle type for screen readers', async () => {
    const { container } = render(<div><Tag kind="image" /><Tag kind="mixed" /><Tag kind="chart" /><Tag kind="config" /></div>)
    expect(screen.getByRole('img', { name: 'image Bundle' })).toHaveTextContent('■ IMG')
    expect(screen.getByRole('img', { name: 'image and config Bundle' })).toHaveTextContent('■◆ MIX')
    await expectAccessible(container)
  })
})

describe('Icon', () => {
  it('is decorative unless labelled', () => {
    const { container } = render(<div><Icon name="gate" /><Icon name="warning" label="Warning" size={20} /></div>)
    expect(container.querySelector('svg')).toHaveAttribute('aria-hidden', 'true')
    expect(screen.getByRole('img', { name: 'Warning' })).toHaveAttribute('width', '20')
  })

  it('draws every name', () => {
    for (const name of ICON_NAMES) {
      const { container, unmount } = render(<Icon name={name} />)
      expect(container.querySelectorAll('path').length, name).toBeGreaterThan(0)
      unmount()
    }
  })
})

describe('Dialog and ConfirmDialog', () => {
  function Harness({ onConfirm = vi.fn() }: { onConfirm?: () => void }) {
    const [open, setOpen] = useState(false)
    return (
      <>
        <button onClick={() => setOpen(true)}>Roll back prod…</button>
        <ConfirmDialog
          open={open}
          title="Roll back prod?"
          body="Opens a PR that restores 1.4.1."
          confirmLabel="Roll back prod"
          tone="destructive"
          onConfirm={onConfirm}
          onCancel={() => setOpen(false)}
        />
      </>
    )
  }

  it('is a labelled modal that starts on Cancel, closes on Esc and gives focus back', async () => {
    const user = userEvent.setup()
    render(<Harness />)
    const trigger = screen.getByRole('button', { name: 'Roll back prod…' })
    await user.click(trigger)
    const dialog = screen.getByRole('dialog', { name: 'Roll back prod?' })
    expect(dialog).toHaveAttribute('aria-modal', 'true')
    expect(dialog).toHaveAccessibleDescription('Opens a PR that restores 1.4.1.')
    expect(screen.getByRole('button', { name: 'Cancel' })).toHaveFocus()
    expect(screen.getByRole('button', { name: 'Roll back prod' })).toHaveClass('k-button--destructive')
    await expectAccessible(document.body)
    await user.keyboard('{Escape}')
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(trigger).toHaveFocus()
  })

  it('a busy dialog does not close and disables its buttons; an error is an alert', () => {
    const onClose = vi.fn()
    render(
      <ConfirmDialog open title="Pause checkout?" body="b" confirmLabel="Pause checkout" onConfirm={vi.fn()} onCancel={onClose} busy error="The API answered 403" />,
    )
    fireEvent.keyDown(document, { key: 'Escape' })
    expect(onClose).not.toHaveBeenCalled()
    expect(screen.getByRole('button', { name: 'Cancel' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Pause checkout' })).toBeDisabled()
    expect(screen.getByRole('alert')).toHaveTextContent('The API answered 403')
  })

  it('a closed dialog renders nothing', () => {
    render(<Dialog open={false} title="x" onClose={vi.fn()} />)
    expect(screen.queryByRole('dialog')).toBeNull()
  })
})

describe('Drawer', () => {
  it('is a complementary region beside the page, or a modal dialog', async () => {
    const onClose = vi.fn()
    const { rerender, container } = render(<Drawer open title="prod" onClose={onClose}>steps</Drawer>)
    expect(screen.getByRole('complementary', { name: 'prod' })).toHaveTextContent('steps')
    fireEvent.click(screen.getByRole('button', { name: 'Close' }))
    expect(onClose).toHaveBeenCalledOnce()
    await expectAccessible(container)
    rerender(<Drawer open modal title="prod" onClose={onClose}>steps</Drawer>)
    expect(screen.getByRole('dialog', { name: 'prod' })).toHaveAttribute('aria-modal', 'true')
  })
})

describe('Tabs', () => {
  function Harness() {
    const [tab, setTab] = useState('map')
    const items = [{ id: 'map', label: 'Map' }, { id: 'table', label: 'Table' }, { id: 'gates', label: 'Gates', count: 2 }]
    return (
      <>
        <Tabs value={tab} onChange={setTab} items={items} label="Pipeline views" />
        <TabPanel id={tab}>{tab} panel</TabPanel>
      </>
    )
  }

  it('one Tab stop; arrows, Home and End move and select', async () => {
    const user = userEvent.setup()
    const { container } = render(<Harness />)
    const map = screen.getByRole('tab', { name: 'Map' })
    expect(map).toHaveAttribute('aria-selected', 'true')
    expect(screen.getByRole('tab', { name: 'Table' })).toHaveAttribute('tabindex', '-1')
    map.focus()
    await user.keyboard('{ArrowRight}')
    expect(screen.getByRole('tab', { name: 'Table' })).toHaveFocus()
    expect(screen.getByRole('tabpanel', { name: 'Table' })).toHaveTextContent('table panel')
    await user.keyboard('{End}')
    expect(screen.getByRole('tab', { name: /Gates/ })).toHaveAttribute('aria-selected', 'true')
    await user.keyboard('{ArrowRight}')
    expect(map).toHaveFocus()
    await expectAccessible(container)
  })
})

describe('Code, Kbd, Stack', () => {
  it('render with their classes', async () => {
    function Ref() {
      const r = useRef<HTMLDivElement>(null)
      return <Stack ref={r} direction="row" gap="3" align="center" wrap><CodeInline>sha256:9f3e</CodeInline><Kbd>⌘K</Kbd></Stack>
    }
    const { container } = render(<div><Ref /><CodeBlock language="cel" copy>{'!schedule.isWeekend'}</CodeBlock></div>)
    expect(container.querySelector('.k-stack')).toHaveClass('k-stack--row', 'k-stack--gap-3', 'k-stack--align-center', 'k-stack--wrap')
    expect(screen.getByText('!schedule.isWeekend').closest('pre')).toHaveAttribute('data-language', 'cel')
    expect(screen.getByRole('button', { name: 'Copy cel' })).toBeInTheDocument()
    await expectAccessible(container)
  })
})

describe('MenuButton', () => {
  it('opens on click, focuses the first control, and closes on Esc with focus back', async () => {
    const user = userEvent.setup()
    const { container } = render(
      <MenuButton label="More actions"><button>Pause pipeline</button><button>Copy link</button></MenuButton>,
    )
    const toggle = screen.getByRole('button', { name: 'More actions' })
    expect(toggle).toHaveAttribute('aria-expanded', 'false')
    expect(screen.queryByRole('button', { name: 'Pause pipeline' })).toBeNull()
    await user.click(toggle)
    expect(toggle).toHaveAttribute('aria-expanded', 'true')
    expect(screen.getByRole('group', { name: 'More actions' })).toBeVisible()
    expect(screen.getByRole('button', { name: 'Pause pipeline' })).toHaveFocus()
    await expectAccessible(container)
    await user.keyboard('{Escape}')
    expect(toggle).toHaveAttribute('aria-expanded', 'false')
    expect(toggle).toHaveFocus()
  })

  it('closes on a click outside', async () => {
    const user = userEvent.setup()
    render(<div><MenuButton label="More actions"><button>Pause pipeline</button></MenuButton><p>outside</p></div>)
    await user.click(screen.getByRole('button', { name: 'More actions' }))
    await user.click(screen.getByText('outside'))
    expect(screen.getByRole('button', { name: 'More actions' })).toHaveAttribute('aria-expanded', 'false')
  })
})
