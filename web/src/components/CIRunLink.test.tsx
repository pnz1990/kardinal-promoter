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

// CIRunLink.test.tsx — the bundle header links provenance.ciRunURL only when
// it is an http(s) URL, and shows "—" otherwise, never the value (E2E-R22).
import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'
import { CIRunLink } from './CIRunLink'

describe('CIRunLink', () => {
  it.each([
    { name: 'https', url: 'https://github.com/o/r/actions/runs/1', linked: true },
    { name: 'http', url: 'http://jenkins.local:8080/job/1', linked: true },
    { name: 'javascript', url: 'javascript:alert(document.domain)', linked: false },
    { name: 'data', url: 'data:text/html,<script>alert(1)</script>', linked: false },
    { name: 'relative', url: '/o/r/actions/runs/1', linked: false },
    { name: 'user info', url: 'https://u:s3cret@ci.example.com/1', linked: false },
    { name: 'credential parsed as a port', url: 'https://user:s3cret/x', linked: false },
    { name: 'whitespace', url: 'https://ci.example.com/1 [x](y)', linked: false },
  ])('$name', ({ url, linked }) => {
    const { container } = render(<CIRunLink url={url} />)
    if (linked) {
      const link = screen.getByRole('link', { name: 'CI run ↗' })
      expect(link).toHaveAttribute('href', url)
      expect(link).toHaveAttribute('rel', 'noopener noreferrer')
      return
    }
    expect(screen.queryByRole('link')).not.toBeInTheDocument()
    expect(screen.getByText('CI run: —')).toBeInTheDocument()
    expect(container.innerHTML).not.toContain(url.slice(0, 12))
    expect(container.innerHTML).not.toContain('s3cret')
  })
})
