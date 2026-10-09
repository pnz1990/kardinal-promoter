// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package steps

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"text/template"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/kustomize/kyaml/filesys"

	parentsteps "github.com/kardinal-promoter/kardinal-promoter/pkg/steps"
)

// TestManifestFiles: one file per object, namespaced under its namespace,
// sorted, a group suffix for a kind/name clash across API groups, and the
// object count limit.
func TestManifestFiles(t *testing.T) {
	docs := [][]byte{
		[]byte("apiVersion: v1\nkind: Service\nmetadata: {name: web, namespace: prod}\n"),
		[]byte("apiVersion: apps/v1\nkind: Deployment\nmetadata: {name: web, namespace: prod}\n"),
		[]byte("apiVersion: v1\nkind: Namespace\nmetadata: {name: prod}\n"),
		[]byte("apiVersion: example.com/v1\nkind: Deployment\nmetadata: {name: web, namespace: prod}\n"),
	}
	files, err := manifestFiles(docs)
	require.NoError(t, err)
	var paths []string
	for _, f := range files {
		paths = append(paths, f.path)
	}
	assert.Equal(t, []string{"namespace-prod.yaml", "prod_deployment-web.example.com.yaml", "prod_deployment-web.yaml",
		"prod_service-web.yaml"}, paths)

	_, err = manifestFiles([][]byte{[]byte("kind: ConfigMap\n")})
	assert.ErrorContains(t, err, "no kind or metadata.name")

	many := make([][]byte, maxRenderedObjects+1)
	for i := range many {
		many[i] = []byte(fmt.Sprintf("kind: ConfigMap\nmetadata: {name: c%d}\n", i))
	}
	_, err = manifestFiles(many)
	assert.ErrorContains(t, err, "more than the limit")

	big := []byte("kind: ConfigMap\nmetadata: {name: big}\ndata: {x: \"" + strings.Repeat("x", maxRenderOutputBytes) + "\"}\n")
	_, err = manifestFiles([][]byte{big})
	assert.ErrorContains(t, err, "larger than 16 MiB")
}

// TestFileSafe keeps names on one path segment.
func TestFileSafe(t *testing.T) {
	assert.Equal(t, "a_b", fileSafe("a/b"))
	assert.Equal(t, "_", fileSafe(".."))
	assert.Equal(t, "web-1.example", fileSafe("Web-1.Example"))
}

// TestRemoteRef recognises the remote forms kustomize would fetch.
func TestRemoteRef(t *testing.T) {
	for _, r := range []string{"https://github.com/o/r//base", "git@github.com:o/r.git", "github.com/o/r/base?ref=v1",
		"ssh://h/r", "http://169.254.169.254/latest/meta-data", "file:///var/run/secrets/token"} {
		assert.True(t, remoteRef(r), r)
	}
	for _, r := range []string{"../base", "deployment.yaml", "components/x"} {
		assert.False(t, remoteRef(r), r)
	}
}

// TestCheckKustomizations_EveryField (QA on #1515): a URL is refused in any
// field kustomize loads, not only resources: configMapGenerator files (the
// proven SSRF), envs, patch paths, openapi, crds, replacements and the
// rest, also nested. Data fields (generator literals, labels, annotations,
// inline patches) may hold one.
func TestCheckKustomizations_EveryField(t *testing.T) {
	const url = "http://169.254.169.254/latest/meta-data/iam"
	refused := map[string]string{
		"resources":                "resources: [" + url + "]",
		"components":               "components: [" + url + "]",
		"bases":                    "bases: [" + url + "]",
		"configMapGenerator.files": "configMapGenerator:\n- name: x\n  files: [" + url + "]",
		"configMapGenerator.envs":  "configMapGenerator:\n- name: x\n  envs: [" + url + "]",
		"secretGenerator.files":    "secretGenerator:\n- name: x\n  files: [key=" + url + "]",
		"patches.path":             "patches:\n- path: " + url,
		"patchesStrategicMerge":    "patchesStrategicMerge: [" + url + "]",
		"patchesJson6902.path":     "patchesJson6902:\n- path: " + url + "\n  target: {kind: Deployment, name: web}",
		"openapi.path":             "openapi:\n  path: " + url,
		"crds":                     "crds: [" + url + "]",
		"replacements.path":        "replacements:\n- path: " + url,
		"generators":               "generators: [" + url + "]",
		"transformers":             "transformers: [" + url + "]",
		"configurations":           "configurations: [" + url + "]",
		"scp-style git":            "resources: [git@github.com:org/repo.git]",
		"helmCharts":               "helmCharts:\n- name: x\n  repo: oci://ghcr.io/x",
	}
	for name, k := range refused {
		t.Run(name, func(t *testing.T) {
			mem := filesys.MakeFsInMemory()
			require.NoError(t, mem.WriteFile("/env/kustomization.yaml", []byte(k+"\n")))
			err := checkKustomizations(mem)
			require.Error(t, err)
			assert.True(t, errors.Is(err, parentsteps.ErrPermanent))
			if name != "helmCharts" {
				assert.Contains(t, err.Error(), "remote reference")
			}
		})
	}
	allowed := []string{
		"configMapGenerator:\n- name: x\n  literals: [API_URL=https://api.example.com]",
		"commonAnnotations: {link.argocd.argoproj.io/docs: \"https://docs.example.com\"}",
		"labels:\n- pairs: {team: platform}",
		"patches:\n- target: {kind: Deployment}\n  patch: |-\n    - op: add\n      path: /metadata/annotations/url\n      value: https://example.com",
		"patchesStrategicMerge:\n- |-\n  apiVersion: v1\n  kind: ConfigMap\n  metadata: {name: x, annotations: {u: \"https://x\"}}",
		"resources: [deployment.yaml, ../base]",
	}
	for _, k := range allowed {
		mem := filesys.MakeFsInMemory()
		require.NoError(t, mem.WriteFile("/env/kustomization.yaml", []byte(k+"\n")))
		assert.NoError(t, checkKustomizations(mem), k)
	}
}

