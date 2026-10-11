// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// errorMessage.ts — what a failed read means, in words a person can act on (U4). The API
// client's messages ("API error 503: Service Unavailable", "Failed to fetch") stay as the
// detail under it.

/** A failed request: what happened, in plain words, and the client's own message. */
export interface HumanError {
  /** What happened and what to do, e.g. "Can't reach the kardinal controller." */
  message: string
  /** The client's message, unchanged. */
  detail: string
}

/** The HTTP status of an "API error <status>: …" message, if it is one. */
function statusOf(raw: string): number | undefined {
  const m = /^API error (\d{3})\b/.exec(raw)
  return m ? Number(m[1]) : undefined
}

export function humanError(raw: string): HumanError {
  const status = statusOf(raw)
  const say = (message: string) => ({ message, detail: raw })
  if (status === 401) return say('Your token was not accepted. Enter a token that can read this namespace.')
  if (status === 403) return say("Your account can't read this. Ask for the kardinal viewer role in this namespace.")
  if (status === 404) return say("The controller doesn't have this. It may have been deleted.")
  if (status === 409) return say('It changed while you were working. Refresh and try again.')
  if (status === 429) return say('The controller is busy. Try again in a moment.')
  if (status !== undefined && status >= 500) return say('The controller had a problem. It may be restarting; try again.')
  if (/got no answer within/.test(raw)) return say("The controller didn't answer in time.")
  if (/Failed to fetch|NetworkError|Load failed|ECONNREFUSED|connection refused/i.test(raw)) {
    return say("Can't reach the kardinal controller. Check that it is running and reachable from this browser.")
  }
  return say('Something went wrong.')
}
