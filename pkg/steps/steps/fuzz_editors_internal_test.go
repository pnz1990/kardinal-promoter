// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package steps

import (
	"bytes"
	"context"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
	yaml "go.yaml.in/yaml/v3"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	parentsteps "github.com/kardinal-promoter/kardinal-promoter/pkg/steps"
)

// The fuzz and property tests of the YAML editors (#1837): the yaml-update
// step (update.strategy yaml) and kustomize-set-image, run on a real checkout
// with fuzzed documents, paths and image values. Properties: no panic; a
// refused edit leaves the file byte-identical and no temporary file behind;
// an applied edit reads back, changes no value outside its target, and
// applying it again writes the same bytes.

// yamlLeaves returns every scalar and alias of the document in raw, by path
// ("a.b[0].c"), with its value. ok is false when raw does not parse as one
// mapping document.
func yamlLeaves(raw []byte) (map[string]string, bool) {
	doc, err := parseYAMLMapping(raw)
	if err != nil {
		return nil, false
	}
	out := map[string]string{}
	var walk func(n *yaml.Node, path string)
	walk = func(n *yaml.Node, path string) {
		switch n.Kind {
		case yaml.ScalarNode:
			out[path] = n.Tag + ":" + n.Value
			if n.Tag == "!!null" { // "", "~" and "null" are one value
				out[path] = "!!null"
			}
		case yaml.AliasNode:
			out[path] = "*" + n.Value
		case yaml.MappingNode:
			for i := 0; i+1 < len(n.Content); i += 2 {
				k := n.Content[i]
				key := k.Value
				switch {
				case k.Kind != yaml.ScalarNode:
					// A complex key (? ...), named by its position: no
					// Kubernetes manifest or Helm values file has one.
					key = fmt.Sprintf("?%d", i/2)
				case k.Tag == "!!null":
					// "?", "~", "null" and "" are one null key.
					key = "<null>"
				}
				p := key
				if path != "" {
					p = path + "." + key
				}
				walk(n.Content[i+1], p)
			}
		case yaml.SequenceNode:
			for i, c := range n.Content {
				walk(c, path+"["+strconv.Itoa(i)+"]")
			}
		}
	}
	walk(doc.root(), "")
	return out, true
}

// leafChanges returns the leaves of before that after removed or changed, and
// the leaves after added.
func leafChanges(before, after map[string]string) (removed, changed, added []string) {
	for k, v := range before {
		nv, ok := after[k]
		switch {
		case !ok:
			removed = append(removed, k)
		case nv != v:
			changed = append(changed, k)
		}
	}
	for k := range after {
		if _, ok := before[k]; !ok {
			added = append(added, k)
		}
	}
	sort.Strings(removed)
	sort.Strings(changed)
	sort.Strings(added)
	return removed, changed, added
}

// dirFiles lists the files of dir, so a refused edit is seen to leave no
// temporary file.
func dirFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// runEditor writes file into a fresh checkout's environment directory, runs
// step there, and returns the result, the file's new bytes and the files
// of the directory.
func runEditor(t *testing.T, step parentsteps.Step, state *parentsteps.StepState, file string, content []byte) (parentsteps.StepResult, []byte) {
	t.Helper()
	var (
		res parentsteps.StepResult
		err error
	)
	require.NotPanics(t, func() { res, err = step.Execute(context.Background(), state) })
	if res.Status == parentsteps.StepSuccess {
		require.NoError(t, err)
	}
	got, rerr := os.ReadFile(file)
	require.NoError(t, rerr)
	return res, got
}

