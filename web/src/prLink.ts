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
// name, percent-decoded, without a trailing array index, lower-cased and
// without "-" and "_", is one of CREDENTIAL_EXACT, or ends in one of
// CREDENTIAL_SUFFIXES and is not one of CREDENTIAL_HARMLESS (a Go test pins
// the lists to urlsecret.Exact, Suffixes and Harmless).
const CREDENTIAL_PARAM = /[?&;#]([^=&;#\s]+)=/g
const CREDENTIAL_SUFFIXES = [
  'token', 'secret', 'signature', 'password', 'passwd', 'passphrase', 'credential', 'credentials',
  'key', 'accesskeyid', 'session', 'sessionid', 'assertion', 'ticket', 'bearer',
]
const CREDENTIAL_EXACT = ['sig', 'auth', 'authorization', 'jwt', 'code', 'pass', 'pwd', 'sid']
const CREDENTIAL_HARMLESS = [
  'monkey', 'donkey', 'turkey', 'jockey', 'hockey', 'whiskey', 'hotkey', 'sortkey', 'primarykey',
  'foreignkey', 'partitionkey', 'groupkey', 'cachekey', 'lookupkey',
  'buildkey', 'plankey', 'jobkey', 'resultkey', 'buildresultkey', 'chainkey', 'stagekey', 'projectkey',
  'issuekey', 'branchkey',
]

function isCredentialName(raw: string): boolean {
  let name = raw
  try {
    name = decodeURIComponent(raw.replace(/\+/g, ' '))
  } catch {
    // not percent-encoded as a whole: compare it as written
  }
  name = name.replace(/(\[[^\]]*\])+$/, '').toLowerCase().replace(/[-_]/g, '')
  if (CREDENTIAL_EXACT.includes(name)) return true
  if (CREDENTIAL_HARMLESS.includes(name)) return false
  return CREDENTIAL_SUFFIXES.some((s) => name.endsWith(s))
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
