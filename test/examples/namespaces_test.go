// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package examples

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// kubectlCreateNamespace matches a literal namespace created in a README or script.
var kubectlCreateNamespace = regexp.MustCompile(`kubectl create (?:namespace|ns) ([a-z0-9][a-z0-9-]*)`)

// prerequisiteNamespaces may be used without creating them, because a
// component the example lists as a prerequisite creates them. The pattern is
// the text that must appear in the example's docs. "default" always exists.
var prerequisiteNamespaces = map[string]*regexp.Regexp{
	"argocd":      regexp.MustCompile(`Argo ?CD`),     // the Argo CD install
	"flux-system": regexp.MustCompile(`flux install`), // the Flux install
}

// exampleUnit returns the example a document belongs to: examples/<name>, or
// demo, or test/pdca. A user applies one example, so its namespaces must be
// created within it.
func exampleUnit(source string) string {
	p := strings.Fields(strings.TrimPrefix(source, "helm template "))[0]
	if rest, ok := strings.CutPrefix(p, "examples/"); ok {
		if name, _, found := strings.Cut(rest, "/"); found {
			return "examples/" + name
		}
	}
	for _, d := range manifestDirs {
		if p == d || strings.HasPrefix(p, d+"/") {
			return d
		}
	}
	return p
}

// unitText returns the READMEs, scripts and manifests of an example: the
// places where it creates a namespace or lists a prerequisite.
func unitText(t *testing.T, unit string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.WalkDir(filepath.Join(repoRoot(t), unit), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		switch filepath.Ext(p) {
		case ".md", ".sh", ".yaml", ".yml":
			raw, readErr := os.ReadFile(p)
			if readErr != nil {
				return fmt.Errorf("read %s: %w", p, readErr)
			}
			b.Write(raw)
			b.WriteByte('\n')
		}
		return nil
	})
	require.NoError(t, err)
	return b.String()
}

// namespaceProblems reports every metadata.namespace that its example neither
// creates (a Namespace object, or `kubectl create namespace` in its README or
// scripts) nor lists as a prerequisite. text maps each example to its docs.
// A rendered chart may use its release namespace: whoever installs the chart
// creates it (root-appset.yaml syncs with CreateNamespace=true).
func namespaceProblems(docs []doc, text map[string]string) []string {
	created := map[string]map[string]bool{}
	add := func(unit, ns string) {
		if created[unit] == nil {
			created[unit] = map[string]bool{}
		}
		created[unit][ns] = true
	}
	for _, d := range docs {
		if str(d.obj, "apiVersion") == "v1" && str(d.obj, "kind") == "Namespace" {
			add(exampleUnit(d.source), str(d.obj, "metadata", "name"))
		}
	}
	for unit, txt := range text {
		for _, m := range kubectlCreateNamespace.FindAllStringSubmatch(txt, -1) {
			add(unit, m[1])
		}
	}
	var problems []string
	for _, d := range docs {
		ns := str(d.obj, "metadata", "namespace")
		unit := exampleUnit(d.source)
		switch {
		case ns == "" || ns == "default" || ns == d.release || created[unit][ns]:
			continue
		case prerequisiteNamespaces[ns] != nil && prerequisiteNamespaces[ns].MatchString(text[unit]):
			continue
		}
		problems = append(problems, fmt.Sprintf(
			"%s: namespace %q is not created in %s (no Namespace object or `kubectl create namespace %s`) and is not a listed prerequisite",
			d, ns, unit, ns))
	}
	sort.Strings(problems)
	return problems
}

// TestExampleNamespacesExist checks that applying an example as its README
// says does not fail with "namespaces ... not found".
func TestExampleNamespacesExist(t *testing.T) {
	docs := allDocs(t)
	text := map[string]string{}
	var namespaced int
	for _, d := range docs {
		unit := exampleUnit(d.source)
		if _, ok := text[unit]; !ok {
			text[unit] = unitText(t, unit)
		}
		if ns := str(d.obj, "metadata", "namespace"); ns != "" && ns != "default" {
			namespaced++
		}
	}
	assert.Empty(t, namespaceProblems(docs, text))
	assert.Greater(t, namespaced, 10, "too few namespaced documents found; is the walk broken?")
}