// checkYAMLUpdateProperty is the property of FuzzYAMLUpdate and
// TestYAMLUpdate_Property: the yaml-update step sets path to tag in a
// values.yaml holding doc.
func checkYAMLUpdateProperty(t *testing.T, doc, path, tag string) {
	t.Helper()
	if tag == "" || !utf8.ValidString(tag) || len(doc) > 1<<16 {
		return // the Bundle CRD requires a tag; big documents are TestYAMLUpdate_Refusals' oversize case
	}
	dir := t.TempDir()
	envDir := filepath.Join(dir, "environments", "prod")
	require.NoError(t, os.MkdirAll(envDir, 0o755))
	file := filepath.Join(envDir, "values.yaml")
	require.NoError(t, os.WriteFile(file, []byte(doc), 0o644))
	state := &parentsteps.StepState{
		WorkDir: dir,
		Environment: v1alpha1.EnvironmentSpec{Name: "prod", Update: v1alpha1.UpdateConfig{Strategy: "yaml",
			YAML: &v1alpha1.YAMLUpdateConfig{Updates: []v1alpha1.YAMLUpdate{{File: "values.yaml", Path: path}}}}},
		Bundle:  v1alpha1.BundleSpec{Images: []v1alpha1.ImageRef{{Repository: "ghcr.io/org/app", Tag: tag}}},
		Outputs: map[string]string{},
	}
	step := &yamlUpdateStep{}
	res, got := runEditor(t, step, state, file, []byte(doc))
	require.Equal(t, []string{"values.yaml"}, dirFiles(t, envDir), "no temporary file is left")
	if res.Status != parentsteps.StepSuccess {
		require.Equal(t, doc, string(got), "a refused edit (%s) leaves the file as it was", res.Message)
		return
	}
	newDoc, err := parseYAMLMapping(got)
	require.NoError(t, err, "the written file parses:\n%s", got)
	v, ok := getYAMLPath(newDoc.root(), path)
	require.True(t, ok, "%s reads back", path)
	require.Equal(t, tag, v)

	before, ok := yamlLeaves([]byte(doc))
	require.True(t, ok, "an edited document parsed")
	after, ok := yamlLeaves(got)
	require.True(t, ok)
	removed, changed, added := leafChanges(before, after)
	// The target is the one leaf that may change or appear; a null on the
	// path to it may become the mapping that holds it.
	// No leaf changes when the target already held the tag.
	edited := make([]string, 0, len(changed)+len(added))
	edited = append(append(edited, changed...), added...)
	require.LessOrEqual(t, len(edited), 1, "only the target changes: changed %v added %v; doc:\n%s\nafter:\n%s", changed, added, doc, got)
	target := ""
	if len(edited) == 1 {
		target = edited[0]
		require.Equal(t, "!!str:"+tag, after[target], "leaf %s", target)
	}
	for _, k := range removed {
		require.Equal(t, "!!null", before[k], "leaf %s was removed (%q); doc:\n%s\nafter:\n%s", k, before[k], doc, got)
		require.True(t, strings.HasPrefix(target, k+"."), "the removed null %s is on the path to the target %s", k, target)
	}

	// Byte for byte, the file outside the edited top-level entry is as it
	// was (#1907).
	if key := topKeyOf(path); key != "" {
		if same, ok := onlyTopKeyChanged([]byte(doc), got, key); ok {
			require.True(t, same, "only the %s entry changes, byte for byte; doc:\n%s\nafter:\n%s", key, doc, got)
		}
	}

	// Applying the same edit again writes the same bytes.
	res2, again := runEditor(t, step, state, file, got)
	require.Equal(t, parentsteps.StepSuccess, res2.Status, res2.Message)
	require.Equal(t, string(got), string(again), "yaml-update is idempotent")
}

