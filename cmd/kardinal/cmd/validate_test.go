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

package cmd

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const validPipelineDoc = `apiVersion: kardinal.io/v1alpha1
kind: Pipeline
metadata:
  name: web
spec:
  git:
    url: https://github.com/o/r
  environments:
  - name: test
  - name: prod
`

// secretRefDoc is a Pipeline with metadata lines meta and a git Secret in
// namespace secretNS.
func secretRefDoc(meta, secretNS string) string {
	return "apiVersion: kardinal.io/v1alpha1\nkind: Pipeline\nmetadata:\n  name: web\n" + meta +
		"spec:\n  git:\n    url: https://github.com/o/r\n    secretRef:\n      name: github-token\n" +
		"      namespace: " + secretNS + "\n  environments:\n  - name: test\n"
}

// C09b-cli-15 / C09b-cli-16: validate rejects what the controller or the API
// server rejects: an empty git.url, a CEL expression the PolicyGate
// environment does not compile, and every document in the file is checked.
func TestValidate_Documents(t *testing.T) {
	cases := []struct {
		name    string
		content string
		wantOut []string
		wantErr bool
	}{
		{name: "valid pipeline", content: validPipelineDoc, wantOut: []string{"✓ f.yaml is valid"}},
		{
			name: "init defaults: empty git url",
			content: "apiVersion: kardinal.io/v1alpha1\nkind: Pipeline\nmetadata:\n  name: web\n" +
				"spec:\n  git:\n    url: \n  environments:\n  - name: test\n",
			wantOut: []string{"✗ f.yaml is invalid:", "  - spec.git.url is required"},
			wantErr: true,
		},
		{
			name: "second document is checked",
			content: validPipelineDoc + "---\napiVersion: kardinal.io/v1alpha1\nkind: PolicyGate\n" +
				"metadata:\n  name: g\nspec:\n  expression: schedule.hour >>> 9\n",
			wantOut: []string{"✓ f.yaml is valid", "spec.expression CEL error"},
			wantErr: true,
		},
		// E2E-R01: the quickstart and demo gate files start with a Namespace.
		{
			name: "non-kardinal kinds are skipped",
			content: "apiVersion: v1\nkind: Namespace\nmetadata:\n  name: platform-policies\n---\n" +
				"apiVersion: kardinal.io/v1alpha1\nkind: PolicyGate\nmetadata:\n  name: g\n" +
				"  namespace: platform-policies\nspec:\n  expression: \"!schedule.isWeekend\"\n",
			wantOut: []string{"- skipped Namespace/platform-policies", "✓ f.yaml is valid"},
		},
		// #1323: spec.when has no effect; setting it warns but is valid.
		{
			name: "PolicyGate with the deprecated when field warns",
			content: "apiVersion: kardinal.io/v1alpha1\nkind: PolicyGate\nmetadata:\n  name: g\n" +
				"spec:\n  expression: \"!schedule.isWeekend\"\n  when: pre-deploy\n",
			wantOut: []string{"✓ f.yaml is valid", "  ! warning: spec.when is deprecated and has no effect"},
		},
		{
			name:    "a Pipeline of another API group is skipped",
			content: "apiVersion: tekton.dev/v1\nkind: Pipeline\nmetadata:\n  name: build\nspec: {}\n---\n" + validPipelineDoc,
			wantOut: []string{"- skipped Pipeline/build", "✓ f.yaml is valid"},
		},
		{
			name:    "a file with nothing to check fails",
			content: "apiVersion: v1\nkind: Namespace\nmetadata:\n  name: platform-policies\n",
			wantOut: []string{"- skipped Namespace/platform-policies"},
			wantErr: true,
		},
		// E2E-R14: the unimplemented fields that fail Bundles are reported,
		// like the Pipeline's Ready=False/NotImplemented condition.
		{
			name:    "autoRollback",
			content: validPipelineDoc + "    autoRollback:\n      failureThreshold: 2\n",
			wantOut: []string{"✗ f.yaml is invalid:", `environment "prod": environments[].autoRollback is not implemented`},
			wantErr: true,
		},
		{
			name:    "two regions",
			content: validPipelineDoc + "    regions: [us-east-1, eu-west-1]\n",
			wantOut: []string{"✗ f.yaml is invalid:", `environment "prod": regions is not supported; declare one environment per region`},
			wantErr: true,
		},
		{
			name:    "one region is accepted and ignored",
			content: validPipelineDoc + "    regions: [us-east-1]\n",
			wantOut: []string{"✓ f.yaml is valid"},
		},
		// #1321: distributed mode was removed.
		{
			name:    "shard",
			content: validPipelineDoc + "    shard: eu\n",
			wantOut: []string{"✗ f.yaml is invalid:", `environment "prod": shard is not supported: distributed mode was removed`},
			wantErr: true,
		},
		{
			name: "layout branch",
			content: "apiVersion: kardinal.io/v1alpha1\nkind: Pipeline\nmetadata:\n  name: web\n" +
				"spec:\n  git:\n    url: https://github.com/o/r\n    layout: branch\n  environments:\n  - name: test\n",
			wantOut: []string{"✗ f.yaml is invalid:", "spec.git.layout: branch is not implemented"},
			wantErr: true,
		},
		{
			name:    "health.resource.kind other than Deployment",
			content: validPipelineDoc + "    health:\n      resource:\n        kind: StatefulSet\n",
			wantOut: []string{"✗ f.yaml is invalid:", `environment "prod": health.resource.kind "StatefulSet" is not supported`},
			wantErr: true,
		},
		// C03-promotionstep-18: a Secret in another namespace is refused (not
		// "not implemented"); a file without metadata.namespace is not judged,
		// since the namespace it lands in is not known offline.
		{
			name:    "git.secretRef in another namespace",
			content: secretRefDoc("  namespace: team-a\n", "kardinal-system"),
			wantOut: []string{"✗ f.yaml is invalid:",
				`git.secretRef.namespace "kardinal-system" is not allowed: the Secret must be in the Pipeline's namespace "team-a"`},
			wantErr: true,
		},
		{
			name:    "git.secretRef in the Pipeline's namespace",
			content: secretRefDoc("  namespace: team-a\n", "team-a"),
			wantOut: []string{"✓ f.yaml is valid"},
		},
		{
			name:    "git.secretRef namespace without metadata.namespace",
			content: secretRefDoc("", "team-a"),
			wantOut: []string{"✓ f.yaml is valid"},
		},
		{
			name:    "steps are reported once",
			content: validPipelineDoc + "    steps:\n    - uses: git-clone\n",
			wantOut: []string{"✗ f.yaml is invalid:\n  - environment \"prod\" declares 1 steps; spec.environments[].steps is not implemented"},
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			require.NoError(t, os.WriteFile("f.yaml", []byte(tc.content), 0o600))
			out, err := executeRoot(t, "validate", "-f", "f.yaml")
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			for _, s := range tc.wantOut {
				assert.Contains(t, out, s)
			}
			assert.NotContains(t, out, "build: environment", "a problem is reported once")
		})
	}
}
