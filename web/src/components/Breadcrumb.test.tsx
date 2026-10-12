// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'
import { Breadcrumb } from './Breadcrumb'
import { EMPTY_URL_STATE } from '../useUrlState'

describe('Breadcrumb', () => {
  it('names a followed version by its tag, the Bundle name when it has none', () => {
    const route = { ...EMPTY_URL_STATE, pipeline: 'checkout', ns: 'shop', version: 'checkout-1-8-0' }
    const { rerender } = render(<Breadcrumb route={route} versionLabel="1.8.0" />)
    expect(screen.getByRole('navigation', { name: 'Breadcrumb' })).toHaveTextContent('Pipelinesshopcheckout1.8.0')
    rerender(<Breadcrumb route={route} />)
    expect(screen.getByText('checkout-1-8-0')).toHaveAttribute('aria-current', 'page')
  })
})