// checkKustomizeProperty is the property of FuzzKustomizeSetImage and
// TestKustomizeSetImage_Property: kustomize-set-image writes repo with tag
// and digest into a kustomization.yaml holding doc.
func checkKustomizeProperty(t *testing.T, doc, repo, tag, digest string) {
	t.Helper()
	if repo == "" || !utf8.ValidString(repo+tag+digest) || len(doc) > 1<<16 || (tag == "" && digest == "") {
		return // the Bundle CRD requires a repository and a tag or digest
	}
	dir := t.TempDir()
	envDir := filepath.Join(dir, "environments", "prod")
	require.NoError(t, os.MkdirAll(envDir, 0o755))
	file := filepath.Join(envDir, "kustomization.yaml")
	require.NoError(t, os.WriteFile(file, []byte(doc), 0o644))
	state := &parentsteps.StepState{
		WorkDir:     dir,
		Environment: v1alpha1.EnvironmentSpec{Name: "prod"},
		Bundle:      v1alpha1.BundleSpec{Images: []v1alpha1.ImageRef{{Repository: repo, Tag: tag, Digest: digest}}},
		Outputs:     map[string]string{},
	}
	step := &kustomizeSetImageStep{}
	res, got := runEditor(t, step, state, file, []byte(doc))
	require.Equal(t, []string{"kustomization.yaml"}, dirFiles(t, envDir), "no temporary file is left")
	if res.Status != parentsteps.StepSuccess {
		require.Equal(t, doc, string(got), "a refused edit (%s) leaves the file as it was", res.Message)
		return
	}
	newDoc, err := parseYAMLMapping(got)
	require.NoError(t, err, "the written file parses:\n%s", got)
	images := mapValue(newDoc.root(), "images")
	require.NotNil(t, images)
	require.Equal(t, yaml.SequenceNode, images.Kind)
	entry := imageEntryByName(images, repo)
	require.NotNil(t, entry, "an images entry named %s", repo)
	require.Equal(t, tag, scalarValue(entry, "newTag"))
	require.Equal(t, digest, scalarValue(entry, "digest"))

	before, ok := yamlLeaves([]byte(doc))
	require.True(t, ok)
	after, ok := yamlLeaves(got)
	require.True(t, ok)
	removed, changed, _ := leafChanges(before, after)
	for _, k := range append(removed, changed...) {
		require.True(t, k == "images" || strings.HasPrefix(k, "images["),
			"only images entries change, not %s; doc:\n%s\nafter:\n%s", k, doc, got)
	}

	// Byte for byte, the file outside the images entry is as it was (#1907).
	if same, ok := onlyTopKeyChanged([]byte(doc), got, "images"); ok {
		require.True(t, same, "only the images entry changes, byte for byte; doc:\n%s\nafter:\n%s", doc, got)
	}

	res2, again := runEditor(t, step, state, file, got)
	require.Equal(t, parentsteps.StepSuccess, res2.Status, res2.Message)
	require.Equal(t, string(got), string(again), "kustomize-set-image is idempotent")
}

// yamlSeedDocs are documents the editors meet: Helm values, a Deployment,
// a kustomization, comments, flow style, anchors and aliases, merge keys,
// nulls, several documents and a non-mapping top level.
var yamlSeedDocs = []string{
	"",
	"# only a comment\n",
	"image:\n  tag: \"1.0.0\" # pinned\n  repository: ghcr.io/org/app\nreplicas: 2\n",
	"apiVersion: apps/v1\nkind: Deployment\nspec:\n  template:\n    spec:\n      containers:\n        - name: app\n          image: ghcr.io/org/app:1.0.0\n        - name: sidecar\n          image: busybox\n",
	"resources:\n- deploy.yaml\nimages:\n- name: ghcr.io/org/app\n  newTag: 1.0.0\n- name: app\n  newName: ghcr.io/org/app\n",
	"images: []\nnamePrefix: prod-\n",
	"images:\n",
	"images: notalist\n",
	"base: &b\n  tag: x\nimage:\n  <<: *b\n  tag: y\n",
	"a: &x 1\nb: *x\n",
	"image: {tag: '1', pull: Always}\n",
	"image: null\nlist: [1, 2, 3]\n",
	"a: 1\n---\nb: 2\n",
	"a: 1\n---\n# end\n",
	"- 1\n- 2\n",
	"plain scalar",
	"a: 1\na: 2\n",
	"key: |\n  literal\n  block\nother: >\n  folded\n",
	"dependencies:\n  - name: podinfo\n    version: 6.0.0\n  - name: redis\n    version: 1.0.0\n",
	"\"quoted key\": 1\n? complex\n: 2\n",
}