// TestEstimateKustomizeObjects: a diamond of overlays, each including the
// next level twice, is estimated without building it, and refused once it
// passes the object limit; a cycle is an error.
func TestEstimateKustomizeObjects(t *testing.T) {
	mem := filesys.MakeFsInMemory()
	require.NoError(t, mem.WriteFile("/l0/kustomization.yaml", []byte("resources: [cm.yaml]\nconfigMapGenerator:\n- name: g\n")))
	require.NoError(t, mem.WriteFile("/l0/cm.yaml", []byte("kind: ConfigMap\nmetadata: {name: a}\n---\nkind: ConfigMap\nmetadata: {name: b}\n")))
	for i := 1; i <= 20; i++ {
		k := fmt.Sprintf("resources: [../l%d, ../l%d-copy]\n", i-1, i-1)
		require.NoError(t, mem.WriteFile(fmt.Sprintf("/l%d/kustomization.yaml", i), []byte(k)))
		require.NoError(t, mem.WriteFile(fmt.Sprintf("/l%d-copy/kustomization.yaml", i-1), []byte(fmt.Sprintf("resources: [../l%d]\nnamePrefix: c-\n", i-1))))
	}
	n, err := estimateKustomizeObjects(mem, "/l3")
	require.NoError(t, err)
	assert.Equal(t, 3*8, n, "3 objects at the bottom, doubled three times")
	n, err = estimateKustomizeObjects(mem, "/l20")
	require.NoError(t, err)
	assert.Greater(t, n, maxRenderedObjects, "3 x 2^20 saturates past the limit")

	require.NoError(t, mem.WriteFile("/a/kustomization.yaml", []byte("resources: [../b]\n")))
	require.NoError(t, mem.WriteFile("/b/kustomization.yaml", []byte("resources: [../a]\n")))
	_, err = estimateKustomizeObjects(mem, "/a")
	assert.ErrorContains(t, err, "includes itself")
}

// renderTemplate executes tpl with the bounded functions.
func renderTemplate(t *testing.T, tpl string, allowNondeterministic bool) (string, error) {
	t.Helper()
	tm, err := template.New("t").Funcs(templateFuncs(allowNondeterministic)).Parse(tpl)
	require.NoError(t, err)
	var b strings.Builder
	err = tm.Execute(&b, nil)
	return b.String(), err
}

// TestTemplateFuncs_Bounded (QA on #1515): a template that doubles a string
// in a loop stops at the output limit with an error instead of filling the
// memory; repeat, indent, until and seq are refused before they allocate.
func TestTemplateFuncs_Bounded(t *testing.T) {
	_, err := renderTemplate(t, `{{ $x := "aaaaaaaa" }}{{ range until 40 }}{{ $x = print $x $x }}{{ end }}{{ len $x }}`, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "larger than the render limit")
	for _, tpl := range []string{
		`{{ repeat 1000000000 "x" }}`,
		`{{ "x" | indent 1000000000 }}`,
		`{{ "x" | nindent 1000000000 }}`,
		`{{ until 1000000000 }}`,
		`{{ untilStep 0 1000000000 1 }}`,
		`{{ seq 1000000000 }}`,
		`{{ $x := repeat 1000000 "ab" }}{{ replace "" $x $x }}`,
	} {
		_, err := renderTemplate(t, tpl, false)
		require.Error(t, err, tpl)
		assert.Contains(t, err.Error(), "larger than the render limit", tpl)
	}
	out, err := renderTemplate(t, `{{ repeat 3 "ab" }} {{ "a\nb" | indent 2 }} {{ len (until 5) }} {{ printf "%s-%d" "x" 1 }}`, false)
	require.NoError(t, err)
	assert.Equal(t, "ababab   a\n  b 5 x-1", out)
}

// TestTemplateFuncs_Nondeterministic (QA on #1515): functions whose result
// changes between renders fail unless render.allowNondeterministic, and env
// is never available.
func TestTemplateFuncs_Nondeterministic(t *testing.T) {
	for _, tpl := range []string{`{{ randAlphaNum 8 }}`, `{{ uuidv4 }}`, `{{ now }}`, `{{ genCA "x" 365 }}`,
		`{{ genPrivateKey "rsa" }}`, `{{ bcrypt "x" }}`, `{{ randInt 1 9 }}`, `{{ shuffle "abc" }}`} {
		_, err := renderTemplate(t, tpl, false)
		require.Error(t, err, tpl)
		assert.Contains(t, err.Error(), "render.allowNondeterministic", tpl)
		_, err = renderTemplate(t, tpl, true)
		assert.NoError(t, err, "%s with allowNondeterministic", tpl)
	}
	_, err := renderTemplate(t, `{{ env "HOME" }}`, true)
	assert.ErrorContains(t, err, "not available")
	out, err := renderTemplate(t, `{{ "a" | upper }}{{ sha256sum "x" | trunc 4 }}`, false)
	require.NoError(t, err)
	assert.Equal(t, "A2d71", out)
}

// TestTrailers parses the last paragraph of a commit message.
func TestTrailers(t *testing.T) {
	msg := "[kardinal] Promote b to prod\n\nBundle: b\nPipeline: p\n\nKardinal-Dry-Commit: abc\nKardinal-Bundle: b\n"
	assert.Equal(t, map[string]string{"Kardinal-Dry-Commit": "abc", "Kardinal-Bundle": "b"}, trailers(msg))
}
