// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
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
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/yaml"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

func buildCreateBundleScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	_ = v1alpha1.AddToScheme(s)
	return s
}

// TestCreateBundle_DryRunFlagRegistered verifies --dry-run is registered.
func TestCreateBundle_DryRunFlagRegistered(t *testing.T) {
	cmd := newCreateBundleCmd()
	f := cmd.Flags().Lookup("dry-run")
	require.NotNil(t, f, "--dry-run must be registered on 'create bundle'")
	assert.Equal(t, "false", f.DefValue, "--dry-run must default to false")
}

// TestCreateBundle_DryRun_NoAPICallsMade verifies that --dry-run does NOT call
// c.Create (i.e., no Bundle is created on the cluster).
func TestCreateBundle_DryRun_NoAPICallsMade(t *testing.T) {
	pipe := &v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "nginx-demo",
			Namespace: "default",
		},
		Spec: v1alpha1.PipelineSpec{
			Environments: []v1alpha1.EnvironmentSpec{
				{Name: "test"},
				{Name: "uat"},
				{Name: "prod"},
			},
		},
	}

	s := buildCreateBundleScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(pipe).Build()

	var buf bytes.Buffer
	err := createBundleDryRun(&buf, c, "default", "nginx-demo",
		createBundleOptions{Images: []string{"ghcr.io/org/app:sha-abc123"}, Type: "image"})
	require.NoError(t, err)

	// No Bundle should exist after dry-run
	var bundleList v1alpha1.BundleList
	require.NoError(t, c.List(context.Background(), &bundleList))
	assert.Empty(t, bundleList.Items, "dry-run must not create any Bundles")
}

// TestCreateBundle_DryRun_OutputContainsKeyInfo verifies the dry-run output mentions
// the pipeline name and dry-run indicator.
func TestCreateBundle_DryRun_OutputContainsKeyInfo(t *testing.T) {
	pipe := &v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "nginx-demo",
			Namespace: "default",
		},
		Spec: v1alpha1.PipelineSpec{
			Environments: []v1alpha1.EnvironmentSpec{
				{Name: "test"},
				{Name: "uat"},
				{Name: "prod"},
			},
		},
	}

	s := buildCreateBundleScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(pipe).Build()

	var buf bytes.Buffer
	err := createBundleDryRun(&buf, c, "default", "nginx-demo",
		createBundleOptions{Images: []string{"ghcr.io/org/app:sha-abc123"}, Type: "image"})
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "[DRY-RUN]", "output must contain DRY-RUN marker")
	assert.Contains(t, out, "nginx-demo", "output must contain pipeline name")
	assert.Contains(t, out, "No resources were created", "output must confirm nothing was written")
}

// TestCreateBundle_DryRun_PipelineNotFound returns an error when the Pipeline does not exist.
func TestCreateBundle_DryRun_PipelineNotFound(t *testing.T) {
	s := buildCreateBundleScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).Build()

	var buf bytes.Buffer
	err := createBundleDryRun(&buf, c, "default", "nonexistent",
		createBundleOptions{Images: []string{"ghcr.io/org/app:sha-abc123"}, Type: "image"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "dry-run", "error must mention dry-run context")
}

// TestCreateBundle_DryRun_BuildError shows the Graph builder's error with the
// dry-run context once, not "dry-run: graph build failed: build: ..." (B47).
func TestCreateBundle_DryRun_BuildError(t *testing.T) {
	pipe := &v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo", Namespace: "default"},
		Spec: v1alpha1.PipelineSpec{Environments: []v1alpha1.EnvironmentSpec{
			{Name: "prod", DependsOn: []string{"staging"}},
		}},
	}
	c := fake.NewClientBuilder().WithScheme(buildCreateBundleScheme(t)).WithObjects(pipe).Build()

	var buf bytes.Buffer
	err := createBundleDryRun(&buf, c, "default", "nginx-demo",
		createBundleOptions{Images: []string{"ghcr.io/org/app:sha-abc123"}, Type: "image"})
	require.Error(t, err)
	assert.Equal(t, `dry-run: build: environment "prod" dependsOn unknown environment "staging"`, err.Error())
}