var yamlSeedPaths = []string{
	"image.tag", ".image.tag", "spec.template.spec.containers[0].image",
	"spec.template.spec.containers[name=app].image", "dependencies[name=podinfo].version",
	"dependencies.0.version", "list[1]", "list.5", "a", "b", "image.new.deep.key", "base.tag",
	"image", "key", "", ".", "a..b", "a[", "a[01]", "a[x=]", "images[0].newTag",
}

var yamlSeedTags = []string{"2.0.0", "true", "null", "1e3", "~", "a: b", "#x", "v1.2.3@sha256:abc", "multi\nline", " "}

// FuzzYAMLUpdate fuzzes the yaml-update step with checkYAMLUpdateProperty.
// Run it with go test ./pkg/steps/steps -run '^$' -fuzz '^FuzzYAMLUpdate$' -fuzztime 2m.
func FuzzYAMLUpdate(f *testing.F) {
	for i, doc := range append(append([]string{}, yamlSeedDocs...), styledDocs()...) {
		f.Add(doc, yamlSeedPaths[i%len(yamlSeedPaths)], yamlSeedTags[i%len(yamlSeedTags)])
	}
	f.Fuzz(func(t *testing.T, doc, path, tag string) { checkYAMLUpdateProperty(t, doc, path, tag) })
}

// FuzzKustomizeSetImage fuzzes kustomize-set-image with checkKustomizeProperty.
// Run it with go test ./pkg/steps/steps -run '^$' -fuzz '^FuzzKustomizeSetImage$' -fuzztime 2m.
func FuzzKustomizeSetImage(f *testing.F) {
	repos := []string{"ghcr.io/org/app", "app", "docker.io/library/nginx", "a/b/c/app"}
	for i, doc := range append(append([]string{}, yamlSeedDocs...), styledDocs()...) {
		f.Add(doc, repos[i%len(repos)], yamlSeedTags[i%len(yamlSeedTags)], []string{"", "sha256:abc"}[i%2])
	}
	f.Fuzz(func(t *testing.T, doc, repo, tag, digest string) { checkKustomizeProperty(t, doc, repo, tag, digest) })
}

