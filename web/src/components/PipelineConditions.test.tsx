// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

import { describe, it, expect } from 'vitest'
import { render, screen, within } from '@testing-library/react'
import axe from 'axe-core'
import { PipelineConditions, fixCommand } from './PipelineConditions'

// The messages the PipelineReconciler writes (pkg/reconciler/pipeline).
const MISSING = 'git Secret git-token not found in namespace team-a, so git has no token for https://github.com/o/r; create it: '
  + 'kubectl create secret generic git-token -n team-a --from-literal=token=<TOKEN> && kubectl label secret git-token -n team-a kardinal.io/referenceable=true'
const UNSET = 'spec.git.secretRef is not set, so git has no token for https://github.com/o/r; create the Secret '
  + '(kubectl create secret generic git-token -n team-a --from-literal=token=<TOKEN> && kubectl label secret git-token -n team-a kardinal.io/referenceable=true) '
  + 'and set spec.git.secretRef.name: git-token'
const UNLABELLED = 'git Secret gh is not labeled kardinal.io/referenceable=true, so git does not use it; label it: '
  + 'kubectl label secret gh -n team-a kardinal.io/referenceable=true'

describe('fixCommand', () => {
  it.each<[string, string]>([
    [MISSING, 'kubectl create secret generic git-token -n team-a --from-literal=token=<TOKEN> && kubectl label secret git-token -n team-a kardinal.io/referenceable=true'],
    [UNSET, 'kubectl create secret generic git-token -n team-a --from-literal=token=<TOKEN> && kubectl label secret git-token -n team-a kardinal.io/referenceable=true'],
    [UNLABELLED, 'kubectl label secret gh -n team-a kardinal.io/referenceable=true'],
  ])('finds the command in %s', (message, want) => {
    expect(fixCommand(message)).toBe(want)
  })

  it('has none for a message without one', () => {
    expect(fixCommand('spec.environments[1].name is empty')).toBeUndefined()
  })
})

describe('PipelineConditions', () => {
  it('shows nothing for a Ready pipeline with a labelled Secret', () => {
    const { container } = render(<PipelineConditions conditions={[{ type: 'Ready', status: 'True' }]} />)
    expect(container).toBeEmptyDOMElement()
  })

  it('shows GitCredentialMissing as an error with the command to copy', async () => {
    const { container } = render(<PipelineConditions conditions={[
      { type: 'Ready', status: 'False', reason: 'GitCredentialMissing', message: MISSING },
    ]} />)
    const error = screen.getByRole('alert')
    expect(error).toHaveTextContent('ErrorGit has no token for this pipeline, so nothing can be promoted')
    expect(within(error).getByRole('button', { name: 'Copy shell' })).toBeInTheDocument()
    expect(error.querySelector('pre')).toHaveTextContent(/^kubectl create secret generic git-token/)

    const result = await axe.run(container, { runOnly: ['wcag2a', 'wcag2aa'], rules: { 'color-contrast': { enabled: false } } })
    expect(result.violations.map(v => v.id)).toEqual([])
  })

  it('shows an unlabelled git Secret as an error with the label command', () => {
    render(<PipelineConditions conditions={[{ type: 'Ready', status: 'False', reason: 'SecretNotReferenceable', message: UNLABELLED }]} />)
    const error = screen.getByRole('alert')
    expect(error).toHaveTextContent('ErrorGit does not use the git Secret until it is labelled, so nothing can be promoted')
    expect(error.querySelector('pre')).toHaveTextContent('kubectl label secret gh -n team-a kardinal.io/referenceable=true')
    expect(screen.queryByRole('status')).toBeNull()
  })

  it('names any other reason a pipeline is not ready', () => {
    render(<PipelineConditions conditions={[{ type: 'Ready', status: 'False', reason: 'ValidationFailed', message: 'spec.environments[1].name is empty' }]} />)
    expect(screen.getByRole('alert')).toHaveTextContent('This pipeline is not ready (ValidationFailed)spec.environments[1].name is empty')
    expect(screen.getByRole('alert').querySelector('pre')).toBeNull()
  })
})
