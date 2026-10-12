// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package cmd

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

var updateQuickstartFixIts = flag.Bool("update-quickstart-fixits", false, "rewrite testdata/quickstart-fixits.golden")

// errStopAfterChecks ends a quickstart test at its first tool run, after
// every check before an install passed.
var errStopAfterChecks = errors.New("stopped at the first install")

// readyController is the kardinal-promoter Deployment of an install.
func readyController() *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "kardinal-promoter", Namespace: quickstartControllerNS,
			Labels: map[string]string{"app.kubernetes.io/name": "kardinal-promoter"}},
		Status: appsv1.DeploymentStatus{ReadyReplicas: 1},
	}
}

// TestQuickstart_FixIts_Golden (walkthrough v0.11): quickstart refuses a
// --repo that is not on github.com before it installs anything, unless
// --set scm.provider says which git host it is or kardinal is already
// installed; and a chart whose values schema refuses this CLI's values says
// which chart this CLI needs instead of helm's raw error.
//
// Covers CLI-QUICKSTART-03.
func TestQuickstart_FixIts_Golden(t *testing.T) {
	const forgejo = "http://forgejo.forgejo.svc.cluster.local:3000/kardinal/kardinal-demo"
	var golden strings.Builder
	repos := []struct {
		name      string
		repo      string
		sets      []string
		installed bool
		refused   bool
	}{
		{name: "a Forgejo fork", repo: forgejo, refused: true},
		{name: "a GitLab fork", repo: "https://gitlab.com/me/kardinal-demo", refused: true},
		{name: "a GitHub fork", repo: "https://github.com/me/kardinal-demo"},
		{name: "a GitHub fork, www", repo: "https://www.github.com/me/kardinal-demo"},
		{name: "a Forgejo fork with --set scm.provider", repo: forgejo, sets: []string{"scm.provider=forgejo,scm.apiURL=http://forgejo:3000"}},
		{name: "a Forgejo fork, kardinal already installed", repo: forgejo, installed: true},
	}
	for _, tc := range repos {
		t.Run(tc.name, func(t *testing.T) {
			writeKubeconfig(t, "kind-kardinal", "https://127.0.0.1:41234")
			noCluster(t)
			t.Setenv("GITHUB_TOKEN", "tok")
			b := fake.NewClientBuilder().WithScheme(rootScheme)
			if tc.installed {
				b = b.WithObjects(readyController())
			}
			c := b.Build()
			setForTest(t, &quickstartClient, func(*clientcmdapi.Config) (sigs_client.Client, error) { return c, nil })
			ran := false
			setForTest(t, &quickstartRun, func(context.Context, string, ...string) (string, error) {
				ran = true
				return "", errStopAfterChecks
			})
			var out bytes.Buffer
			err := quickstartFn(context.Background(), &out, quickstartOptions{repo: tc.repo, sets: tc.sets})
			require.Error(t, err)
			if tc.refused {
				assert.False(t, ran, "refused before any install")
				assert.Empty(t, out.String(), "refused before it says anything")
				golden.WriteString("== " + tc.name + "\n" + err.Error() + "\n\n")
				return
			}
			assert.NotContains(t, err.Error(), "quickstart supports GitHub forks")
			assert.True(t, ran || tc.installed, "went on to install: %v", err)
		})
	}

	helmOut := "Pulled: ghcr.io/pnz1990/charts/kardinal-promoter:0.10.0\n" +
		"Digest: sha256:abc\n" +
		"Error: values don't meet the specifications of the schema(s) in the following chart(s):\n" +
		"kardinal-promoter:\n" +
		"- scm: Additional property secretRef is not allowed\n"
	for _, v := range []struct{ name, cli, chart, version string }{
		{"a development CLI and the newest published chart", "v0.1.0-dev", quickstartChart, ""},
		{"a release CLI and an older --chart-version", "v0.11.0", quickstartChart, "0.10.0"},
	} {
		setForTest(t, &CLIVersion, v.cli)
		golden.WriteString("== " + v.name + "\n" + chartSchemaFixIt(helmOut, v.chart, v.version) + "\n\n")
	}
	assert.Empty(t, chartSchemaFixIt("Error: INSTALLATION FAILED: context deadline exceeded", quickstartChart, ""),
		"any other helm failure keeps helm's error")

	path := filepath.Join("testdata", "quickstart-fixits.golden")
	if *updateQuickstartFixIts {
		require.NoError(t, os.WriteFile(path, []byte(golden.String()), 0o644))
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err, "run go test -run TestQuickstart_FixIts_Golden -update-quickstart-fixits")
	assert.Equal(t, string(want), golden.String())
}