// TestCreateBundle_DryRun_InvalidImage returns an error for malformed image refs.
func TestCreateBundle_DryRun_InvalidImage(t *testing.T) {
	s := buildCreateBundleScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).Build()

	var buf bytes.Buffer
	err := createBundleDryRun(&buf, c, "default", "nginx-demo",
		createBundleOptions{Images: []string{"not valid @@@"}, Type: "image"})
	require.Error(t, err)
	assert.Equal(t, `invalid image repository "not valid @@": want [host[:port]/]path (e.g. ghcr.io/org/image)`,
		err.Error())
}

// TestCreateBundle_DryRun_EnvironmentsInPromotionOrder: the preview lists the
// environments in the order they are promoted (dependsOn), not the order they
// are declared, so a Pipeline that declares prod before uat still shows
// test, uat, prod.
func TestCreateBundle_DryRun_EnvironmentsInPromotionOrder(t *testing.T) {
	tests := []struct {
		name string
		envs []v1alpha1.EnvironmentSpec
		want string
	}{
		{name: "declared in promotion order",
			envs: []v1alpha1.EnvironmentSpec{
				{Name: "test"}, {Name: "uat", DependsOn: []string{"test"}}, {Name: "prod", DependsOn: []string{"uat"}},
			},
			want: "  • test\n  • uat\n  • prod\n"},
		{name: "dependsOn declared out of order",
			envs: []v1alpha1.EnvironmentSpec{
				{Name: "test"}, {Name: "prod", DependsOn: []string{"uat"}}, {Name: "uat", DependsOn: []string{"test"}},
			},
			want: "  • test\n  • uat\n  • prod\n"},
		{name: "waves declared out of order",
			envs: []v1alpha1.EnvironmentSpec{{Name: "prod", Wave: 2}, {Name: "test", Wave: 1}},
			want: "  • test\n  • prod\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pipe := &v1alpha1.Pipeline{
				ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "default"},
				Spec:       v1alpha1.PipelineSpec{Environments: tc.envs},
			}
			c := fake.NewClientBuilder().WithScheme(buildCreateBundleScheme(t)).WithObjects(pipe).Build()
			var buf bytes.Buffer
			require.NoError(t, createBundleDryRun(&buf, c, "default", "demo",
				createBundleOptions{Images: []string{"ghcr.io/org/app:sha-abc1234"}, Type: "image"}))
			assert.Contains(t, buf.String(), "Environments in promotion order:\n"+tc.want, buf.String())
		})
	}
}

// TestCreateBundle_DryRun_DemoPipelinesListEveryEnvironment: against the demo
// Pipelines the preview lists every environment. Before the list was built
// from the Pipeline, it parsed hyphenated node IDs and came out empty for
// every upstream-kro Graph.
func TestCreateBundle_DryRun_DemoPipelinesListEveryEnvironment(t *testing.T) {
	files, err := filepath.Glob("../../../demo/manifests/*/pipeline.yaml")
	require.NoError(t, err)
	require.NotEmpty(t, files, "demo pipelines not found")
	for _, f := range files {
		t.Run(filepath.Base(filepath.Dir(f)), func(t *testing.T) {
			raw, err := os.ReadFile(f)
			require.NoError(t, err)
			var pipe v1alpha1.Pipeline
			require.NoError(t, yaml.Unmarshal(raw, &pipe))
			require.NotEmpty(t, pipe.Spec.Environments)
			if pipe.Namespace == "" {
				pipe.Namespace = "default"
			}
			c := fake.NewClientBuilder().WithScheme(buildCreateBundleScheme(t)).WithObjects(&pipe).Build()
			var buf bytes.Buffer
			require.NoError(t, createBundleDryRun(&buf, c, pipe.Namespace, pipe.Name,
				createBundleOptions{Images: []string{"ghcr.io/pnz1990/kardinal-test-app:sha-abc1234"}, Type: "image"}))
			_, list, found := strings.Cut(buf.String(), "Environments in promotion order:\n")
			require.True(t, found, buf.String())
			list, _, _ = strings.Cut(list, "\n\n")
			lines := strings.Split(list, "\n")
			require.Len(t, lines, len(pipe.Spec.Environments), buf.String())
			for _, env := range pipe.Spec.Environments {
				assert.Contains(t, buf.String(), "  • "+env.Name, buf.String())
			}
		})
	}
}

