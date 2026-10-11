// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// components/InsecureConnectionBanner.tsx — Warning banner when UI is accessed over HTTP
// from a non-localhost origin. Prompts users to use kubectl port-forward for in-cluster access.
//
// Design doc: docs/design/06-kardinal-ui.md §Future — port-forward UX (#913)

import '../styles/InsecureConnectionBanner.css'

interface InsecureConnectionBannerProps {
  /** Whether the banner has been dismissed by the user. */
  dismissed: boolean
  /** Callback to dismiss the banner. */
  onDismiss: () => void
  /** A class from the page that places it, for its margins (default: space below it only). */
  className?: string
}

/** The command that reaches the UI on a default install (release kardinal-promoter). */
export const PORT_FORWARD_COMMAND = 'kubectl port-forward svc/kardinal-promoter -n kardinal-system 8082:8082'

/** Loopback host names. `location.hostname` keeps the brackets on IPv6 ("[::1]"). */
const LOOPBACK_HOSTS = new Set(['localhost', '127.0.0.1', '[::1]', '::1'])

/**
 * Returns true when the page is accessed over plain HTTP from a non-localhost origin.
 * Port-forward to loopback (localhost, 127.0.0.1 or [::1]) is safe and is the
 * documented access method — it must NOT trigger the warning.
 */
export function isInsecureNonLocalConnection(): boolean {
  if (typeof window === 'undefined') return false
  if (window.location.protocol === 'https:') return false
  return !LOOPBACK_HOSTS.has(window.location.hostname)
}

/**
 * InsecureConnectionBanner renders an amber warning banner when the UI is accessed
 * over plain HTTP from a non-localhost address. It is dismissible for the session.
 * App shows it at the top of every view, including the empty and error states:
 * the connection is insecure before any pipeline loads.
 */
export function InsecureConnectionBanner({ dismissed, onDismiss, className }: InsecureConnectionBannerProps) {
  if (dismissed) return null
  if (!isInsecureNonLocalConnection()) return null

  return (
    <div
      role="alert"
      className={className ? `insecure-banner ${className}` : 'insecure-banner'}
    >
      <div className="insecure-banner__message">
        <span className="insecure-banner__icon" aria-hidden="true">⚠</span>
        <span className="insecure-banner__text">
          Insecure connection — kardinal UI is accessed over plain HTTP.{' '}
          Use{' '}
          <code className="insecure-banner__code">
            {PORT_FORWARD_COMMAND}
          </code>{' '}
          and open{' '}
          <a href="http://localhost:8082/ui/" className="insecure-banner__link">
            http://localhost:8082/ui/
          </a>
          {' '}for secure in-cluster access.
        </span>
      </div>
      <button
        type="button"
        onClick={onDismiss}
        aria-label="Dismiss insecure connection warning"
        className="insecure-banner__dismiss"
      >
        Dismiss
      </button>
    </div>
  )
}