func TestNamespaceCheckCatchesKnownMistakes(t *testing.T) {
	// The quickstart's policy-gates.yaml before C12-examples-demo-09 was fixed:
	// its gates live in platform-policies, which nothing in the quickstart created.
	const gate = "apiVersion: kardinal.io/v1alpha1\nkind: PolicyGate\n" +
		"metadata: {name: no-weekend-deploys, namespace: platform-policies}\nspec: {expression: '!schedule.isWeekend'}\n"
	const namespace = "apiVersion: v1\nkind: Namespace\nmetadata: {name: platform-policies}\n"
	const app = "apiVersion: argoproj.io/v1alpha1\nkind: Application\nmetadata: {name: a, namespace: argocd}\n"
	const pipeline = "apiVersion: kardinal.io/v1alpha1\nkind: Pipeline\nmetadata: {name: p, namespace: team-a}\n"
	const readme = "## Prerequisites\n\n- Argo CD installed and running\n"

	type file struct{ source, yaml, release string }
	tests := []struct {
		name  string
		files []file
		text  map[string]string
		want  string // "" means no problem
	}{
		{
			name:  "gates in a namespace the example does not create",
			files: []file{{source: "examples/quickstart/policy-gates.yaml", yaml: gate}},
			text:  map[string]string{"examples/quickstart": readme},
			want:  `namespace "platform-policies" is not created in examples/quickstart`,
		},
		{
			name:  "Namespace object in the same example",
			files: []file{{source: "examples/quickstart/policy-gates.yaml", yaml: namespace + "---\n" + gate}},
			text:  map[string]string{"examples/quickstart": readme},
		},
		{
			name: "Namespace object in another example does not count",
			files: []file{
				{source: "examples/quickstart/policy-gates.yaml", yaml: gate},
				{source: "examples/multi-cluster-fleet/policy-gates.yaml", yaml: namespace},
			},
			text: map[string]string{"examples/quickstart": readme, "examples/multi-cluster-fleet": ""},
			want: `namespace "platform-policies" is not created in examples/quickstart`,
		},
		{
			name:  "kubectl create namespace in the README",
			files: []file{{source: "examples/quickstart/policy-gates.yaml", yaml: gate}},
			text:  map[string]string{"examples/quickstart": "kubectl create namespace platform-policies\n"},
		},
		{
			name:  "argocd without Argo CD in the docs",
			files: []file{{source: "examples/x/app.yaml", yaml: app}},
			text:  map[string]string{"examples/x": ""},
			want:  `namespace "argocd" is not created in examples/x`,
		},
		{
			name:  "argocd with Argo CD as a prerequisite",
			files: []file{{source: "examples/x/app.yaml", yaml: app}},
			text:  map[string]string{"examples/x": readme},
		},
		{
			name:  "rendered chart in its release namespace",
			files: []file{{source: "helm template examples/multi-tenant/chart", yaml: pipeline, release: "team-a"}},
			text:  map[string]string{"examples/multi-tenant": ""},
		},
		{
			name:  "rendered chart outside its release namespace",
			files: []file{{source: "helm template examples/multi-tenant/chart", yaml: pipeline, release: "team-b"}},
			text:  map[string]string{"examples/multi-tenant": ""},
			want:  `namespace "team-a" is not created in examples/multi-tenant`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var docs []doc
			for _, f := range tt.files {
				for _, d := range splitDocs(t, f.source, []byte(f.yaml)) {
					d.release = f.release
					docs = append(docs, d)
				}
			}
			problems := namespaceProblems(docs, tt.text)
			if tt.want == "" {
				assert.Empty(t, problems)
				return
			}
			assert.Contains(t, strings.Join(problems, "\n"), tt.want)
		})
	}
	assert.Equal(t, "examples/quickstart", exampleUnit("examples/quickstart/policy-gates.yaml"))
	assert.Equal(t, "demo", exampleUnit("demo/manifests/argocd/applications.yaml"))
	assert.Equal(t, "test/pdca", exampleUnit("test/pdca/pipeline.yaml"))
	assert.Equal(t, "examples/multi-tenant", exampleUnit("helm template examples/multi-tenant/chart --values v.yaml"))
}