// TestCreateBundle_SharesBundleAPIRules is #1285: kardinal create bundle
// applies the Bundle API's checks (lifecycle.ValidateNewBundle), checks that
// the Pipeline exists, and sets configRef and provenance from the new flags.
// Before, it created a Bundle for a missing Pipeline, an image Bundle with no
// image, and could not create a config Bundle or set provenance.
func TestCreateBundle_SharesBundleAPIRules(t *testing.T) {
	const img = "ghcr.io/org/app:v1"
	tests := []struct {
		name     string
		pipeline string
		opts     createBundleOptions
		wantErr  string
		check    func(t *testing.T, b v1alpha1.Bundle)
	}{
		{name: "unknown pipeline", pipeline: "ghost",
			opts:    createBundleOptions{Images: []string{img}, Type: "image"},
			wantErr: `pipeline "ghost" not found in namespace "default"`},
		{name: "image bundle without an image",
			opts:    createBundleOptions{Type: "image"},
			wantErr: `create bundle: type "image" requires at least one --image`},
		{name: "config bundle without a commit",
			opts:    createBundleOptions{Type: "config", ConfigRepo: "https://github.com/org/config"},
			wantErr: `create bundle: type "config" requires --config-commit`},
		{name: "mixed bundle without a commit",
			opts:    createBundleOptions{Images: []string{img}, Type: "mixed"},
			wantErr: `create bundle: type "mixed" requires --config-commit`},
		{name: "image bundle with a config repo (#1353)",
			opts:    createBundleOptions{Images: []string{img}, Type: "image", ConfigRepo: "https://github.com/org/config"},
			wantErr: "create bundle: --config-repo needs --type config or mixed and --config-commit"},
		{name: "image bundle with a config commit (#1353)",
			opts:    createBundleOptions{Images: []string{img}, ConfigCommit: "9f8e7d6"},
			wantErr: "create bundle: --config-commit needs --type config or mixed"},
		{name: "unknown type",
			opts:    createBundleOptions{Images: []string{img}, Type: "helm"},
			wantErr: `create bundle: type must be one of image, config, mixed, chart (got "helm")`},
		{name: "ci run url with another scheme",
			opts:    createBundleOptions{Images: []string{img}, Type: "image", CIRunURL: "javascript:alert(1)"},
			wantErr: "create bundle: --ci-run-url must be an absolute http or https URL"},
		{name: "config bundle",
			opts: createBundleOptions{Type: "config", ConfigRepo: "https://github.com/org/config", ConfigCommit: "9f8e7d6"},
			check: func(t *testing.T, b v1alpha1.Bundle) {
				assert.Equal(t, "config", b.Spec.Type)
				assert.Empty(t, b.Spec.Images)
				assert.Equal(t, &v1alpha1.ConfigRef{GitRepo: "https://github.com/org/config", CommitSHA: "9f8e7d6"}, b.Spec.ConfigRef)
				assert.Nil(t, b.Spec.Provenance, "no provenance flag, no provenance")
			}},
		{name: "image bundle with provenance",
			opts: createBundleOptions{Images: []string{img}, Type: "image",
				Commit: "abc1234", Author: "ci-bot", CIRunURL: "https://github.com/org/app/actions/runs/1"},
			check: func(t *testing.T, b v1alpha1.Bundle) {
				assert.Equal(t, []v1alpha1.ImageRef{{Repository: "ghcr.io/org/app", Tag: "v1"}}, b.Spec.Images)
				assert.Nil(t, b.Spec.ConfigRef)
				require.NotNil(t, b.Spec.Provenance)
				assert.Equal(t, "abc1234", b.Spec.Provenance.CommitSHA)
				assert.Equal(t, "ci-bot", b.Spec.Provenance.Author)
				assert.Equal(t, "https://github.com/org/app/actions/runs/1", b.Spec.Provenance.CIRunURL)
			}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pipeline := tc.pipeline
			if pipeline == "" {
				pipeline = "demo"
			}
			c := fake.NewClientBuilder().WithScheme(buildCreateBundleScheme(t)).
				WithObjects(policyPipeline("demo", "test", "prod")).Build()
			var buf bytes.Buffer
			err := createBundleFn(&buf, c, "default", pipeline, tc.opts)
			var list v1alpha1.BundleList
			require.NoError(t, c.List(context.Background(), &list))
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Equal(t, tc.wantErr, err.Error())
				assert.Empty(t, list.Items, "nothing is created when a check fails")

				// The dry-run applies the same checks.
				buf.Reset()
				require.Error(t, createBundleDryRun(&buf, c, "default", pipeline, tc.opts))
				return
			}
			require.NoError(t, err)
			require.Len(t, list.Items, 1)
			tc.check(t, list.Items[0])
		})
	}
}

