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

// components/EmptyState.tsx — Improved empty state for the pipeline list.
// Replaces the plain div with an actionable onboarding card (#530).
// NNG Heuristic 10: in-context help must be immediately actionable.
import CopyButton from './CopyButton'
import '../styles/EmptyState.css'

const QUICKSTART_COMMAND = 'kubectl apply -f https://raw.githubusercontent.com/pnz1990/kardinal-promoter/main/examples/quickstart/pipeline.yaml'
const DOCS_URL = 'https://pnz1990.github.io/kardinal-promoter/'

/** Expected output shown after applying the quickstart pipeline. */
// Matches the Pipeline printer columns (Phase, Paused, Age) in api/v1alpha1/pipeline_types.go.
// A pipeline with no bundle yet has phase Unknown (DerivePhase in pkg/reconciler/pipeline).
export const EXPECTED_OUTPUT = `NAME                PHASE     PAUSED   AGE
kardinal-test-app   Unknown   false    5s`

/**
 * EmptyState — shown when no pipelines are found.
 * Provides actionable onboarding: explanation, kubectl command with copy button,
 * docs link, and expected output preview.
 * #530
 */
export default function EmptyState() {
  return (
    <div data-testid="empty-state" className="empty-state">
      {/* Title */}
      <div className="empty-state__title">
        No pipelines found
      </div>

      {/* One-sentence explanation */}
      <p className="empty-state__lead">
        A <strong className="empty-state__term">Pipeline</strong> defines your promotion environments
        (test → uat → prod) and the policy gates between them.{' '}
        <a
          href={DOCS_URL}
          target="_blank"
          rel="noopener noreferrer"
          data-testid="docs-link"
          className="empty-state__link"
        >
          Read the docs ↗
        </a>
      </p>

      {/* Quickstart command */}
      <div className="empty-state__step">
        <div className="empty-state__step-label">
          Apply the quickstart example:
        </div>
        <div className="empty-state__command">
          <code data-testid="quickstart-command" className="empty-state__code">
            {QUICKSTART_COMMAND}
          </code>
          <CopyButton text={QUICKSTART_COMMAND} title="Copy kubectl apply command" />
        </div>
      </div>

      {/* Expected output */}
      <div className="empty-state__step empty-state__step--last">
        <div className="empty-state__step-label">
          Then run <code className="empty-state__inline-code">kubectl get pipelines</code>:
        </div>
        {/* kubectl output (data-k8s: Kubernetes words, as kubectl prints them). It scrolls sideways
            on a narrow screen, so it takes focus (keyboard scrolling). */}
        <pre className="empty-state__output" data-k8s tabIndex={0} role="region" aria-label="Expected output of kubectl get pipelines">
          {EXPECTED_OUTPUT}
        </pre>
      </div>

      {/* Polling status */}
      <div data-testid="watching-indicator" className="empty-state__watching">
        <span className="empty-state__spinner" aria-hidden="true">◌</span>
        Watching for new pipelines…
      </div>
    </div>
  )
}
