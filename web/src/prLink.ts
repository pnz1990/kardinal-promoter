// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// prLink.ts — pull request links from PromotionStep status.prURL.
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

// GitHub /pull/N, Forgejo and Gitea /pulls/N, GitLab /-/merge_requests/N,
// Bitbucket /pull-requests/N, Azure DevOps /pullrequest/N.
const PR_NUMBER = /\/(?:pull|pulls|merge_requests|pull-requests|pullrequest)\/(\d+)\/?$/

/** "#42" for a pull request URL from any supported SCM, or null. */
export function prNumberFromURL(url: string | undefined): string | null {
  if (!isHttpURL(url)) return null
  const m = new URL(url).pathname.match(PR_NUMBER)
  return m ? `#${m[1]}` : null
}
