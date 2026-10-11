// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

/** Joins class names, dropping empty ones. */
export function cx(...names: (string | false | null | undefined)[]): string {
  return names.filter(Boolean).join(' ')
}
