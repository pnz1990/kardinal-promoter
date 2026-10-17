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
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// inRepo makes a git repository (a .git directory) in a temp dir, writes
// .kardinal.yaml with content at its root unless content is "", and runs the
// test from sub, a directory below the root.
func inRepo(t *testing.T, content, sub string) string {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, ".git"), 0o755))
	if content != "" {
		require.NoError(t, os.WriteFile(filepath.Join(root, ".kardinal.yaml"), []byte(content), 0o600))
	}
	dir := filepath.Join(root, sub)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	t.Chdir(dir)
	return root
}

// TestRepoDefaults_Precedence (#1759): each default is, in order, the
// KARDINAL_PIPELINE / KARDINAL_NAMESPACE environment variable, then
// .kardinal.yaml (looked for from the working directory up to the git
// repository's root), then none. An explicit argument or -n always wins
// (TestRepoDefaults_Commands).
//
// Covers CLI-DEFAULTS-01.
func TestRepoDefaults_Precedence(t *testing.T) {
	cases := []struct {
		name             string
		file, sub        string
		envPipe, envNS   string
		wantPipe, wantNS string
		wantErr          string
	}{
		{name: "none", sub: "a"},
		{name: "file at the root, from a subdirectory", file: "pipeline: web\nnamespace: team-a\n", sub: "a/b",
			wantPipe: "web", wantNS: "team-a"},
		{name: "env wins over the file", file: "pipeline: web\nnamespace: team-a\n", sub: ".",
			envPipe: "api", envNS: "team-b", wantPipe: "api", wantNS: "team-b"},
		{name: "each key on its own", file: "pipeline: web\nnamespace: team-a\n", sub: ".",
			envNS: "team-b", wantPipe: "web", wantNS: "team-b"},
		{name: "env only", sub: ".", envPipe: "api", wantPipe: "api"},
		{name: "the file never names a cluster", file: "pipeline: web\ncontext: prod-eks\n", sub: ".",
			wantErr: `unknown field "context": it may set pipeline and namespace only (a repository file never picks a cluster)`},
		{name: "a bad name in the file", file: "pipeline: Web_App\n", sub: ".",
			wantErr: `pipeline "Web_App" is not a valid name`},
		{name: "a bad name in the env", sub: ".", envNS: "team a",
			wantErr: `KARDINAL_NAMESPACE "team a" is not a valid name`},
		{name: "not YAML", file: "pipeline: [\n", sub: ".", wantErr: ".kardinal.yaml"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := inRepo(t, tc.file, tc.sub)
			t.Setenv("KARDINAL_PIPELINE", tc.envPipe)
			t.Setenv("KARDINAL_NAMESPACE", tc.envNS)
			p, perr := defaultPipeline()
			ns, nerr := defaultNamespace()
			if tc.wantErr != "" {
				err := perr
				if err == nil {
					err = nerr
				}
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				if tc.file != "" {
					assert.Contains(t, err.Error(), filepath.Join(root, ".kardinal.yaml"), "the error names the file")
				}
				return
			}
			require.NoError(t, perr)
			require.NoError(t, nerr)
			assert.Equal(t, tc.wantPipe, p)
			assert.Equal(t, tc.wantNS, ns)
		})
	}
}

// TestRepoDefaults_StopsAtTheRepository (#1759): the file is looked for up to
// the git repository's root, never above it; outside a repository only the
// working directory is looked at.
//
// Covers CLI-DEFAULTS-01.
func TestRepoDefaults_StopsAtTheRepository(t *testing.T) {
	t.Setenv("KARDINAL_PIPELINE", "")
	outer := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outer, ".kardinal.yaml"), []byte("pipeline: outer\n"), 0o600))
	repo := filepath.Join(outer, "repo")
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".git"), 0o755))
	t.Chdir(repo)
	p, err := defaultPipeline()
	require.NoError(t, err)
	assert.Empty(t, p, "a file above the repository is not read")

	plain := filepath.Join(t.TempDir(), "x", "y")
	require.NoError(t, os.MkdirAll(plain, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(plain), ".kardinal.yaml"), []byte("pipeline: up\n"), 0o600))
	t.Chdir(plain)
	p, err = defaultPipeline()
	require.NoError(t, err)
	assert.Empty(t, p, "outside a repository, only the working directory")
	require.NoError(t, os.WriteFile(filepath.Join(plain, ".kardinal.yaml"), []byte("pipeline: here\n"), 0o600))
	p, err = defaultPipeline()
	require.NoError(t, err)
	assert.Equal(t, "here", p)
}