// TestQuickstart_KardinalSchemaError: a helm schema refusal of the
// kardinal-promoter chart comes back as the fix-it, not helm's raw error.
//
// Covers CLI-QUICKSTART-03.
func TestQuickstart_KardinalSchemaError(t *testing.T) {
	noCluster(t)
	t.Setenv("GITHUB_TOKEN", "tok")
	setForTest(t, &CLIVersion, "v0.1.0-dev")
	c := fake.NewClientBuilder().WithScheme(rootScheme).Build()
	setForTest(t, &quickstartRun, func(_ context.Context, name string, args ...string) (string, error) {
		out := "Pulled: ghcr.io/pnz1990/charts/kardinal-promoter:0.10.0\n" +
			"Error: values don't meet the specifications of the schema(s) in the following chart(s):\n" +
			"kardinal-promoter:\n- scm: Additional property secretRef is not allowed\n"
		return out, errors.New("helm upgrade --install ...: exit status 1\n" + out)
	})
	q := &quickstart{ctx: context.Background(), w: &bytes.Buffer{}, c: c, kctx: "kind-kardinal", kubeconfig: "/tmp/qs-kubeconfig"}
	err := q.kardinal()
	require.Error(t, err)
	assert.Equal(t, "quickstart: install kardinal-promoter: the chart ghcr.io/pnz1990/charts/kardinal-promoter:0.10.0 does not accept "+
		"the values this development CLI (v0.1.0-dev) sets (scm: Additional property secretRef is not allowed): this CLI needs the chart "+
		"of the checkout it was built from, or a later release; "+
		"pass --chart <checkout>/chart/kardinal-promoter, or --chart-version <version>", err.Error())
}

// TestQuickstart_RefusesNonGitHubRepo: a --repo off github.com is refused
// after the local-context, tool and token checks and before anything is
// installed or printed: no helm or kubectl runs, no object is written, and
// the error names the repo, says nothing was changed and links both guides.
//
// Covers CLI-QUICKSTART-03.
func TestQuickstart_RefusesNonGitHubRepo(t *testing.T) {
	for _, repo := range []string{
		"http://forgejo.forgejo.svc.cluster.local:3000/kardinal/kardinal-demo",
		"https://gitlab.com/me/kardinal-demo",
		"https://github.example.com/me/kardinal-demo",
		"https://notgithub.com/me/kardinal-demo",
	} {
		t.Run(repo, func(t *testing.T) {
			writeKubeconfig(t, "kind-kardinal", "https://127.0.0.1:41234")
			noCluster(t)
			t.Setenv("GITHUB_TOKEN", "tok")
			writes := 0
			c := fake.NewClientBuilder().WithScheme(rootScheme).WithInterceptorFuncs(interceptor.Funcs{
				Create: func(context.Context, sigs_client.WithWatch, sigs_client.Object, ...sigs_client.CreateOption) error {
					writes++
					return nil
				},
				Patch: func(context.Context, sigs_client.WithWatch, sigs_client.Object, sigs_client.Patch, ...sigs_client.PatchOption) error {
					writes++
					return nil
				},
			}).Build()
			setForTest(t, &quickstartClient, func(*clientcmdapi.Config) (sigs_client.Client, error) { return c, nil })
			var out bytes.Buffer
			err := quickstartFn(context.Background(), &out, quickstartOptions{repo: repo})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "quickstart supports GitHub forks of kardinal-demo, and "+repo+" is not on github.com, so nothing was changed")
			assert.Contains(t, err.Error(), docsURL+"installation/")
			assert.Contains(t, err.Error(), docsURL+"scm-providers/")
			assert.Contains(t, err.Error(), "--set scm.provider=")
			assert.Empty(t, out.String())
			assert.Zero(t, writes, "nothing written")
		})
	}
}

