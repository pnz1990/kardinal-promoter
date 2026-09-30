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
// it is an http(s) URL (E2E-R22).
import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'
import { CIRunLink } from './CIRunLink'

describe('CIRunLink', () => {
  it.each([
    { name: 'https', url: 'https://github.com/o/r/actions/runs/1', href: 'https://github.com/o/r/actions/runs/1' },
    { name: 'http', url: 'http://jenkins.local:8080/job/1', href: 'http://jenkins.local:8080/job/1' },
    { name: 'javascript', url: 'javascript:alert(document.domain)', text: 'javascript:alert(document.domain)' },
    { name: 'data', url: 'data:text/html,<script>alert(1)</script>', text: 'data:text/html,<script>alert(1)</script>' },
    { name: 'relative', url: '/o/r/actions/runs/1', text: '/o/r/actions/runs/1' },
    { name: 'user info', url: 'https://github.com@evil.example/1', text: 'https://github.com@evil.example/1' },
    {
      name: 'long text is shortened',
      url: 'javascript:' + 'a'.repeat(60),
      text: 'javascript:' + 'a'.repeat(28) + '…',
    },
  ])('$name', ({ url, href, text }) => {
    render(<CIRunLink url={url} />)
    if (href) {
      const link = screen.getByRole('link', { name: 'CI run ↗' })
      expect(link).toHaveAttribute('href', href)
      expect(link).toHaveAttribute('rel', 'noopener noreferrer')
    } else {
      expect(screen.queryByRole('link')).not.toBeInTheDocument()
      expect(screen.getByText(text!)).toBeInTheDocument()
    }
  })
})
