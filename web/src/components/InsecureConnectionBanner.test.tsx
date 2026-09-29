// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// components/InsecureConnectionBanner.test.tsx — Tests for InsecureConnectionBanner (#913)

import { render, screen, fireEvent } from '@testing-library/react'
import { describe, it, expect, vi, afterEach } from 'vitest'
import { InsecureConnectionBanner, isInsecureNonLocalConnection } from './InsecureConnectionBanner'

// Helper to mock window.location for testing
function mockLocation(href: string) {
  const url = new URL(href)
  Object.defineProperty(window, 'location', {
    value: { protocol: url.protocol, hostname: url.hostname },
    writable: true,
    configurable: true,
  })
}

describe('isInsecureNonLocalConnection', () => {
  const original = window.location

  afterEach(() => {
    Object.defineProperty(window, 'location', {
      value: original,
      writable: true,
      configurable: true,
    })
  })

  it('returns true for HTTP non-localhost (O1)', () => {
    mockLocation('http://10.0.0.1:8082/ui/')
    expect(isInsecureNonLocalConnection()).toBe(true)
  })

  it('returns false for localhost (O2)', () => {
    mockLocation('http://localhost:8082/ui/')
    expect(isInsecureNonLocalConnection()).toBe(false)
  })

  it('returns false for 127.0.0.1 (O2)', () => {
    mockLocation('http://127.0.0.1:8082/ui/')
    expect(isInsecureNonLocalConnection()).toBe(false)
  })

  // Audit C10a-web-22: location.hostname keeps the brackets on IPv6.
  it.each([
    { href: 'http://[::1]:8082/ui/', want: false },
    { href: 'http://[fd00::1]:8082/ui/', want: true },
  ])('returns $want for $href', ({ href, want }) => {
    mockLocation(href)
    expect(isInsecureNonLocalConnection()).toBe(want)
  })

  it('returns false for HTTPS (O3)', () => {
    mockLocation('https://kardinal.example.com/ui/')
    expect(isInsecureNonLocalConnection()).toBe(false)
  })
})

describe('InsecureConnectionBanner', () => {
  const original = window.location

  afterEach(() => {
    Object.defineProperty(window, 'location', {
      value: original,
      writable: true,
      configurable: true,
    })
  })

  it('renders warning on HTTP non-localhost (O1)', () => {
    mockLocation('http://10.0.0.1:8082/ui/')
    render(<InsecureConnectionBanner dismissed={false} onDismiss={vi.fn()} />)
    expect(screen.getByRole('alert')).toBeDefined()
    expect(screen.getByText(/Insecure connection/)).toBeDefined()
  })

  // Audit C10a-web-04: the chart's Service has no 8082 port, so the command
  // must target the Deployment, in the release namespace.
  it('tells the user a port-forward command that reaches the UI port', () => {
    mockLocation('http://10.0.0.1:8082/ui/')
    render(<InsecureConnectionBanner dismissed={false} onDismiss={vi.fn()} />)
    expect(screen.getByRole('alert')).toHaveTextContent(
      'kubectl port-forward -n kardinal-system deploy/kardinal-promoter 8082:8082')
  })

  it('does not render on localhost (O2)', () => {
    mockLocation('http://localhost:8082/ui/')
    render(<InsecureConnectionBanner dismissed={false} onDismiss={vi.fn()} />)
    expect(screen.queryByRole('alert')).toBeNull()
  })

  it('does not render on HTTPS (O3)', () => {
    mockLocation('https://kardinal.example.com/ui/')
    render(<InsecureConnectionBanner dismissed={false} onDismiss={vi.fn()} />)
    expect(screen.queryByRole('alert')).toBeNull()
  })

  it('does not render when dismissed (O4)', () => {
    mockLocation('http://10.0.0.1:8082/ui/')
    render(<InsecureConnectionBanner dismissed={true} onDismiss={vi.fn()} />)
    expect(screen.queryByRole('alert')).toBeNull()
  })

  it('calls onDismiss when dismiss button is clicked (O4)', () => {
    mockLocation('http://10.0.0.1:8082/ui/')
    const onDismiss = vi.fn()
    render(<InsecureConnectionBanner dismissed={false} onDismiss={onDismiss} />)
    fireEvent.click(screen.getByLabelText('Dismiss insecure connection warning'))
    expect(onDismiss).toHaveBeenCalledOnce()
  })

  it('dismiss button has accessible label (O5)', () => {
    mockLocation('http://10.0.0.1:8082/ui/')
    render(<InsecureConnectionBanner dismissed={false} onDismiss={vi.fn()} />)
    expect(screen.getByRole('button', { name: 'Dismiss insecure connection warning' })).toBeDefined()
  })
})
