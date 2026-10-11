// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// versionName.ts — what a person calls a version (a Bundle, in the glossary of
// UX-AUDIT.md §4.3). A Bundle named after its pipeline ("checkout-abc123") reads
// as "abc123" on that pipeline's pages; any other name reads as itself. The full
// name stays one click away (copy buttons, Kubernetes details).

/** The short name of a version: the Bundle name without its "<pipeline>-" prefix. */
export function versionName(bundleName: string, pipeline?: string): string {
  if (pipeline && bundleName.startsWith(`${pipeline}-`) && bundleName.length > pipeline.length + 1) {
    return bundleName.slice(pipeline.length + 1)
  }
  return bundleName
}
