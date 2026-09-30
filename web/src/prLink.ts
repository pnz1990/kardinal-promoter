// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// prLink.ts — links built from cluster data: pull request links from
// PromotionStep status.prURL and CI run links from Bundle provenance.ciRunURL.
// status.prURL is written by the controller from the SCM API response, but it
// is still data from the cluster: only http(s) URLs are opened.

/** True when url parses as an absolute http: or https: URL. */
export function isHttpURL(url: string | undefined): url is string {
  if (!url) return false
  try {
    const u = new URL(url)
    return u.protocol === 'https:' || u.protocol === 'http:'
  } catch {
    return false
  }
}

// The authority of an http(s) URL written with "//": the part between "//" and
// the first "/", "?" or "#".
const HTTP_AUTHORITY = /^https?:\/\/([^/?#]*)/i

// Whitespace (including a no-break space) or a control character.
const SPACE_OR_CONTROL = /[\s\p{Cc}]/u

/**
 * True when a Bundle's provenance.ciRunURL may be linked. Mirrors
 * graph.ValidateCIRunURL, which the controller applies to new Bundles: an
 * absolute http(s) URL with a host, no user info (https://github.com@evil.example
 * goes to evil.example), and no whitespace or control characters. Bundles
 * created before that check can hold any string.
 */
export function isCIRunURL(url: string | undefined): boolean {
  if (!isHttpURL(url) || SPACE_OR_CONTROL.test(url)) return false
  const authority = HTTP_AUTHORITY.exec(url)?.[1]
  return !!authority && !authority.includes('@')
}

// GitHub /pull/N, Forgejo and Gitea /pulls/N, GitLab /-/merge_requests/N,
// Bitbucket /pull-requests/N, Azure DevOps /pullrequest/N.
const PR_NUMBER = /\/(?:pull|pulls|merge_requests|pull-requests|pullrequest)\/(\d+)\/?$/

/** "#42" for a pull request URL from any supported SCM, or null. */
export function prNumberFromURL(url: string | undefined): string | null {
  if (!isHttpURL(url)) return null
  const m = new URL(url).pathname.match(PR_NUMBER)
  return m ? `#${m[1]}` : null
}
