// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

import { describe, expect, it } from 'vitest'
import { render, screen } from '@testing-library/react'
import { BundleTypeBadge, bundleTypeLabel } from './BundleTypeBadge'

describe('BundleTypeBadge', () => {
  it('names each Bundle type, and an unknown one as itself', () => {
    expect(bundleTypeLabel('image')).toBe('image')
    expect(bundleTypeLabel(undefined)).toBe('image')
    expect(bundleTypeLabel('config')).toBe('config')
    expect(bundleTypeLabel('mixed')).toBe('image + config')
    expect(bundleTypeLabel('chart')).toBe('chart')
    expect(bundleTypeLabel('Rendered')).toBe('rendered')
  })

  it('says what kind of change a Bundle carries, to a screen reader too', () => {
    render(<>
      <BundleTypeBadge type="config" />
      <BundleTypeBadge type="mixed" />
      <BundleTypeBadge type="chart" />
      <BundleTypeBadge type="rendered" />
    </>)
    expect(screen.getByRole('img', { name: 'config Bundle' })).toHaveTextContent('config')
    expect(screen.getByRole('img', { name: 'image and config Bundle' })).toHaveTextContent('image + config')
    expect(screen.getByRole('img', { name: 'Helm chart Bundle' })).toHaveAttribute('data-bundle-type', 'chart')
    expect(screen.getByRole('img', { name: 'rendered Bundle' })).toHaveAttribute('data-bundle-type', 'other')
  })

  it('compact: image Bundles, the common case, show no badge', () => {
    const { container } = render(<BundleTypeBadge type="image" compact />)
    expect(container).toBeEmptyDOMElement()
    render(<BundleTypeBadge type="config" compact />)
    expect(screen.getByRole('img', { name: 'config Bundle' })).toHaveClass('bundle-type--compact')
  })

  it('full: an image Bundle is labelled too', () => {
    render(<BundleTypeBadge type="image" />)
    expect(screen.getByRole('img', { name: 'image Bundle' })).toBeInTheDocument()
  })
})
