// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

import { describe, it, expect } from 'vitest'
import { humanError } from './errorMessage'

describe('humanError', () => {
  it.each<[string, RegExp]>([
    ['API error 401: Unauthorized', /token was not accepted/],
    ['API error 403: Forbidden', /viewer role/],
    ['API error 404: pipeline not found', /doesn't have this/],
    ['API error 409: conflict', /changed while/],
    ['API error 429: Too Many Requests', /busy/],
    ['API error 503: Service Unavailable', /may be restarting/],
    ['GET /api/v1/ui/pipelines got no answer within 15 s. The UI tries again on the next refresh.', /didn't answer in time/],
    ['Failed to fetch', /Can't reach the kardinal controller/],
    ['connection refused', /Can't reach/],
    ['boom', /^Something went wrong\.$/],
  ])('%s reads as %s and keeps the message as the detail', (raw, want) => {
    const h = humanError(raw)
    expect(h.message).toMatch(want)
    expect(h.detail).toBe(raw)
  })
})
