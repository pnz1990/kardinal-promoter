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

// components/CopyButton.tsx — Reusable copy-to-clipboard button.
// Extracted from NodeDetail.tsx for use in EmptyState and other components (#530).
import { useState, useCallback } from 'react'

interface Props {
  text: string
  /** Optional title override for the button. Defaults to "Copy to clipboard". */
  title?: string
  /**
   * Optional tabIndex. Set to -1 when embedded inside another focusable element
   * (e.g. a button) to avoid nested-interactive WCAG violation.
   */
  tabIndex?: number
}

/**
 * CopyButton — copies `text` to clipboard on click.
 * Shows a checkmark for 2 seconds on success.
 * Falls back to execCommand for environments without Clipboard API.
 */
export default function CopyButton({ text, title, tabIndex }: Props) {
  const [copied, setCopied] = useState(false)
  const handleCopy = useCallback(() => {
    navigator.clipboard.writeText(text).then(() => {
      setCopied(true)
      setTimeout(() => setCopied(false), 2000)
    }).catch(() => {
      // Fallback for environments without Clipboard API
      const el = document.createElement('textarea')
      el.value = text
      el.style.position = 'fixed'
      el.style.opacity = '0'
      document.body.appendChild(el)
      el.select()
      document.execCommand('copy')
      document.body.removeChild(el)
      setCopied(true)
      setTimeout(() => setCopied(false), 2000)
    })
  }, [text])

  return (
    <button
      data-testid="copy-button"
      onClick={handleCopy}
      tabIndex={tabIndex}
      title={copied ? 'Copied!' : (title ?? 'Copy to clipboard')}
      style={{
        background: 'none',
        border: '1px solid #334155',
        borderRadius: '4px',
        padding: '1px 6px',
        cursor: 'pointer',
        fontSize: '0.7rem',
        color: copied ? '#86efac' : 'var(--color-text-muted)',
        transition: 'color 0.2s',
        lineHeight: 1.4,
      }}
    >
      {copied ? '✓' : '📋'}
    </button>
  )
}
