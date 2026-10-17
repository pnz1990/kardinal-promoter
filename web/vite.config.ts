// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// vite.config.ts — build (served by the controller under [<prefix>]/ui/) and Vitest config.
// defineConfig comes from vitest/config so the `test` block type-checks.

import { defineConfig } from 'vitest/config'
import react from '@vitejs/plugin-react'

// https://vitejs.dev/config/
export default defineConfig({
  plugins: [react()],
  // Relative asset URLs: the controller serves the UI at /ui/ and, with
  // --ui-base-path, at <prefix>/ui/ too, from the same build.
  base: './',
  build: {
    outDir: 'dist',
    emptyOutDir: true,
  },
  test: {
    globals: true,
    environment: 'jsdom',
    setupFiles: ['./src/test-setup.ts'],
    // Exclude Playwright E2E specs from vitest (#534)
    exclude: ['test/e2e/**', 'node_modules/**'],
  },
})
