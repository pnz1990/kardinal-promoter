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
// playwright.live.config.ts — the browser half of the live e2e ui suite.
//
// The specs in test/e2e/live/ drive the real UI of a kind cluster's
// controller. Each one runs from its Go test in test/e2e/live/ui_browser_test.go
// (framework.Playwright), which sets the cluster up, passes what the spec needs
// in KARDINAL_UI_* variables, reads the JSON report and checks the cluster
// afterwards. Run them with `go test` (hack/e2e/run.sh ui), not directly: a
// spec throws when its variables are missing, so it fails instead of skipping.

import { defineConfig, devices } from '@playwright/test'

export default defineConfig({
  testDir: './test/e2e/live',

  // One spec per `npx playwright test` call; its tests share the cluster
  // state the Go test made, so they run in order and are never retried.
  workers: 1,
  fullyParallel: false,
  retries: 0,
  forbidOnly: true,

  // A promotion on kind takes a minute or two.
  timeout: 5 * 60_000,
  expect: { timeout: 30_000 },

  use: {
    ...devices['Desktop Chrome'],
    // The UI API allows this host name (ui.allowedHosts); it resolves to the
    // kubectl port-forward on 127.0.0.1 so the controller sees a loopback peer.
    launchOptions: { args: ['--host-resolver-rules=MAP kardinal-ui.test 127.0.0.1'] },
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
  },
})
