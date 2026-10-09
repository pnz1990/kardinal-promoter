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
// Journeys (test/e2e/journeys/):
//   001 — pipeline-list:   List renders, click selects pipeline, logo loads from /ui/
//   002 — dag-node-click:  Click DAG node → NodeDetail panel opens
//   003 — health-chip:     State colors come from CSS classes, not inline styles (#532)
//   004 — policy-gates:    Blocked gate auto-expands; only the shown bundle's gates count (#524)
//   005 — bundle-timeline: Click bundle chip → DAG switches, and stays after a poll
//   006 — pause-resume:    Pause/Resume confirm, send the pipeline, and flip the button
//   007 — empty-state:     No pipelines → onboarding card with the kubectl command
//   008 — loading-state:   Spinner clears after first successful fetch (#522 guard)
//   009 — accessibility:   WCAG 2.1 AA axe-core scan (#748)
//   010 — responsive:      No horizontal overflow at 1280×800 (#799)
//   011 — rollback-button: Rollback asks first, then calls the API
//   012 — fleet-board:     Stations show what each environment runs; a station opens its pipeline
//   013 — keyboard-types:  Skip link, arrow keys on the fleet and the history, type badges, axe in light
//   015 — approvals:       Approval gate quorum and decisions, approve/reject from the panel, rejected Bundles

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

  // Start the mock API server, which also serves the built UI from web/dist
  // (run `npm run build` first).
  webServer: {
    command: `KARDINAL_E2E_PORT=${PORT} node test/e2e/mock-server/server.mjs`,
    port: PORT,
    reuseExistingServer: !process.env.CI,
    timeout: 30_000,
  },
})
