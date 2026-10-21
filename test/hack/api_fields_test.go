// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package hack

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/yaml"
)

// crdDir holds the generated CRDs, at every release as in this tree.
const crdDir = "config/crd/bases"

// crdField is one field of a CRD version's schema: "<Kind> <version>
// <path>", the path "" for the version itself, with "[]" for a list's items
// and "{}" for a map's values.
type crdField struct {
	// The field is deprecated when its description has a paragraph that
	// starts "Deprecated:" (the Go doc comment's, which controller-gen
	// copies), or when it is a version marked deprecated.
	deprecated bool
	parent     string // the key of the enclosing field; "" for a version
}

// crdFields reads every field of every version of the CRDs in files (file
// name to YAML), by key.
func crdFields(files map[string][]byte) (map[string]crdField, error) {
	out := map[string]crdField{}
	for name, raw := range files {
		var crd apiextv1.CustomResourceDefinition
		if err := yaml.Unmarshal(raw, &crd); err != nil {
			return nil, fmt.Errorf("parse CRD %s: %w", name, err)
		}
		for _, v := range crd.Spec.Versions {
			root := crd.Spec.Names.Kind + " " + v.Name
			var schema *apiextv1.JSONSchemaProps
			if v.Schema != nil {
				schema = v.Schema.OpenAPIV3Schema
			}
			out[root] = crdField{deprecated: v.Deprecated || (schema != nil && deprecatedText(schema.Description))}
			if schema != nil {
				walkFields(out, root, root, "", schema)
			}
		}
	}
	return out, nil
}

// walkFields adds the fields under s, the field at key ("<Kind> <version>"
// prefix, then path).
func walkFields(out map[string]crdField, prefix, key, path string, s *apiextv1.JSONSchemaProps) {
	add := func(p string, child *apiextv1.JSONSchemaProps) {
		k := prefix + " " + p
		out[k] = crdField{deprecated: deprecatedText(child.Description), parent: key}
		walkFields(out, prefix, k, p, child)
	}
	names := make([]string, 0, len(s.Properties))
	for n := range s.Properties {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		child := s.Properties[n]
		p := n
		if path != "" {
			p = path + "." + n
		}
		add(p, &child)
	}
	if s.Items != nil && s.Items.Schema != nil {
		add(path+"[]", s.Items.Schema)
	}
	if s.AdditionalProperties != nil && s.AdditionalProperties.Schema != nil {
		add(path+"{}", s.AdditionalProperties.Schema)
	}
}

// deprecatedText reports whether a description has a paragraph that starts
// "Deprecated:", the Go convention (go.dev/wiki/Deprecated).
func deprecatedText(description string) bool {
	for _, line := range strings.Split(description, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "Deprecated:") {
			return true
		}
	}
	return false
}

