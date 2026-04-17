// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//
// playwright.config.ts — E2E configuration for kardinal UI.
//
// The test suite runs against a mock API server (web/test/e2e/mock-server/)
// that serves deterministic fixture data. No real Kubernetes cluster is required.
//
// Journey structure (8 baseline journeys):
//   001 — pipeline-list:   List renders, click selects pipeline
//   002 — dag-node-click:  Click DAG node → NodeDetail panel opens
//   003 — health-chip:     CSS class assertions for all 7 states (regression guard #532)
//   004 — policy-gates:    Blocked gate auto-expands (#524 regression guard)
//   005 — bundle-timeline: Click bundle chip → DAG switches
//   006 — pause-resume:    ActionBar pause/resume state changes
//   007 — empty-state:     No pipelines → onboarding card visible
//   008 — loading-state:   Spinner clears after first successful fetch (#522 guard)
//   009 — accessibility:   WCAG 2.1 AA axe-core scan (#748)

import { defineConfig, devices } from '@playwright/test'

const PORT = parseInt(process.env.KARDINAL_E2E_PORT ?? '3001', 10)
// Use origin as baseURL so page.goto('/') hits the root redirect → /ui/.
// Tests use page.goto('/') which the mock server redirects to /ui/ automatically.
const BASE_URL = `http://localhost:${PORT}`

export default defineConfig({
  testDir: './test/e2e/journeys',

  // Retry once on CI to absorb startup latency
  retries: process.env.CI ? 1 : 0,

  // Run all journeys in parallel (all are read-only against the mock server)
  workers: process.env.CI ? 2 : 4,
  fullyParallel: true,

  reporter: [
    ['list'],
    ['html', { open: 'never', outputFolder: 'test/e2e/playwright-report' }],
  ],

  // Per-test timeout: 30s (mock server is fast)
  timeout: 30_000,

  // Expect timeout: 8s (DOM assertions)
  expect: { timeout: 8_000 },

  use: {
    baseURL: BASE_URL,
    ...devices['Desktop Chrome'],
    trace: 'on-first-retry',
    screenshot: 'only-on-failure',
  },

  // Start the Vite dev server (with mock API) before tests
  webServer: {
    command: `KARDINAL_E2E_PORT=${PORT} node test/e2e/mock-server/server.mjs`,
    port: PORT,
    reuseExistingServer: !process.env.CI,
    timeout: 30_000,
  },
})
