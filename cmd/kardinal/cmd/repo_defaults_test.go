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
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

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
			p, psrc, perr := defaultPipeline()
			ns, nsrc, nerr := defaultNamespace()
			if tc.wantErr != "" {
				err := perr
				if err == nil {
					err = nerr
				}
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				if tc.file != "" {
					assert.Contains(t, err.Error(), filepath.Join(root, ".kardinal.yaml"), "the error names the file")
					assert.Contains(t, err.Error(), "(give -n or set KARDINAL_NAMESPACE, and the pipeline as an argument or "+
						"KARDINAL_PIPELINE, to go without the file)", "and how to go without it")
				}
				return
			}
			require.NoError(t, perr)
			require.NoError(t, nerr)
			assert.Equal(t, tc.wantPipe, p)
			assert.Equal(t, tc.wantNS, ns)
			// The source says where each came from.
			for _, c := range []struct{ v, src, env, envVal string }{{p, psrc, "KARDINAL_PIPELINE", tc.envPipe}, {ns, nsrc, "KARDINAL_NAMESPACE", tc.envNS}} {
				switch {
				case c.v == "":
				case c.envVal != "":
					assert.Equal(t, c.env, c.src)
				default:
					assert.Equal(t, filepath.Join(root, ".kardinal.yaml"), c.src)
				}
			}
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
	p, _, err := defaultPipeline()
	require.NoError(t, err)
	assert.Empty(t, p, "a file above the repository is not read")

	plain := filepath.Join(t.TempDir(), "x", "y")
	require.NoError(t, os.MkdirAll(plain, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(plain), ".kardinal.yaml"), []byte("pipeline: up\n"), 0o600))
	t.Chdir(plain)
	p, _, err = defaultPipeline()
	require.NoError(t, err)
	assert.Empty(t, p, "outside a repository, only the working directory")
	require.NoError(t, os.WriteFile(filepath.Join(plain, ".kardinal.yaml"), []byte("pipeline: here\n"), 0o600))
	p, _, err = defaultPipeline()
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
	setForTest(t, &newClient, func() (sigs_client.Client, string, error) { return c, "default", nil })
	root := inRepo(t, "pipeline: web\n", ".")
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
			var stderr bytes.Buffer
			setForTest(t, &errOut, io.Writer(&stderr))
			setForTest(t, &noticed, map[string]bool{})
			p, env, err := envArgs(why, tc.args, tc.flagEnv)
			require.NoError(t, err)
			assert.Equal(t, tc.pipeline, p)
			assert.Equal(t, tc.env, env)
			// A default the command line does not show is said, once.
			if p == "web" && (len(tc.args) == 0 || tc.args[0] != "web") {
				assert.Equal(t, "using pipeline web (from "+filepath.Join(root, ".kardinal.yaml")+")\n", stderr.String())
			} else {
				assert.Empty(t, stderr.String())
			}
		})
	}

	// The cluster cannot say whether "prod" is an environment of web: no
	// guess (QA on #2179).
	t.Run("the lookup fails", func(t *testing.T) {
		forbidden := fake.NewClientBuilder().WithScheme(rootScheme).WithObjects(web).WithInterceptorFuncs(interceptor.Funcs{
			Get: func(context.Context, sigs_client.WithWatch, sigs_client.ObjectKey, sigs_client.Object, ...sigs_client.GetOption) error {
				return apierrors.NewForbidden(schema.GroupResource{Group: "kardinal.io", Resource: "pipelines"}, "web", errors.New("no"))
			}}).Build()
		setForTest(t, &newClient, func() (sigs_client.Client, string, error) { return forbidden, "default", nil })
		_, _, err := envArgs(why, []string{"prod"}, "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), `cannot tell whether "prod" is an environment of the default pipeline web (from `+
			filepath.Join(root, ".kardinal.yaml")+"): read pipeline default/web: ")
		assert.Contains(t, err.Error(), "give both, <pipeline> <env>, or the pipeline alone with --env")
	})
	// QA on #2179: a broken file gives no default, so it cannot stop an
	// explicit pipeline; with no pipeline at all its error is the answer.
	t.Run("a broken file and an explicit pipeline", func(t *testing.T) {
		broken := inRepo(t, "pipeline: web\ncontext: prod\n", ".")
		for _, tc := range []struct {
			args    []string
			flagEnv string
		}{{[]string{"my-app"}, ""}, {[]string{"my-app"}, "prod"}, {[]string{"my-app", "prod"}, ""}} {
			p, _, err := envArgs(why, tc.args, tc.flagEnv)
			require.NoError(t, err, tc.args)
			assert.Equal(t, "my-app", p, tc.args)
		}
		_, _, err := envArgs(why, nil, "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), filepath.Join(broken, ".kardinal.yaml"))
		assert.Contains(t, err.Error(), "a repository file never picks a cluster")
	})
	t.Run("the default pipeline does not exist", func(t *testing.T) {
		empty := fake.NewClientBuilder().WithScheme(rootScheme).Build()
		setForTest(t, &newClient, func() (sigs_client.Client, string, error) { return empty, "default", nil })
		p, env, err := envArgs(why, []string{"prod"}, "")
		require.NoError(t, err)
		assert.Equal(t, "prod", p, "then the argument is a pipeline")
		assert.Empty(t, env)
	})

	t.Run("no default", func(t *testing.T) {
		inRepo(t, "", ".")
		setForTest(t, &errOut, io.Discard)
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
	root := inRepo(t, "pipeline: web\nnamespace: team-a\n", ".")
	// No cluster, ever: the commands reach the client and stop there (QA
	// on #2179: the real client would use the ambient kubeconfig).
	setForTest(t, &newClient, func() (sigs_client.Client, string, error) { return nil, "", errors.New("no cluster in this test") })
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
	for _, args := range [][]string{{"promote"}, {"pause"}, {"ship", "--image", "app:1"}} {
		out, err := run(args...)
		require.Error(t, err, "the args are accepted; then promote asks for the environment, the others reach the client")
		assert.NotContains(t, err.Error(), "accepts", args)
		assert.NotContains(t, err.Error(), "a pipeline name is required", args)
		assert.Contains(t, out, "using pipeline web (from "+filepath.Join(root, ".kardinal.yaml")+")\n", args)
	}
	// One argument with no cluster to tell: refused, not guessed.
	_, err := run("why", "prod")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `cannot tell whether "prod" is an environment of the default pipeline web`)

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
	var stderr bytes.Buffer
	setForTest(t, &errOut, io.Writer(&stderr))
	setForTest(t, &noticed, map[string]bool{})
	_, ns, err := buildRestConfig()
	require.NoError(t, err)
	assert.Equal(t, "team-a", ns, "the file over the kubeconfig")
	_, _, err = buildRestConfig()
	require.NoError(t, err)
	assert.Equal(t, "using namespace team-a (from "+filepath.Join(root, ".kardinal.yaml")+")\n", stderr.String(), "said once")
	// The MCP server's tools ignore the file (the agent's working directory).
	setForTest(t, &ignoreRepoFile, true)
	_, ns, err = buildRestConfig()
	require.NoError(t, err)
	assert.Equal(t, "from-context", ns, "the file is not read")
	ignoreRepoFile = false
	t.Setenv("KARDINAL_NAMESPACE", "team-b")
	noticed = map[string]bool{}
	_, ns, err = buildRestConfig()
	require.NoError(t, err)
	assert.Equal(t, "team-b", ns, "the environment over the file")
	assert.Contains(t, stderr.String(), "using namespace team-b (from KARDINAL_NAMESPACE)\n")
	stderr.Reset()
	setForTest(t, &globalNamespace, "team-c")
	_, ns, err = buildRestConfig()
	require.NoError(t, err)
	assert.Equal(t, "team-c", ns, "-n over everything")
	assert.Empty(t, stderr.String(), "-n is on the command line: nothing to say")
}
