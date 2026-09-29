// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

import { describe, it, expect } from 'vitest'
import { isHttpURL, prNumberFromURL } from './prLink'

describe('isHttpURL', () => {
  it.each([
    { url: 'https://github.com/org/repo/pull/42', want: true },
    { url: 'http://forgejo.local/org/repo/pulls/7', want: true },
    { url: 'javascript:alert(document.domain)//x/pull/1', want: false },
    { url: 'data:text/html,<script>alert(1)</script>', want: false },
    { url: '/org/repo/pull/1', want: false },
    { url: '', want: false },
    { url: undefined, want: false },
  ])('$url → $want', ({ url, want }) => {
    expect(isHttpURL(url)).toBe(want)
  })
})

describe('prNumberFromURL', () => {
  it.each([
    { scm: 'github', url: 'https://github.com/org/repo/pull/42', want: '#42' },
    { scm: 'gitlab', url: 'https://gitlab.com/org/repo/-/merge_requests/17', want: '#17' },
    { scm: 'forgejo', url: 'https://codeberg.org/org/repo/pulls/9', want: '#9' },
    { scm: 'bitbucket', url: 'https://bitbucket.example.com/projects/P/repos/r/pull-requests/5/', want: '#5' },
    { scm: 'azure devops', url: 'https://dev.azure.com/org/proj/_git/repo/pullrequest/311', want: '#311' },
    { scm: 'javascript scheme', url: 'javascript:alert(document.domain)//x/pull/1', want: null },
    { scm: 'not a PR', url: 'https://github.com/org/repo/commit/abc', want: null },
  ])('$scm', ({ url, want }) => {
    expect(prNumberFromURL(url)).toBe(want)
  })
})