// removedFields lists the fields of released that current no longer has and
// that released did not mark deprecated, itself or through an enclosing
// field or its version. On a patch release (patch) every removed field is
// listed: a field is removed only in a minor release.
func removedFields(released, current map[string]crdField, patch bool) []string {
	var out []string
	for key := range released {
		if _, ok := current[key]; ok {
			continue
		}
		if !patch && deprecatedWithin(released, key) {
			continue
		}
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

// deprecatedWithin reports whether the field at key, or a field enclosing
// it, is deprecated in fields.
func deprecatedWithin(fields map[string]crdField, key string) bool {
	for k, seen := key, 0; seen <= len(fields); seen++ {
		f, ok := fields[k]
		if !ok {
			return false
		}
		if f.deprecated {
			return true
		}
		if f.parent == "" {
			return false
		}
		k = f.parent
	}
	return false
}

// releaseTag is a final release tag, vX.Y.Z.
var releaseTag = regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)$`)

// relVersion is a release's version.
type relVersion [3]int

func parseRelease(tag string) (relVersion, bool) {
	m := releaseTag.FindStringSubmatch(tag)
	if m == nil {
		return relVersion{}, false
	}
	var v relVersion
	for i := range v {
		v[i], _ = strconv.Atoi(m[i+1])
	}
	return v, true
}

func (v relVersion) less(w relVersion) bool {
	for i := range v {
		if v[i] != w[i] {
			return v[i] < w[i]
		}
	}
	return false
}

// baselineTag picks the release whose fields a tree must keep: the newest
// of tags, or on a branch of minor (X.Y, set) the newest that is not newer
// than X.Y. patch is whether that release is of minor itself: the tree is
// then a patch release of it.
func baselineTag(tags []string, minor *[2]int) (tag string, patch bool) {
	var best relVersion
	for _, t := range tags {
		v, ok := parseRelease(t)
		if !ok || (minor != nil && (v[0] > minor[0] || (v[0] == minor[0] && v[1] > minor[1]))) {
			continue
		}
		if tag == "" || best.less(v) {
			tag, best = t, v
		}
	}
	return tag, tag != "" && minor != nil && best[0] == minor[0] && best[1] == minor[1]
}

// treeMinor is the minor release this tree belongs to, and whether that
// makes it a maintenance branch (a removal there is a patch release's): CI's
// branch (GITHUB_BASE_REF for a pull request, else GITHUB_REF_NAME) or the
// local branch when it is release/vX.Y; locally otherwise the minor of the
// newest tag HEAD descends from, so a detached checkout of a release branch
// is not held to a newer minor's fields. nil: any release (main, in CI).
func treeMinor(root string) (minor *[2]int, branch bool) {
	ref := os.Getenv("GITHUB_BASE_REF")
	if ref == "" {
		ref = os.Getenv("GITHUB_REF_NAME")
	}
	ci := os.Getenv("CI") != ""
	if ref == "" && !ci {
		out, _ := exec.Command("git", "-C", root, "rev-parse", "--abbrev-ref", "HEAD").Output()
		ref = strings.TrimSpace(string(out))
	}
	if m := regexp.MustCompile(`^release/v(\d+)\.(\d+)$`).FindStringSubmatch(ref); m != nil {
		x, _ := strconv.Atoi(m[1])
		y, _ := strconv.Atoi(m[2])
		return &[2]int{x, y}, true
	}
	if ci {
		return nil, false
	}
	out, err := exec.Command("git", "-C", root, "describe", "--tags", "--abbrev=0", "--match", "v[0-9]*", "HEAD").Output()
	if err != nil {
		return nil, false
	}
	if v, ok := parseRelease(strings.TrimSpace(string(out))); ok {
		return &[2]int{v[0], v[1]}, false
	}
	return nil, false
}

// crdsAt reads the CRDs of crdDir at a git ref.
func crdsAt(t *testing.T, root, ref string) map[string][]byte {
	t.Helper()
	out, err := exec.Command("git", "-C", root, "ls-tree", "--name-only", ref, "--", crdDir+"/").Output()
	require.NoError(t, err, "list %s at %s", crdDir, ref)
	files := map[string][]byte{}
	for _, f := range strings.Fields(string(out)) {
		if !strings.HasSuffix(f, ".yaml") {
			continue
		}
		raw, err := exec.Command("git", "-C", root, "show", ref+":"+f).Output()
		require.NoError(t, err, "read %s at %s", f, ref)
		files[filepath.Base(f)] = raw
	}
	require.NotEmpty(t, files, "no CRD in %s at %s", crdDir, ref)
	return files
}

// crdsInTree reads the CRDs of crdDir in the working tree.
func crdsInTree(t *testing.T, root string) map[string][]byte {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(root, crdDir, "*.yaml"))
	require.NoError(t, err)
	files := map[string][]byte{}
	for _, p := range paths {
		raw, err := os.ReadFile(p)
		require.NoError(t, err)
		files[filepath.Base(p)] = raw
	}
	require.NotEmpty(t, files, "no CRD in %s", crdDir)
	return files
}

// TestReleasedCRDFieldsKept (#1996): every field of the newest release's
// CRDs is still in this tree's, unless that release marked it deprecated: a
// "Deprecated:" paragraph in its description (its Go doc comment), or in a
// field that encloses it, or its version marked deprecated. On a release
// branch whose minor is released no field is removed at all
// (docs/api-stability.md#deprecation-policy).
//
// The newest release is the newest vX.Y.Z tag; on release/vX.Y, the newest
// not newer than vX.Y. It skips without the tags outside CI and on a tag's
// own run (release.yml: the tag is the release); in CI a run without them
// fails: ci.yml fetches them for it.
func TestReleasedCRDFieldsKept(t *testing.T) {
	if os.Getenv("GITHUB_REF_TYPE") == "tag" {
		t.Skip("a release tag's run: the tag is the newest release itself")
	}
	root := repoRoot(t)
	minor, branch := treeMinor(root)
	out, err := exec.Command("git", "-C", root, "tag", "--list", "v*").Output()
	require.NoError(t, err)
	tag, patch := baselineTag(strings.Fields(string(out)), minor)
	patch = patch && branch
	if tag == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("no vX.Y.Z release tag: ci.yml must fetch the release tags before go test " +
				"(git fetch --no-tags --depth=1 origin '+refs/tags/v*:refs/tags/v*'), or this guard cannot run")
		}
		t.Skip("no vX.Y.Z release tag in this clone (git fetch --tags)")
	}
	released, err := crdFields(crdsAt(t, root, tag))
	require.NoError(t, err)
	current, err := crdFields(crdsInTree(t, root))
	require.NoError(t, err)
	gone := removedFields(released, current, patch)
	if patch {
		assert.Empty(t, gone, "fields of %s's CRDs are gone from this %s.x maintenance branch; a patch release "+
			"removes no field, deprecated or not (docs/api-stability.md#deprecation-policy):\n  %s",
			tag, strings.Join(strings.Split(tag, ".")[:2], "."), strings.Join(gone, "\n  "))
		return
	}
	assert.Empty(t, gone, "fields of %s's CRDs are gone without a release that marked them deprecated. Restore "+
		"them; to remove a field, a release first marks it deprecated (a \"Deprecated:\" paragraph in its Go doc "+
		"comment, a changelog.d fragment in section Deprecated) and a later minor removes it "+
		"(docs/api-stability.md#deprecation-policy):\n  %s", tag, strings.Join(gone, "\n  "))
}

// TestRemovedFieldsRule: removedFields lists a removed field unless the
// release marked it, an enclosing field or its version deprecated, and on a
// patch release lists every removed field.
func TestRemovedFieldsRule(t *testing.T) {
	crd := func(kind string, versions ...string) []byte {
		return []byte("apiVersion: apiextensions.k8s.io/v1\nkind: CustomResourceDefinition\n" +
			"spec:\n  names:\n    kind: " + kind + "\n  versions:\n" + strings.Join(versions, ""))
	}
	version := func(name string, deprecated bool, spec string) string {
		return "  - name: " + name + "\n    deprecated: " + strconv.FormatBool(deprecated) + "\n" +
			"    schema:\n      openAPIV3Schema:\n        type: object\n        properties:\n" +
			"          spec:\n            type: object\n            properties:\n" + spec
	}
	field := func(name, description string) string {
		return "              " + name + ":\n                type: string\n" +
			"                description: " + strconv.Quote(description) + "\n"
	}
	list := func(name, description, item string) string {
		return "              " + name + ":\n                type: array\n" +
			"                description: " + strconv.Quote(description) + "\n" +
			"                items:\n                  type: object\n                  properties:\n" +
			"                    " + item + ":\n                      type: string\n"
	}
	mapOf := func(name, value string) string {
		return "              " + name + ":\n                type: object\n" +
			"                additionalProperties:\n                  type: object\n                  properties:\n" +
			"                    " + value + ":\n                      type: string\n"
	}
	fields := func(files map[string][]byte) map[string]crdField {
		f, err := crdFields(files)
		require.NoError(t, err)
		return f
	}
	released := fields(map[string][]byte{
		"pipelines.yaml": crd("Pipeline", version("v1alpha1", false,
			field("kept", "Kept.")+
				field("plain", "Plain is a field.")+
				field("marked", "Marked is ignored.\n\nDeprecated: remove it. It is removed in v0.2.")+
				field("mentions", "Mentions is not Deprecated: the word is mid-line.")+
				list("old", "Old is ignored.\n\nDeprecated: use kept.", "name")+
				list("items", "Items stay.", "gone"))),
		"gates.yaml":    crd("PolicyGate", version("v1alpha1", false, field("rule", "Rule.")+mapOf("labels", "x"))),
		"clocks.yaml":   crd("ScheduleClock", version("v1alpha1", true, field("tick", "Tick."))),
		"hooks.yaml":    crd("Hook", version("v1alpha1", false, field("url", "URL."))),
		"versions.yaml": crd("Run", version("v1alpha1", false, field("a", "A.")), version("v1beta1", false, field("a", "A."))),
	})
	current := fields(map[string][]byte{
		"pipelines.yaml": crd("Pipeline", version("v1alpha1", false, field("kept", "Kept.")+field("added", "New.")+
			list("items", "Items stay.", "other"))),
		"gates.yaml":    crd("PolicyGate", version("v1alpha1", false, field("rule", "Rule.")+field("labels", "Labels."))),
		"hooks.yaml":    crd("Hook", version("v1alpha1", false, field("url", "URL."))),
		"versions.yaml": crd("Run", version("v1beta1", false, field("a", "A."))),
	})
	assert.Equal(t, []string{
		"Pipeline v1alpha1 spec.items[].gone", // an item's field
		"Pipeline v1alpha1 spec.mentions",     // "Deprecated:" mid-line is no mark
		"Pipeline v1alpha1 spec.plain",
		"PolicyGate v1alpha1 spec.labels{}", "PolicyGate v1alpha1 spec.labels{}.x", // a map's values
		"Run v1alpha1", "Run v1alpha1 spec", "Run v1alpha1 spec.a", // a version removed
	}, removedFields(released, current, false),
		"marked, old and old[].name (under a deprecated list) and the deprecated ScheduleClock version may go")
	assert.Contains(t, removedFields(released, current, true), "Pipeline v1alpha1 spec.marked",
		"a patch release removes no field, deprecated or not")
	assert.Contains(t, removedFields(released, current, true), "ScheduleClock v1alpha1 spec.tick")
	assert.Empty(t, removedFields(current, current, true), "no field removed")
	assert.Subset(t, removedFields(released, fields(map[string][]byte{"other.yaml": crd("Other", version("v1alpha1", false, ""))}), false),
		[]string{"PolicyGate v1alpha1", "PolicyGate v1alpha1 spec", "PolicyGate v1alpha1 spec.rule"}, "a kind removed is every field of it")
}

// TestReleasedCRDFieldsBaseline: the release a tree is held to.
func TestReleasedCRDFieldsBaseline(t *testing.T) {
	tags := []string{"v0.9.0", "v0.10.0", "v0.12.0", "v0.12.3", "v0.13.0-rc.1", "v0.13.0", "v0.13.1", "v0.14.0-rc.0", "latest"}
	for _, tc := range []struct {
		name  string
		minor *[2]int
		tag   string
		patch bool
	}{
		{"main: the newest final release", nil, "v0.13.1", false},
		{"release/v0.12: its newest patch", &[2]int{0, 12}, "v0.12.3", true},
		{"release/v0.14 before v0.14.0: the minor before it", &[2]int{0, 14}, "v0.13.1", false},
		{"release/v0.10: versions sort as numbers", &[2]int{0, 10}, "v0.10.0", true},
		{"before any release", &[2]int{0, 8}, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tag, patch := baselineTag(tags, tc.minor)
			assert.Equal(t, tc.tag, tag)
			assert.Equal(t, tc.patch, patch)
		})
	}
}

// TestRemovedFieldsRuleOnHistory: the one minor that removed fields so far,
// v0.11.0, removed only what v0.10.0 marked deprecated (the guard would have
// let it through), and without those marks every one of them is reported.
func TestRemovedFieldsRuleOnHistory(t *testing.T) {
	root := repoRoot(t)
	for _, tag := range []string{"v0.10.0", "v0.11.0"} {
		if exec.Command("git", "-C", root, "rev-parse", "--verify", "--quiet", "refs/tags/"+tag).Run() != nil {
			t.Skipf("needs the tag %s", tag)
		}
	}
	released, err := crdFields(crdsAt(t, root, "v0.10.0"))
	require.NoError(t, err)
	next, err := crdFields(crdsAt(t, root, "v0.11.0"))
	require.NoError(t, err)
	assert.Empty(t, removedFields(released, next, false), "v0.11.0 removed only v0.10.0's deprecated fields")
	unmarked := map[string]crdField{}
	for k, f := range released {
		unmarked[k] = crdField{parent: f.parent}
	}
	gone := removedFields(unmarked, next, false)
	assert.Len(t, gone, 34, "v0.11.0 removed 34 fields of v0.10.0's CRDs: %v", gone)
	assert.Contains(t, gone, "Pipeline v1alpha1 spec.policyGates")
	assert.Contains(t, gone, "NotificationHook v1alpha1 spec.webhook.authorizationHeader")
}

// apiStabilityDoc lists every kind's stability level (#1996).
const apiStabilityDoc = "docs/api-stability.md"

// TestAPIStabilityListsEveryKind: docs/api-stability.md's table has one row
// per kind of config/crd/bases, each with a level, and no row for a kind
// that has no CRD, so a new kind cannot ship without a stated level.
func TestAPIStabilityListsEveryKind(t *testing.T) {
	root := repoRoot(t)
	kinds := map[string]bool{}
	for _, raw := range crdsInTree(t, root) {
		var crd apiextv1.CustomResourceDefinition
		require.NoError(t, yaml.Unmarshal(raw, &crd))
		kinds[crd.Spec.Names.Kind] = true
	}
	doc, err := os.ReadFile(filepath.Join(root, apiStabilityDoc))
	require.NoError(t, err)
	row := regexp.MustCompile(`(?m)^\| \[(\w+)\]\(reference/api\.md#(\w+)\) \|[^|\n]*\|[^|\n]*\| (\w+) \|`)
	rows := map[string]bool{}
	for _, m := range row.FindAllStringSubmatch(string(doc), -1) {
		kind, anchor, level := m[1], m[2], m[3]
		assert.False(t, rows[kind], "%s: two rows for %s", apiStabilityDoc, kind)
		rows[kind] = true
		assert.True(t, kinds[kind], "%s: a row for %s, which has no CRD in %s", apiStabilityDoc, kind, crdDir)
		assert.Equal(t, strings.ToLower(kind), anchor, "%s: %s links to the API reference's #%s", apiStabilityDoc, kind, anchor)
		assert.Contains(t, []string{"Settled", "Evolving", "New"}, level, "%s: %s's level", apiStabilityDoc, kind)
	}
	for kind := range kinds {
		assert.True(t, rows[kind], "%s: no row for %s: give the new kind a level (New for a kind this release adds)", apiStabilityDoc, kind)
	}
}