// genYAML writes a random mapping document: nested mappings and lists over
// a small key set (so paths hit), with comments, flow style, quoting,
// anchors and aliases.
func genYAML(r *rand.Rand) (string, []string) {
	keys := []string{"image", "tag", "spec", "containers", "name", "app", "list", "a", "b", "0", "images", "newTag"}
	var b strings.Builder
	var paths []string
	anchors := 0
	var gen func(depth int, indent string, path string)
	gen = func(depth int, indent string, path string) {
		n := 1 + r.Intn(4)
		used := map[string]bool{}
		for i := 0; i < n; i++ {
			k := keys[r.Intn(len(keys))]
			if used[k] {
				continue
			}
			used[k] = true
			p := k
			if path != "" {
				p = path + "." + k
			}
			if r.Intn(6) == 0 {
				fmt.Fprintf(&b, "%s# comment %d\n", indent, i)
			}
			switch c := r.Intn(10); {
			case (c < 4 || depth > 3) && r.Intn(4) == 0:
				// A block scalar in any style (styledValues), indented under
				// its key (#1965).
				v := styledValues[3+r.Intn(len(styledValues)-3)]
				lines := strings.Split(v, "\n")
				for i := 1; i < len(lines); i++ {
					if lines[i] != "" {
						lines[i] = indent + lines[i]
					}
				}
				fmt.Fprintf(&b, "%s%s: %s\n", indent, k, strings.Join(lines, "\n"))
				paths = append(paths, p)
			case c < 4 || depth > 3:
				fmt.Fprintf(&b, "%s%s: %s\n", indent, k, []string{"1", "\"x\"", "'y'", "null", "true", "v1.0", "~", ""}[r.Intn(8)])
				paths = append(paths, p)
			case c < 6:
				fmt.Fprintf(&b, "%s%s:\n", indent, k)
				gen(depth+1, indent+"  ", p)
			case c < 8:
				fmt.Fprintf(&b, "%s%s:\n", indent, k)
				for j := 0; j < 1+r.Intn(3); j++ {
					if r.Intn(2) == 0 {
						fmt.Fprintf(&b, "%s- name: n%d\n%s  tag: t%d\n", indent, j, indent, j)
						paths = append(paths, fmt.Sprintf("%s[%d].tag", p, j), fmt.Sprintf("%s[name=n%d].tag", p, j))
					} else {
						fmt.Fprintf(&b, "%s- s%d\n", indent, j)
						paths = append(paths, fmt.Sprintf("%s[%d]", p, j))
					}
				}
			case c < 9:
				fmt.Fprintf(&b, "%s%s: {x: 1, y: [a, b]}\n", indent, k)
				paths = append(paths, p+".x", p+".z")
			default:
				if anchors > 0 && r.Intn(2) == 0 {
					fmt.Fprintf(&b, "%s%s: *a%d\n", indent, k, r.Intn(anchors))
				} else {
					fmt.Fprintf(&b, "%s%s: &a%d v\n", indent, k, anchors)
					anchors++
				}
				paths = append(paths, p)
			}
		}
	}
	gen(0, "", "")
	return b.String(), paths
}

// TestYAMLUpdate_Property runs checkYAMLUpdateProperty over the seeds and
// 400 generated documents, with paths that exist, paths that do not, and
// tags YAML would read as other types. Covers STEP-FUZZ-01.
func TestYAMLUpdate_Property(t *testing.T) {
	type input struct{ doc, path, tag string }
	var inputs []input
	for _, d := range yamlSeedDocs {
		for _, p := range yamlSeedPaths {
			inputs = append(inputs, input{d, p, yamlSeedTags[len(inputs)%len(yamlSeedTags)]})
		}
	}
	r := rand.New(rand.NewSource(1837))
	for i := 0; i < 400; i++ {
		doc, paths := genYAML(r)
		path := yamlSeedPaths[r.Intn(len(yamlSeedPaths))]
		if len(paths) > 0 && r.Intn(4) != 0 {
			path = paths[r.Intn(len(paths))]
		}
		inputs = append(inputs, input{doc, path, yamlSeedTags[r.Intn(len(yamlSeedTags))]})
	}
	for i, in := range inputs {
		in := in
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			t.Parallel()
			checkYAMLUpdateProperty(t, in.doc, in.path, in.tag)
		})
	}
}

// TestKustomizeSetImage_Property runs checkKustomizeProperty over the seeds
// and 300 generated documents with an images list. Covers STEP-FUZZ-02.
func TestKustomizeSetImage_Property(t *testing.T) {
	type input struct{ doc, repo, tag, digest string }
	repos := []string{"ghcr.io/org/app", "app", "docker.io/library/nginx", "a/b/c/app", "n0"}
	var inputs []input
	for i, d := range yamlSeedDocs {
		for j, rp := range repos {
			inputs = append(inputs, input{d, rp, yamlSeedTags[(i+j)%len(yamlSeedTags)], []string{"", "sha256:abc"}[j%2]})
		}
	}
	r := rand.New(rand.NewSource(1837))
	for i := 0; i < 300; i++ {
		doc, _ := genYAML(r)
		if r.Intn(2) == 0 {
			doc += "images:\n- name: " + repos[r.Intn(len(repos))] + "\n  newTag: old\n- name: app\n  newName: " + repos[r.Intn(len(repos))] + "\n"
		}
		tag := []string{"", "2.0.0", "true"}[r.Intn(3)]
		digest := []string{"", "sha256:def"}[r.Intn(2)]
		if tag == "" && digest == "" {
			tag = "2.0.0"
		}
		inputs = append(inputs, input{doc, repos[r.Intn(len(repos))], tag, digest})
	}
	for i, in := range inputs {
		in := in
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			t.Parallel()
			checkKustomizeProperty(t, in.doc, in.repo, in.tag, in.digest)
		})
	}
}

