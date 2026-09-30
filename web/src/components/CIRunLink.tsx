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

// CIRunLink.tsx — a Bundle's provenance.ciRunURL in the bundle header.
// Only a URL that passes isCIRunURL is linked; anything else is shown as text.
import { isCIRunURL } from '../prLink'

const MAX_TEXT = 40

export function CIRunLink({ url }: { url: string }) {
  if (isCIRunURL(url)) {
    return (
      <a
        href={url}
        target="_blank"
        rel="noopener noreferrer"
        style={{ color: 'var(--color-accent)', fontSize: '0.78rem' }}
        title="CI run"
      >
        CI run ↗
      </a>
    )
  }
  const text = url.length > MAX_TEXT ? url.slice(0, MAX_TEXT - 1) + '…' : url
  return (
    <span
      style={{ color: 'var(--color-text-muted)', fontSize: '0.78rem' }}
      title="Not linked: the CI run URL is not an http(s) URL"
    >
      CI run: <span style={{ fontFamily: 'monospace', overflowWrap: 'anywhere' }}>{text}</span>
    </span>
  )
}
