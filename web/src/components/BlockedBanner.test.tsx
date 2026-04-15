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

// BlockedBanner.test.tsx — Tests for the blocked PolicyGate banner (#533).
import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { BlockedBanner } from './BlockedBanner'

describe('BlockedBanner — hidden when no gates are blocked', () => {
  it('renders nothing when blockedCount=0', () => {
    const { container } = render(
      <BlockedBanner blockedCount={0} highlightActive={false} onToggleHighlight={vi.fn()} />
    )
    expect(container.firstChild).toBeNull()
  })
})

describe('BlockedBanner — visible when gates are blocked', () => {
  it('shows singular message for 1 blocked gate', () => {
    render(<BlockedBanner blockedCount={1} highlightActive={false} onToggleHighlight={vi.fn()} />)
    expect(screen.getByText('1 PolicyGate blocking promotion')).toBeInTheDocument()
  })

  it('shows plural message for multiple blocked gates', () => {
    render(<BlockedBanner blockedCount={5} highlightActive={false} onToggleHighlight={vi.fn()} />)
    expect(screen.getByText('5 PolicyGates blocking promotion')).toBeInTheDocument()
  })

  it('has role=alert for screen reader accessibility', () => {
    render(<BlockedBanner blockedCount={2} highlightActive={false} onToggleHighlight={vi.fn()} />)
    expect(screen.getByRole('alert')).toBeInTheDocument()
  })

  it('shows "Show blocked" button when highlight is inactive', () => {
    render(<BlockedBanner blockedCount={1} highlightActive={false} onToggleHighlight={vi.fn()} />)
    expect(screen.getByRole('button', { name: /Show blocked/i })).toBeInTheDocument()
  })

  it('shows "Show all" button when highlight is active', () => {
    render(<BlockedBanner blockedCount={1} highlightActive={true} onToggleHighlight={vi.fn()} />)
    expect(screen.getByRole('button', { name: /Show all/i })).toBeInTheDocument()
  })

  it('button has aria-pressed=false when highlight inactive', () => {
    render(<BlockedBanner blockedCount={1} highlightActive={false} onToggleHighlight={vi.fn()} />)
    expect(screen.getByRole('button')).toHaveAttribute('aria-pressed', 'false')
  })

  it('button has aria-pressed=true when highlight active', () => {
    render(<BlockedBanner blockedCount={1} highlightActive={true} onToggleHighlight={vi.fn()} />)
    expect(screen.getByRole('button')).toHaveAttribute('aria-pressed', 'true')
  })
})

describe('BlockedBanner — toggle interaction', () => {
  it('calls onToggleHighlight when button is clicked', async () => {
    const user = userEvent.setup()
    const onToggle = vi.fn()
    render(<BlockedBanner blockedCount={1} highlightActive={false} onToggleHighlight={onToggle} />)
    await user.click(screen.getByRole('button'))
    expect(onToggle).toHaveBeenCalledTimes(1)
  })
})