// TestYAMLUpdate_Refusals: the inputs yaml-update refuses (an anchor or an
// alias on the path, a merge key, several documents, a duplicate key, a file
// over 4 MiB) fail the step and leave the file byte-identical.
// Covers STEP-FUZZ-03.
func TestYAMLUpdate_Refusals(t *testing.T) {
	for name, tc := range map[string]struct{ doc, path string }{
		"anchor on path":  {"image: &i\n  tag: 1\n", "image.tag"},
		"alias on path":   {"base: &b {tag: 1}\nimage: *b\n", "image.tag"},
		"anchored target": {"image:\n  tag: &t 1\n", "image.tag"},
		"merge key":       {"base: &b {x: 1}\nimage:\n  <<: *b\n", "image.tag"},
		"two documents":   {"image: {tag: 1}\n---\nother: 2\n", "image.tag"},
		"duplicate key":   {"image: {tag: 1}\nimage: {tag: 2}\n", "image.tag"},
		"not a scalar":    {"image:\n  tag: {a: 1}\n", "image.tag"},
		"missing element": {"list: [a]\n", "list[3]"},
		"top level list":  {"- a\n", "a"},
		"oversize":        {"image: {tag: 1}\npad: \"" + strings.Repeat("x", maxYAMLUpdateFile) + "\"\n", "image.tag"},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			envDir := filepath.Join(dir, "environments", "prod")
			require.NoError(t, os.MkdirAll(envDir, 0o755))
			file := filepath.Join(envDir, "values.yaml")
			require.NoError(t, os.WriteFile(file, []byte(tc.doc), 0o644))
			state := &parentsteps.StepState{
				WorkDir: dir,
				Environment: v1alpha1.EnvironmentSpec{Name: "prod", Update: v1alpha1.UpdateConfig{Strategy: "yaml",
					YAML: &v1alpha1.YAMLUpdateConfig{Updates: []v1alpha1.YAMLUpdate{{File: "values.yaml", Path: tc.path}}}}},
				Bundle:  v1alpha1.BundleSpec{Images: []v1alpha1.ImageRef{{Repository: "ghcr.io/org/app", Tag: "2.0.0"}}},
				Outputs: map[string]string{},
			}
			res, got := runEditor(t, &yamlUpdateStep{}, state, file, []byte(tc.doc))
			require.Equal(t, parentsteps.StepFailed, res.Status)
			require.Equal(t, tc.doc, string(got), "the file is unchanged")
			require.Equal(t, []string{"values.yaml"}, dirFiles(t, envDir))
		})
	}
}

// TestEncode_KeepsFlowNulls: a null with no text inside a flow collection
// ("{a}", "{a: }") is written back as null, not as ” (an empty string), and
// a block null stays "a:". FuzzYAMLUpdate found it: every yaml-update,
// helm-set-image and kustomize-set-image edit of such a file turned the
// nulls into strings. Covers STEP-FUZZ-04.
func TestEncode_KeepsFlowNulls(t *testing.T) {
	for in, want := range map[string]string{
		"{0}\n":       "{0: null}\n",
		"{a, b: 1}\n": "{a: null, b: 1}\n",
		// A block document is spliced (#1907): an unedited file is
		// written back byte for byte.
		"x: {a: }\ny: [~, 1]\n": "x: {a: }\ny: [~, 1]\n",
		"a:\nb: 1\n":            "a:\nb: 1\n",
		"a: ~\nb: null\n":       "a: ~\nb: null\n",
	} {
		doc, err := parseYAMLMapping([]byte(in))
		require.NoError(t, err)
		out, err := doc.encode()
		require.NoError(t, err)
		require.Equal(t, want, string(out), in)
		before, _ := yamlLeaves([]byte(in))
		after, _ := yamlLeaves(out)
		require.Equal(t, before, after, "%q keeps its values", in)
	}
}

