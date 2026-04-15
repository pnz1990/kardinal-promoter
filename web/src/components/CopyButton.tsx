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
//
// components/CopyButton.tsx — Reusable copy-to-clipboard button (#530).
// Extracted from NodeDetail.tsx to enable use in EmptyState and other components.
// Shows 📋 → ✓ on success for 2 seconds.
import { useState, useCallback } from 'react'

interface Props {
  text: string
  size?: 'sm' | 'md'
  'aria-label'?: string
}

/**
 * CopyButton renders a clipboard icon that copies `text` on click.
 * Shows a ✓ checkmark for 2 seconds after a successful copy.
 */
export function CopyButton({ text, size = 'sm', 'aria-label': ariaLabel }: Props) {
  const [copied, setCopied] = useState(false)

  const handleCopy = useCallback(() => {
    const reset = () => {
      setCopied(true)
      setTimeout(() => setCopied(false), 2000)
    }
    navigator.clipboard.writeText(text).then(reset).catch(() => {
      // Fallback for environments without clipboard API (e.g. http dev server)
      try {
        const el = document.createElement('textarea')
        el.value = text
        el.style.position = 'fixed'
        el.style.opacity = '0'
        document.body.appendChild(el)
        el.select()
        document.execCommand('copy')
        document.body.removeChild(el)
        reset()
      } catch {
        // silently fail — clipboard is best-effort
      }
    })
  }, [text])

  const fontSize = size === 'md' ? '0.8rem' : '0.7rem'

  return (
    <button
      onClick={handleCopy}
      title={copied ? 'Copied!' : 'Copy to clipboard'}
      aria-label={ariaLabel ?? (copied ? 'Copied' : 'Copy to clipboard')}
      aria-pressed={copied}
      data-testid="copy-button"
      style={{
        background: 'none',
        border: '1px solid #334155',
        borderRadius: '4px',
        padding: size === 'md' ? '2px 8px' : '1px 6px',
        cursor: 'pointer',
        fontSize,
        color: copied ? '#86efac' : '#94a3b8',
        transition: 'color 0.2s',
        lineHeight: 1.4,
      }}
    >
      {copied ? '✓' : '📋'}
    </button>
  )
}
