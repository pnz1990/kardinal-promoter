// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

import type { Ref, RefCallback, RefObject } from 'react'

/** Joins class names, dropping empty ones. */
export function cx(...names: (string | false | null | undefined)[]): string {
  return names.filter(Boolean).join(' ')
}

/** One ref callback that sets every given ref (a forwarded ref and a local one). */
export function mergeRefs<T>(...refs: (Ref<T> | undefined)[]): RefCallback<T> {
  return (value) => {
    for (const r of refs) {
      if (typeof r === 'function') r(value)
      else if (r) (r as RefObject<T | null>).current = value
    }
  }
}
