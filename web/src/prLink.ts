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

// A credential query or fragment parameter, as pkg/urlsecret decides: the
// name, percent-decoded, lower-cased and without "-" and "_", ends in one
// of CREDENTIAL_SUFFIXES or is one of CREDENTIAL_EXACT (a Go test pins the
// lists to urlsecret.Suffixes and urlsecret.Exact).
const CREDENTIAL_PARAM = /[?&;#]([^=&;#\s]+)=/g
const CREDENTIAL_SUFFIXES = [
  'token', 'secret', 'signature', 'password', 'passwd', 'credential', 'credentials',
  'apikey', 'accesskey', 'secretkey', 'privatekey', 'accesskeyid',
]
const CREDENTIAL_EXACT = ['sig', 'key', 'auth', 'jwt', 'code', 'pass', 'pwd']

function isCredentialName(raw: string): boolean {
  let name = raw
  try {
    name = decodeURIComponent(raw.replace(/\+/g, ' '))
  } catch {
    // not percent-encoded as a whole: compare it as written
  }
  name = name.toLowerCase().replace(/[-_]/g, '')
  return CREDENTIAL_EXACT.includes(name) || CREDENTIAL_SUFFIXES.some((s) => name.endsWith(s))
}

function hasCredentialParam(url: string): boolean {
  for (const m of url.matchAll(CREDENTIAL_PARAM)) {
    if (isCredentialName(m[1])) return true
  }
  return false
}

// The authority of an http(s) URL up to the first "/": a raw "#", "?" or
// space in a password is still the authority, so its "@" is seen.
const AUTHORITY_TO_PATH = /^https?:\/\/([^/]*)/i

/**
 * True when a Bundle's provenance.ciRunURL may be linked. Mirrors
 * graph.ValidateCIRunURL, which the controller applies to new Bundles: an
 * absolute http(s) URL with a host, no user info (https://github.com@evil.example
 * goes to evil.example), no credential parameter (?token=, #sig=, ...), and
 * no whitespace or control characters. Bundles created before that check
 * can hold any string.
 */
export function isCIRunURL(url: string | undefined): boolean {
  if (!isHttpURL(url) || SPACE_OR_CONTROL.test(url) || hasCredentialParam(url)) return false
  const authority = HTTP_AUTHORITY.exec(url)?.[1]
  return !!authority && !AUTHORITY_TO_PATH.exec(url)?.[1]?.includes('@')
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
