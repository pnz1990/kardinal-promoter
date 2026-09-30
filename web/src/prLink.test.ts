// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

import { describe, it, expect } from 'vitest'
import { isCIRunURL, isHttpURL, prNumberFromURL } from './prLink'

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

// The same cases as TestValidateCIRunURL in pkg/graph/validate_test.go.
describe('isCIRunURL', () => {
  it.each([
    { name: 'github actions run', url: 'https://github.com/o/r/actions/runs/1', want: true },
    { name: 'http with port', url: 'http://ci.example.com:8080/job/1', want: true },
    { name: 'upper-case scheme', url: 'HTTPS://ci.example.com/1', want: true },
    { name: 'parentheses', url: 'https://ci.example.com/run(1)', want: true },
    { name: 'empty', url: '', want: false },
    { name: 'undefined', url: undefined, want: false },
    { name: 'javascript scheme', url: 'javascript:alert(1)', want: false },
    { name: 'data scheme', url: 'data:text/html,<script>alert(1)</script>', want: false },
    { name: 'ftp scheme', url: 'ftp://ci.example.com/1', want: false },
    { name: 'relative path', url: '/o/r/actions/runs/1', want: false },
    { name: 'no slashes', url: 'https:ci.example.com/1', want: false },
    { name: 'no host', url: 'https:///runs/1', want: false },
    { name: 'scheme-relative', url: '//ci.example.com/1', want: false },
    { name: 'user info', url: 'https://u:s3cret@ci.example.com/1', want: false },
    { name: 'host disguised as user', url: 'https://github.com@evil.example/1', want: false },
    { name: 'space', url: 'https://ci.example.com/1) [x](y)', want: false },
    { name: 'leading space', url: ' https://ci.example.com/1', want: false },
    { name: 'newline', url: 'https://ci.example.com/1\nx', want: false },
    { name: 'tab', url: 'https://ci.example.com/\t1', want: false },
    { name: 'NUL', url: 'https://ci.example.com/1\u0000', want: false },
    { name: 'no-break space', url: 'https://ci.example.com/1\u00a0x', want: false },
  ])('$name', ({ url, want }) => {
    expect(isCIRunURL(url)).toBe(want)
  })
})
