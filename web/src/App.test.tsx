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

// App.test.tsx — Tests for buildStaticPipelineGraph utility (issue #525).
import { describe, it, expect } from 'vitest'
import { buildStaticPipelineGraph } from './App'

describe('buildStaticPipelineGraph (#525)', () => {
  it('returns empty graph when no environments data available', () => {
    const result = buildStaticPipelineGraph({
      environmentCount: 0,
      environments: [],
      environmentStates: {},
    })
    expect(result.nodes).toHaveLength(0)
    expect(result.edges).toHaveLength(0)
  })

  it('uses environments array when provided', () => {
    const result = buildStaticPipelineGraph({
      environmentCount: 3,
      environments: ['test', 'uat', 'prod'],
    })
    expect(result.nodes).toHaveLength(3)
    expect(result.nodes[0].environment).toBe('test')
    expect(result.nodes[1].environment).toBe('uat')
    expect(result.nodes[2].environment).toBe('prod')
  })

  it('creates linear chain edges from environments array', () => {
    const result = buildStaticPipelineGraph({
      environmentCount: 3,
      environments: ['test', 'uat', 'prod'],
    })
    expect(result.edges).toHaveLength(2)
    expect(result.edges[0]).toEqual({ from: 'step-test', to: 'step-uat' })
    expect(result.edges[1]).toEqual({ from: 'step-uat', to: 'step-prod' })
  })

  it('all nodes have NotStarted state', () => {
    const result = buildStaticPipelineGraph({
      environmentCount: 3,
      environments: ['test', 'uat', 'prod'],
    })
    for (const node of result.nodes) {
      expect(node.state).toBe('NotStarted')
    }
  })

  it('all nodes have PromotionStep type', () => {
    const result = buildStaticPipelineGraph({
      environmentCount: 2,
      environments: ['dev', 'prod'],
    })
    for (const node of result.nodes) {
      expect(node.type).toBe('PromotionStep')
    }
  })

  it('falls back to environmentStates keys when environments is not provided', () => {
    const result = buildStaticPipelineGraph({
      environmentCount: 2,
      environmentStates: { test: 'Verified', prod: 'Pending' },
    })
    expect(result.nodes).toHaveLength(2)
    const envNames = result.nodes.map(n => n.environment)
    expect(envNames).toContain('test')
    expect(envNames).toContain('prod')
  })

  it('falls back to numbered placeholders when only environmentCount is available', () => {
    const result = buildStaticPipelineGraph({ environmentCount: 3 })
    expect(result.nodes).toHaveLength(3)
    expect(result.nodes[0].environment).toBe('env-1')
    expect(result.nodes[1].environment).toBe('env-2')
    expect(result.nodes[2].environment).toBe('env-3')
  })

  it('generates correct node IDs prefixed with step-', () => {
    const result = buildStaticPipelineGraph({
      environmentCount: 2,
      environments: ['staging', 'production'],
    })
    expect(result.nodes[0].id).toBe('step-staging')
    expect(result.nodes[1].id).toBe('step-production')
  })

  it('single environment — no edges', () => {
    const result = buildStaticPipelineGraph({
      environmentCount: 1,
      environments: ['prod'],
    })
    expect(result.nodes).toHaveLength(1)
    expect(result.edges).toHaveLength(0)
  })

  it('environments array takes precedence over environmentStates', () => {
    const result = buildStaticPipelineGraph({
      environmentCount: 2,
      environments: ['alpha', 'beta'],
      environmentStates: { test: 'Verified', prod: 'Pending' },
    })
    const envNames = result.nodes.map(n => n.environment)
    expect(envNames).toContain('alpha')
    expect(envNames).toContain('beta')
    expect(envNames).not.toContain('test')
    expect(envNames).not.toContain('prod')
  })
})