// TestCreateBundle_ConfigAndProvenanceFlagsRegistered pins the #1285 flags.
func TestCreateBundle_ConfigAndProvenanceFlagsRegistered(t *testing.T) {
	cmd := newCreateBundleCmd()
	for _, name := range []string{"config-repo", "config-commit", "commit", "author", "ci-run-url"} {
		assert.NotNil(t, cmd.Flags().Lookup(name), "--%s", name)
	}
}

// TestCreateBundle_CreatesBundle verifies that createBundleFn creates a Bundle CRD.
func TestCreateBundle_CreatesBundle(t *testing.T) {
	s := cliTestScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(policyPipeline("nginx-demo", "test")).Build()

	var buf bytes.Buffer
	err := createBundleFn(&buf, c, "default", "nginx-demo", createBundleOptions{Images: []string{"nginx:1.25"}, Type: "image"})
	require.NoError(t, err)

	var bundles v1alpha1.BundleList
	require.NoError(t, c.List(context.Background(), &bundles))
	require.Len(t, bundles.Items, 1)
	assert.Equal(t, "nginx-demo", bundles.Items[0].Spec.Pipeline)
	assert.Equal(t, "image", bundles.Items[0].Spec.Type)
	assert.Equal(t, "nginx", bundles.Items[0].Spec.Images[0].Repository)
	assert.Equal(t, "1.25", bundles.Items[0].Spec.Images[0].Tag)

	assert.Contains(t, buf.String(), "Bundle")
	assert.Contains(t, buf.String(), "nginx-demo")
}

// TestCreateBundle_RejectsMalformedImage verifies that image references with
// invalid characters are rejected before creating a Bundle (#283).
func TestCreateBundle_RejectsMalformedImage(t *testing.T) {
	s := cliTestScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(policyPipeline("nginx-demo", "test")).Build()

	tests := []struct {
		image   string
		wantErr bool
	}{
		{"ghcr.io/pnz1990/app:sha-abc1234", false},
		{"nginx:1.29", false},
		{"nginx", false},
		{"!!! bad", true},
		{"space bad", true},
	}
	for _, tc := range tests {
		var buf bytes.Buffer
		err := createBundleFn(&buf, c, "default", "nginx-demo", createBundleOptions{Images: []string{tc.image}, Type: "image"})
		if tc.wantErr {
			require.Error(t, err, "image %q must be rejected", tc.image)
		} else {
			require.NoError(t, err, "image %q must be accepted", tc.image)
		}
	}
}

// TestSplitImageRef verifies image reference parsing. A digest is returned as
// the digest, never as the tag (C09a-cli-02).
func TestSplitImageRef(t *testing.T) {
	tests := []struct {
		img    string
		repo   string
		tag    string
		digest string
	}{
		{"nginx:1.25", "nginx", "1.25", ""},
		{"ghcr.io/myorg/app:v2.0.0", "ghcr.io/myorg/app", "v2.0.0", ""},
		{"nginx", "nginx", "", ""},
		{"nginx@sha256:abc123", "nginx", "", "sha256:abc123"},
		{"ghcr.io/myorg/app:v2@sha256:abc123", "ghcr.io/myorg/app", "v2", "sha256:abc123"},
		{"registry:5000/app", "registry:5000/app", "", ""},
		{"registry:5000/app:v1", "registry:5000/app", "v1", ""},
	}
	for _, tt := range tests {
		t.Run(tt.img, func(t *testing.T) {
			repo, tag, digest := splitImageRef(tt.img)
			assert.Equal(t, tt.repo, repo)
			assert.Equal(t, tt.tag, tag)
			assert.Equal(t, tt.digest, digest)
		})
	}
}

