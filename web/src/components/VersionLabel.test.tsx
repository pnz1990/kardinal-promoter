// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

import { describe, it, expect } from 'vitest'
import { render } from '@testing-library/react'
import { VersionLabel } from './VersionLabel'
import type { Bundle } from '../types'

const b = (over: Partial<Bundle>): Bundle => ({ name: 'checkout-1-8-0', namespace: 'shop', phase: 'Promoting', type: 'image', pipeline: 'checkout', ...over })

describe('VersionLabel', () => {
  it('names a version by what it ships, with the Bundle name after it', () => {
    const { container } = render(<VersionLabel bundle={b({ images: [{ repository: 'r', tag: '1.8.0' }] })} />)
    expect(container.querySelector('.version-label__tag')).toHaveTextContent('1.8.0')
    expect(container.querySelector('.version-label__name')).toHaveTextContent('checkout-1-8-0')
  })

  it('compact: the Bundle name goes in the title', () => {
    const { container } = render(<VersionLabel bundle={b({ version: '1.8.0' })} compact />)
    expect(container.querySelector('.version-label__name')).toBeNull()
    expect(container.querySelector('.version-label')).toHaveAttribute('title', 'checkout-1-8-0')
  })

  it('a Bundle that ships nothing nameable reads as its name, once', () => {
    const { container } = render(<VersionLabel bundle={b({})} />)
    expect(container.textContent).toBe('checkout-1-8-0')
  })
})
