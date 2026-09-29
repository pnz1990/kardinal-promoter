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
import { useState, useCallback, useEffect, useRef } from 'react'

interface Props {
  text: string
  /** Optional title override for the button. Defaults to "Copy to clipboard". */
  title?: string
  /**
   * Optional tabIndex override. Pass -1 when the button is nested inside another
   * interactive element (e.g. a selection <button>) to prevent nested-interactive
   * axe-core violation (WCAG 2.1 §4.1.3 — nested focusable elements).
   */
  tabIndex?: number
}

/** Copy with a hidden textarea; returns false when the browser refuses. */
function execCommandCopy(text: string): boolean {
  const el = document.createElement('textarea')
  el.value = text
  el.setAttribute('readonly', '')
  el.style.position = 'fixed'
  el.style.opacity = '0'
  document.body.appendChild(el)
  el.select()
  let ok = false
  try {
    ok = document.execCommand('copy')
  } catch {
    ok = false
  }
  document.body.removeChild(el)
  return ok
}

type CopyState = 'idle' | 'copied' | 'failed'

/**
 * CopyButton — copies `text` to clipboard on click.
 * Shows a checkmark for 2 seconds on success.
 * Uses execCommand when the Clipboard API is missing (plain-HTTP port-forward)
 * or refuses, and says so when both fail.
 */
export default function CopyButton({ text, title, tabIndex }: Props) {
  const [state, setState] = useState<CopyState>('idle')
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)
  useEffect(() => () => clearTimeout(timer.current), [])

  const show = useCallback((next: CopyState) => {
    setState(next)
    clearTimeout(timer.current)
    timer.current = setTimeout(() => setState('idle'), 2000)
  }, [])

  const handleCopy = useCallback(() => {
    const fallback = () => show(execCommandCopy(text) ? 'copied' : 'failed')
    const clip = typeof navigator !== 'undefined' ? navigator.clipboard : undefined
    if (!clip?.writeText) {
      fallback()
      return
    }
    clip.writeText(text).then(() => show('copied'), fallback)
  }, [text, show])

  const label = state === 'copied'
    ? 'Copied!'
    : state === 'failed'
      ? 'Copy failed: select the text and copy it by hand'
      : (title ?? 'Copy to clipboard')

  return (
    <button
      data-testid="copy-button"
      onClick={handleCopy}
      tabIndex={tabIndex}
      title={label}
      aria-label={label}
      style={{
        background: 'none',
        border: '1px solid var(--color-border)',
        borderRadius: '4px',
        padding: '1px 6px',
        cursor: 'pointer',
        fontSize: '0.7rem',
        color: state === 'copied' ? 'var(--color-success)' : state === 'failed' ? 'var(--color-error)' : 'var(--color-text-muted)',
        transition: 'color 0.2s',
        lineHeight: 1.4,
      }}
    >
      {state === 'copied' ? '✓' : state === 'failed' ? '✕' : '📋'}
    </button>
  )
}