// TestImageRepoPattern (C09a-cli-04): repositories with a registry port are
// valid. (repo:tag@digest also needs the C09a-cli-02 split, lifecycle area.)
func TestImageRepoPattern(t *testing.T) {
	cases := []struct {
		repo string
		want bool
	}{
		{"nginx", true},
		{"docker.io/library/nginx", true},
		{"ghcr.io/pnz1990/kardinal-test-app", true},
		{"localhost:5000/kardinal-test-app", true},
		{"registry.internal:8443/org/app", true},
		{"my-registry.example.com/team__a/app.v2", true},
		{"not valid @@@", false},
		{"ghcr.io/org/app:v1", false},
		{"ghcr.io/Org/App", false},
		{"-bad", false},
		{"", false},
	}
	for _, tc := range cases {
		t.Run(tc.repo, func(t *testing.T) {
			assert.Equal(t, tc.want, imageRepoPattern.MatchString(tc.repo))
		})
	}

	c := fake.NewClientBuilder().WithScheme(cliTestScheme(t)).WithObjects(policyPipeline("demo", "test")).Build()
	var buf bytes.Buffer
	require.NoError(t, createBundleFn(&buf, c, "default", "demo", createBundleOptions{Images: []string{"localhost:5000/kardinal-test-app:sha-abc1234"}, Type: "image"}))
	var bundles v1alpha1.BundleList
	require.NoError(t, c.List(context.Background(), &bundles))
	require.Len(t, bundles.Items, 1)
	assert.Equal(t, v1alpha1.ImageRef{Repository: "localhost:5000/kardinal-test-app", Tag: "sha-abc1234"}, bundles.Items[0].Spec.Images[0])
}

// TestCreateBundle_DryRun_ListsEnvironmentsAndGates (C09a-cli-05): the preview
// lists the Pipeline's environments in order with the gates the controller
// would attach, including org gates from platform-policies.
func TestCreateBundle_DryRun_ListsEnvironmentsAndGates(t *testing.T) {
	c := policyClient(t,
		policyPipeline("demo", "test", "uat", "prod"),
		policyGate("no-weekend-deploys", "platform-policies", "prod", "!schedule.isWeekend", "kardinal.io/scope", "org"),
		policyGate("team-soak", "default", "uat", "upstream.test.soakMinutes >= 5"),
	)
	var buf bytes.Buffer
	require.NoError(t, createBundleDryRun(&buf, c, "default", "demo", createBundleOptions{Images: []string{"ghcr.io/org/app:sha-abc1234"}, Type: "image"}))
	assert.Contains(t, buf.String(), "Environments in promotion order:\n"+
		"  • test\n"+
		"  • uat (gates: team-soak)\n"+
		"  • prod (gates: no-weekend-deploys)\n", buf.String())
}

// TestCreateBundle_DryRun_CompactShape: the dry run uses the controller's
// compact threshold given with --graph-compact-above (default 100) and says
// when the Graph would be compact.
func TestCreateBundle_DryRun_CompactShape(t *testing.T) {
	c := policyClient(t, policyPipeline("demo", "test", "uat", "prod"))
	for _, tc := range []struct {
		above *int
		want  string
	}{
		{want: "Promotion graph: 6 node(s)\n"},
		{above: func() *int { v := 0; return &v }(), want: "Promotion graph: 9 node(s), compact shape\n"},
	} {
		var buf bytes.Buffer
		require.NoError(t, createBundleDryRun(&buf, c, "default", "demo",
			createBundleOptions{Images: []string{"ghcr.io/org/app:sha-abc1234"}, Type: "image", CompactAbove: tc.above}))
		assert.Contains(t, buf.String(), tc.want)
	}
}