// topKeyOf is the first key of a yaml-update path ("image" of
// ".image.tag", "spec" of "spec.containers[0].image").
func topKeyOf(path string) string {
	p := strings.TrimPrefix(path, ".")
	if i := strings.IndexAny(p, ".["); i >= 0 {
		p = p[:i]
	}
	return p
}

// onlyTopKeyChanged reports whether out is orig with only the entry of the
// top-level key changed, byte for byte: the lines before the key's line
// are the same, and so are the lines from the next top-level key (or its
// head comment) to the end; the blank and comment lines between the entry
// and that key may move. When orig has no such key, out is orig with the
// entry's lines inserted. ok is false for a document whose top-level lines
// it cannot tell apart: not a one-document block mapping with plain unique
// keys at column 1, CR line endings, or no final line break.
func onlyTopKeyChanged(orig, out []byte, key string) (same, ok bool) {
	o, ok1 := topEntries(orig)
	n, ok2 := topEntries(out)
	if !ok1 {
		return false, false
	}
	if !ok2 {
		return false, true
	}
	ol, nl := splitLines(orig), splitLines(out)
	oe, inOrig := o[key]
	ne, inOut := n[key]
	switch {
	case !inOut:
		return false, true
	case !inOrig:
		k := ne[0]
		tail := len(ol) - k
		return tail >= 0 && len(nl) >= len(ol) && strings.Join(nl[:k], "") == strings.Join(ol[:k], "") &&
			strings.Join(nl[len(nl)-tail:], "") == strings.Join(ol[k:], ""), true
	}
	return strings.Join(ol[:oe[0]], "") == strings.Join(nl[:ne[0]], "") &&
		strings.Join(ol[oe[1]:], "") == strings.Join(nl[ne[1]:], ""), true
}

func splitLines(b []byte) []string {
	l := strings.SplitAfter(string(b), "\n")
	if len(l) > 0 && l[len(l)-1] == "" {
		l = l[:len(l)-1]
	}
	return l
}

// topEntries returns, for each top-level key of raw, the 0-based index of
// its line and of the first line after its entry (the next key's head
// comment or line, or the end of the file).
func topEntries(raw []byte) (map[string][2]int, bool) {
	if bytes.Contains(raw, []byte("\r")) || bytes.Contains(raw, []byte("\n---")) || bytes.HasPrefix(raw, []byte("---")) ||
		!bytes.HasSuffix(raw, []byte("\n")) {
		return nil, false
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil || doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 {
		return nil, false
	}
	m := doc.Content[0]
	if m.Kind != yaml.MappingNode || m.Style&yaml.FlowStyle != 0 || len(m.Content) == 0 {
		return nil, false
	}
	out := map[string][2]int{}
	total := len(splitLines(raw))
	for i := 0; i+1 < len(m.Content); i += 2 {
		k := m.Content[i]
		if k.Kind != yaml.ScalarNode || k.Column != 1 || m.Content[i+1].Line < k.Line {
			return nil, false
		}
		if _, dup := out[k.Value]; dup {
			return nil, false
		}
		end := total
		if i+2 < len(m.Content) {
			next := m.Content[i+2]
			end = next.Line - 1
			if next.HeadComment != "" {
				end -= strings.Count(next.HeadComment, "\n") + 1
			}
		}
		out[k.Value] = [2]int{k.Line - 1, end}
	}
	return out, true
}
