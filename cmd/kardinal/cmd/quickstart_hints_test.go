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
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"
)

// TestQuickstartHints_Verbs: kardinalVerbs is every subcommand of the CLI,
// so a command quickstart prints is never taken for prose.
func TestQuickstartHints_Verbs(t *testing.T) {
	var names []string
	for _, c := range NewRootCmd().Commands() {
		names = append(names, c.Name())
	}
	// cobra adds help when the CLI runs.
	if !slices.Contains(names, "help") {
		names = append(names, "help")
	}
	slices.Sort(names)
	var listed []string
	for v := range kardinalVerbs {
		listed = append(listed, v)
	}
	slices.Sort(listed)
	assert.Equal(t, names, listed)
}

func TestQuickstartHints_Text(t *testing.T) {
	h := &hinter{kctx: "kind-kardinal", verbs: kardinalVerbs}
	for in, want := range map[string]string{
		"Approve prod:    kardinal approve demo prod":    "Approve prod:    kardinal --context kind-kardinal approve demo prod",
		"(kardinal approve demo prod)":                   "(kardinal --context kind-kardinal approve demo prod)",
		"uninstall it (helm uninstall kardinal-promoter": "uninstall it (helm --kube-context kind-kardinal uninstall kardinal-promoter",
		"  kubectl delete namespace a b":                 "  kubectl --context kind-kardinal delete namespace a b",
		"kubectl -n kardinal-git port-forward svc/x":     "kubectl --context kind-kardinal -n kardinal-git port-forward svc/x",
		// Prose stays prose; a command that names a context stays as it is.
		"Setting up the kardinal demo in kind-kardinal": "Setting up the kardinal demo in kind-kardinal",
		"helm or kubectl would use it":                  "helm or kubectl would use it",
		"kardinal --context x approve demo prod":        "kardinal --context x approve demo prod",
		"kardinal-promoter 0.13.0: installed":           "kardinal-promoter 0.13.0: installed",
	} {
		assert.Equal(t, want, h.text(in), in)
	}
	h = &hinter{kctx: "kind-my cluster", kubeconfig: "/tmp/a b/config", verbs: kardinalVerbs}
	assert.Equal(t, "kardinal --context 'kind-my cluster' --kubeconfig '/tmp/a b/config' doctor", h.text("kardinal doctor"))
	// The kubeconfig of $KUBECONFIG, as of --kubeconfig (which wins); a list
	// of files goes before the command, as --kubeconfig takes one.
	list := strings.Join([]string{"/home/me/.kube/a", "/home/me/.kube/b"}, string(filepath.ListSeparator))
	for _, tc := range []struct{ flag, env, want string }{
		{"", "", "kardinal --context kind-k doctor"},
		{"", "/tmp/kc", "kardinal --context kind-k --kubeconfig /tmp/kc doctor"},
		{"/tmp/flag", "/tmp/kc", "kardinal --context kind-k --kubeconfig /tmp/flag doctor"},
		{"", list, "KUBECONFIG=" + list + " kardinal --context kind-k doctor"},
	} {
		assert.Equal(t, tc.want, hinterFor("kind-k", tc.flag, tc.env).text("kardinal doctor"), "flag %q, env %q", tc.flag, tc.env)
	}
	err := h.err(errNotLocal)
	assert.Same(t, errNotLocal, err, "a message with no command is the error itself")
	wrapped := h.err(&exitError{code: 3})
	var ee *exitError
	assert.True(t, errors.As(wrapped, &ee), "errors.As still sees the error")
}

// commandWithoutContext matches kubectl, helm or kardinal followed by a word
// that is neither a context flag nor prose quickstart writes.
var commandWithoutContext = regexp.MustCompile(`(^|[\s(\x60'"])(kubectl|helm|kardinal) (\S+)`)

// proseAfter are the words quickstart writes after kubectl, helm or
// kardinal in a sentence ("the kardinal demo", "helm or kubectl").
var proseAfter = setOf("demo", "or", "would", "and")

// unhinted are the commands of s that name no context.
func unhinted(s string) []string {
	var out []string
	for _, m := range commandWithoutContext.FindAllStringSubmatch(s, -1) {
		if word := m[3]; word != "--context" && word != "--kube-context" && !proseAfter[word] {
			out = append(out, strings.TrimSpace(m[0]))
		}
	}
	return out
}

// TestQuickstartHints_EveryCommandNamesTheContext scans what quickstart and
// quickstart --delete print, and the errors they return, for a kubectl, helm
// or kardinal command without --context (helm: --kube-context): copied into
// a shell whose current context is another cluster, it would run there.
func TestQuickstartHints_EveryCommandNamesTheContext(t *testing.T) {
	var texts []string
	// quickstart --local and its re-run.
	c, _, out := localDemo(t)
	texts = append(texts, out)
	var buf bytes.Buffer
	require.NoError(t, quickstartFn(context.Background(), &buf, quickstartOptions{local: true, image: quickstartImage, noWait: true}))
	texts = append(texts, buf.String())
	// quickstart --local --delete, refused off a terminal, then done.
	setForTest(t, &promptable, func() bool { return false })
	cmd := newQuickstartCmd()
	require.NoError(t, cmd.ParseFlags([]string{"--local", "--delete"}))
	buf.Reset()
	err := quickstartDeleteFn(context.Background(), &buf, deleteAsk(cmd, true), true)
	require.Error(t, err)
	texts = append(texts, buf.String(), err.Error())
	buf.Reset()
	require.NoError(t, quickstartDeleteFn(context.Background(), &buf, yes, true))
	texts = append(texts, buf.String())
	_ = c

	// quickstart --repo, and its --delete.
	ghDemo := quickstartDemo(t)
	_ = ghDemo
	buf.Reset()
	require.NoError(t, quickstartDeleteFn(context.Background(), &buf, yes, false))
	texts = append(texts, buf.String())

	// The refusals of --local.
	for _, objs := range [][]sigs_client.Object{
		nil, // no default StorageClass
		{defaultClass(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: localGitNS}}},
	} {
		writeKubeconfig(t, "kind-kardinal", "https://127.0.0.1:41234")
		noCluster(t)
		rc := quickstartCluster(t, objs...)
		setForTest(t, &quickstartClient, func(*clientcmdapi.Config) (sigs_client.Client, error) { return rc, nil })
		tools := &fakeTools{t: t, helmValues: `{"scm":{"provider":"github"}}`}
		setForTest(t, &quickstartRun, tools.run)
		err := quickstartFn(context.Background(), &bytes.Buffer{}, quickstartOptions{local: true, image: quickstartImage, noWait: true})
		require.Error(t, err)
		texts = append(texts, err.Error())
	}
	writeKubeconfig(t, "kind-kardinal", "https://127.0.0.1:41234")
	noCluster(t)
	rc := quickstartCluster(t, defaultClass())
	setForTest(t, &quickstartClient, func(*clientcmdapi.Config) (sigs_client.Client, error) { return rc, nil })
	setForTest(t, &quickstartRun, (&fakeTools{t: t, helmValues: `{"scm":{"provider":"github"}}`}).run)
	err = quickstartFn(context.Background(), &bytes.Buffer{}, quickstartOptions{local: true, image: quickstartImage, noWait: true})
	require.Error(t, err)
	texts = append(texts, err.Error())

	var commands int
	for _, s := range texts {
		assert.Empty(t, unhinted(s), "a command without --context in:\n%s", s)
		commands += strings.Count(s, " --context kind-kardinal") + strings.Count(s, " --kube-context kind-kardinal")
	}
	assert.Greater(t, commands, 15, "the scan saw the commands quickstart prints")
}