// TestRepoDefaults_Args (#1759): with a default pipeline, no argument is the
// default pipeline, one argument is the environment when the default
// pipeline has an environment of that name and the pipeline otherwise, and
// two arguments are never changed. With --env the one argument is the
// pipeline. Without a default, the error names the argument, the
// environment variable and the file.
//
// Covers CLI-DEFAULTS-01.
func TestRepoDefaults_Args(t *testing.T) {
	keepStyle(t)
	web := &v1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "default"},
		Spec: v1alpha1.PipelineSpec{Environments: []v1alpha1.EnvironmentSpec{{Name: "test"}, {Name: "prod"}}}}
	c := fake.NewClientBuilder().WithScheme(rootScheme).WithObjects(web).Build()
	setForTest(t, &buildClient, func() (sigs_client.Client, string, error) { return c, "default", nil })
	inRepo(t, "pipeline: web\n", ".")
	t.Setenv("KARDINAL_PIPELINE", "")
	why := &cobra.Command{Use: "why"}
	addEnvFlag(why, new(string), "")
	cases := []struct {
		name          string
		args          []string
		flagEnv       string
		pipeline, env string
	}{
		{"no argument: the default", nil, "", "web", ""},
		{"no argument, --env", nil, "prod", "web", "prod"},
		{"an environment of the default", []string{"prod"}, "", "web", "prod"},
		{"the default by name", []string{"web"}, "", "web", ""},
		{"another pipeline", []string{"api"}, "", "api", ""},
		{"with --env, the one argument is the pipeline", []string{"prod"}, "test", "prod", "test"},
		{"two arguments", []string{"api", "prod"}, "", "api", "prod"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, env, err := envArgs(why, tc.args, tc.flagEnv)
			require.NoError(t, err)
			assert.Equal(t, tc.pipeline, p)
			assert.Equal(t, tc.env, env)
		})
	}

	t.Run("no default", func(t *testing.T) {
		inRepo(t, "", ".")
		_, _, err := envArgs(why, nil, "")
		require.EqualError(t, err, `a pipeline name is required: give it as an argument (why <pipeline>), `+
			`set KARDINAL_PIPELINE, or write "pipeline: <name>" in .kardinal.yaml at the repository's root`)
	})
}

// TestRepoDefaults_Commands (#1759): kardinal promote, pause and ship take
// the default pipeline; an explicit argument wins over it; -n wins over the
// default namespace, which wins over the kubeconfig's.
//
// Covers CLI-DEFAULTS-01.
func TestRepoDefaults_Commands(t *testing.T) {
	keepStyle(t)
	t.Setenv("KARDINAL_PIPELINE", "")
	t.Setenv("KARDINAL_NAMESPACE", "")
	inRepo(t, "pipeline: web\nnamespace: team-a\n", ".")
	run := func(args ...string) (string, error) {
		t.Helper()
		t.Cleanup(func() { globalOutput, globalNamespace = "", "" })
		root := NewRootCmd()
		var out bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&out)
		root.SetArgs(args)
		err := root.Execute()
		return out.String(), err
	}
	for _, args := range [][]string{{"promote"}, {"pause"}, {"ship", "--image", "app:1"}, {"why", "prod"}} {
		_, err := run(args...)
		require.Error(t, err, "no cluster in unit tests: the args are accepted, the client fails")
		assert.NotContains(t, err.Error(), "accepts", args)
		assert.NotContains(t, err.Error(), "a pipeline name is required", args)
	}

	// -n > KARDINAL_NAMESPACE > .kardinal.yaml > the kubeconfig's namespace.
	kubeconfig := filepath.Join(t.TempDir(), "config")
	require.NoError(t, os.WriteFile(kubeconfig, []byte(`apiVersion: v1
kind: Config
clusters: [{name: c, cluster: {server: "https://127.0.0.1:1"}}]
users: [{name: u, user: {token: x}}]
contexts: [{name: k, context: {cluster: c, user: u, namespace: from-context}}]
current-context: k
`), 0o600))
	setForTest(t, &globalKubeconfig, kubeconfig)
	_, ns, err := buildRestConfig()
	require.NoError(t, err)
	assert.Equal(t, "team-a", ns, "the file over the kubeconfig")
	t.Setenv("KARDINAL_NAMESPACE", "team-b")
	_, ns, err = buildRestConfig()
	require.NoError(t, err)
	assert.Equal(t, "team-b", ns, "the environment over the file")
	setForTest(t, &globalNamespace, "team-c")
	_, ns, err = buildRestConfig()
	require.NoError(t, err)
	assert.Equal(t, "team-c", ns, "-n over everything")
}