// TestGithubRepo: only github.com (and www.github.com) is GitHub.
//
// Covers CLI-QUICKSTART-03.
func TestGithubRepo(t *testing.T) {
	for repo, want := range map[string]bool{
		"https://github.com/me/kardinal-demo":         true,
		"https://GitHub.com/me/kardinal-demo":         true,
		"https://www.github.com/me/kardinal-demo":     true,
		"http://github.com/me/kardinal-demo.git":      true,
		"https://github.example.com/me/kardinal-demo": false,
		"https://notgithub.com/me/kardinal-demo":      false,
		"https://github.com.evil.io/me/kardinal-demo": false,
		"http://forgejo:3000/kardinal/kardinal-demo":  false,
		"://bad": false,
	} {
		assert.Equal(t, want, githubRepo(repo), repo)
	}
}

// TestSetsValue: a --set names a key in any of its comma-separated pairs.
//
// Covers CLI-QUICKSTART-03.
func TestSetsValue(t *testing.T) {
	tests := []struct {
		sets []string
		want bool
	}{
		{nil, false},
		{[]string{"scm.provider=forgejo"}, true},
		{[]string{"ui.enabled=true", "image.tag=x,scm.provider=gitlab"}, true},
		{[]string{" scm.provider =gitea"}, true},
		{[]string{"scm.providersAllowInsecureHTTP=true"}, false},
		{[]string{"scm.provider"}, false},
		{[]string{"x.scm.provider=github"}, false},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, setsValue(tt.sets, "scm.provider"), "%v", tt.sets)
	}
}

// TestChartSchemaFixIt_ProblemsUnderTheError: only the "- " lines under the
// schema error are its problems; a "- " line before it (helm's own list of
// what it pulled, a warning) is not, and the first other line ends them.
//
// Covers CLI-QUICKSTART-03.
func TestChartSchemaFixIt_ProblemsUnderTheError(t *testing.T) {
	setForTest(t, &CLIVersion, "v0.11.0")
	out := "- a list item helm printed before\n" +
		"Pulled: ghcr.io/pnz1990/charts/kardinal-promoter:0.10.0\n" +
		"Error: values don't meet the specifications of the schema(s) in the following chart(s):\n" +
		"kardinal-promoter:\n" +
		"- scm: Additional property secretRef is not allowed\n" +
		"- ui: Additional property sso is not allowed\n" +
		"\n" +
		"- not a problem: after the blank line\n"
	got := chartSchemaFixIt(out, quickstartChart, "0.10.0")
	assert.Contains(t, got, "(scm: Additional property secretRef is not allowed; ui: Additional property sso is not allowed)")
	assert.NotContains(t, got, "a list item")
	assert.NotContains(t, got, "after the blank line")
	assert.Contains(t, got, "this CLI needs chart 0.11.0 or later; pass --chart-version 0.11.0")

	// No Pulled line: the chart and version quickstart asked for.
	got = chartSchemaFixIt("Error: values don't meet the specifications of the schema(s)\nkardinal-promoter:\n- x: bad\n", "./chart/kardinal-promoter", "")
	assert.Contains(t, got, "the chart ./chart/kardinal-promoter does not accept")
	assert.Contains(t, got, "(x: bad)")
}
