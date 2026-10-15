// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package examples

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	structuralschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	sigyaml "sigs.k8s.io/yaml"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// The docs name kardinal's CRD fields two ways: in YAML blocks that show a
// whole object, and inline as a path (`spec.environments[].bake.minutes`).
// These tests check both against config/crd/bases, so a renamed or removed
// field cannot stay in the docs.

// markdownYAML returns the YAML code blocks of one Markdown file, with the
// line each starts on. Indented blocks (in list items) are dedented.
func markdownYAML(text string) (blocks []string, lines []int) {
	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	in, indent := false, ""
	var cur []string
	n, start := 0, 0
	for sc.Scan() {
		n++
		raw := sc.Text()
		trimmed := strings.TrimSpace(raw)
		switch {
		case !in && (trimmed == "```yaml" || trimmed == "```yml"):
			in, indent, cur, start = true, raw[:len(raw)-len(strings.TrimLeft(raw, " "))], nil, n+1
		case in && trimmed == "```":
			in = false
			blocks = append(blocks, strings.Join(cur, "\n"))
			lines = append(lines, start)
		case in:
			cur = append(cur, strings.TrimPrefix(raw, indent))
		}
	}
	return blocks, lines
}

// TestDocumentedManifestsHaveNoUnknownFields prunes every whole kardinal.io
// object in the docs' YAML blocks (one with apiVersion kardinal.io/... and a
// kind) with its CRD's schema, as the API server would: a field the schema
// does not have would be dropped, so the setting the page shows would do
// nothing. Values are not validated: pages write placeholders.
func TestDocumentedManifestsHaveNoUnknownFields(t *testing.T) {
	schemas := loadCRDSchemas(t)
	root := repoRoot(t)
	checked := 0
	for _, rel := range documentedMarkdown(t) {
		data, err := os.ReadFile(filepath.Join(root, rel))
		require.NoError(t, err)
		blocks, starts := markdownYAML(string(data))
		for i, block := range blocks {
			for j, part := range strings.Split(block, "\n---") {
				var obj map[string]interface{}
				if sigyaml.Unmarshal([]byte(part), &obj) != nil || obj == nil {
					continue // a fragment, or not YAML on its own
				}
				if !strings.HasPrefix(str(obj, "apiVersion"), v1alpha1.GroupVersion.Group+"/") || str(obj, "kind") == "" {
					continue
				}
				s, ok := schemas[str(obj, "kind")]
				if !ok {
					continue // a planned kind on a page that says so
				}
				checked++
				where := fmt.Sprintf("%s:%d#%d %s", rel, starts[i], j, str(obj, "kind"))
				t.Run(where, func(t *testing.T) {
					var unknown []string
					for _, p := range schemaProblems(t, s, obj) {
						if strings.HasPrefix(p, "unknown field") {
							unknown = append(unknown, p)
						}
					}
					assert.Empty(t, unknown)
				})
			}
		}
	}
	assert.Greater(t, checked, 50, "too few kardinal.io objects found in the docs; is the scan broken?")
}

// fieldPath is an inline code span that is a field path of an object's spec
// or status: `spec.git.secretRef`, `.status.environments[].soakMinutes`.
var fieldPath = regexp.MustCompile("`\\.?((?:spec|status)(?:\\.[A-Za-z0-9_]+|\\[[^\\]`]*\\])+)`")

// foreignFieldPaths are spec and status paths of objects that are not
// kardinal's (Argo CD, Flux, Argo Rollouts, Flagger, Kubernetes, kro, Kargo)
// or of kardinal fields that were removed, which the pages name on purpose.
// A path is listed with what it belongs to.
var foreignFieldPaths = map[string]string{
	"status.abort":                        "Argo Rollouts Rollout",
	"status.audiences":                    "TokenReview",
	"status.expirationTimestamp":          "TokenRequest",
	"spec.chart.spec.version":             "Flux HelmRelease",
	"spec.healthChecks":                   "Flux Kustomization",
	"spec.kubeConfig":                     "Flux Kustomization",
	"spec.kubeConfig.secretRef":           "Flux Kustomization",
	"spec.sourceRef":                      "Flux Kustomization",
	"spec.destination.namespace":          "Argo CD Application",
	"status.inventory":                    "Flux Kustomization",
	"status.lastAppliedRevision":          "Flux Kustomization",
	"spec.policyGates":                    "the v0.8.1 Graph (docs/upgrade.md)",
	"spec.selector":                       "the v0.8.1 Graph (docs/upgrade.md)",
	"spec.serviceAccountName":             "kro Graph",
	"spec.shard":                          "Kargo Stage",
	"spec.source.helm.valuesObject":       "Argo CD Application",
	"spec.source.path":                    "Argo CD Application",
	"spec.source.targetRevision":          "Argo CD Application",
	"status.health.status":                "Argo CD Application",
	"status.history":                      "Argo CD Application",
	"status.operationState.phase":         "Argo CD Application",
	"status.summary.images":               "Argo CD Application",
	"status.sync.revision":                "Argo CD Application",
	"status.sync.status":                  "Argo CD Application",
	"spec.targetRef":                      "Flagger Canary",
	"status.lastTransitionTime":           "Flagger Canary",
	"spec.workloadRef":                    "Argo Rollouts Rollout",
	"status.currentPodHash":               "Argo Rollouts Rollout",
	"status.stableRS":                     "Argo Rollouts Rollout",
	"spec.terminate":                      "Argo Rollouts AnalysisRun",
	"spec.template.spec.containers.image": "Kubernetes Deployment",
	"spec.environments.steps":             "removed in v0.11.0 (docs/argocd-native-promotion.md says it is refused)",
	"spec.webhook.authorizationHeader":    "removed in v0.11.0 (docs/upgrade.md)",
}

// foreign returns the foreignFieldPaths entry path is, or is under ("" if
// none): `spec.source.helm.valuesObject.image.tag` is under the Argo CD
// Application's spec.source.helm.valuesObject.
func foreign(path string) string {
	best := ""
	for p := range foreignFieldPaths {
		if (path == p || strings.HasPrefix(path, p+".")) && len(p) > len(best) {
			best = p // the longest entry, so the result does not depend on map order
		}
	}
	return best
}

// schemaHas reports whether path (dot-separated, list indexes removed) is a
// field of s: under properties, list items, additionalProperties (any key),
// or an object that keeps unknown fields.
func schemaHas(s *structuralschema.Structural, path []string) bool {
	for _, seg := range path {
		if s == nil {
			return false
		}
		if s.XPreserveUnknownFields {
			return true
		}
		for s.Items != nil {
			s = s.Items
		}
		if p, ok := s.Properties[seg]; ok {
			s = &p
			continue
		}
		if s.AdditionalProperties != nil && (s.AdditionalProperties.Bool || s.AdditionalProperties.Structural != nil) {
			s = s.AdditionalProperties.Structural
			if s == nil {
				return true
			}
			continue
		}
		return false
	}
	return true
}

// TestDocumentedFieldPathsExist checks that every spec or status path the
// docs name inline is a field of a kardinal CRD (any kind: a page names the
// path without the kind), unless foreignFieldPaths lists it.
func TestDocumentedFieldPathsExist(t *testing.T) {
	schemas := loadCRDSchemas(t)
	root := repoRoot(t)
	used := map[string]bool{}
	checked := 0
	for _, rel := range documentedMarkdown(t) {
		data, err := os.ReadFile(filepath.Join(root, rel))
		require.NoError(t, err)
		for i, line := range strings.Split(string(data), "\n") {
			for _, m := range fieldPath.FindAllStringSubmatch(line, -1) {
				path := regexp.MustCompile(`\[[^\]]*\]`).ReplaceAllString(m[1], "")
				if f := foreign(path); f != "" {
					used[f] = true
					continue
				}
				checked++
				found := false
				for _, s := range schemas {
					if schemaHas(s.structural, strings.Split(path, ".")) {
						found = true
						break
					}
				}
				assert.True(t, found, "%s:%d: `%s` is not a field of any kardinal CRD (if it is another tool's, add it to foreignFieldPaths)", rel, i+1, m[1])
			}
		}
	}
	assert.Greater(t, checked, 300, "too few field paths found in the docs; is the scan broken?")
	for path, what := range foreignFieldPaths {
		assert.True(t, used[path], "foreignFieldPaths lists %s (%s), which no page names any more: remove it", path, what)
	}
}

func TestSchemaHas(t *testing.T) {
	s := loadCRDSchemas(t)["Pipeline"].structural
	for path, want := range map[string]bool{
		"spec.environments.bake.minutes": true,
		"spec.git.secretRef.name":        true,
		"spec.environments.nope":         false,
		"status.environments":            true,
		"spec.nope":                      false,
	} {
		assert.Equal(t, want, schemaHas(s, strings.Split(path, ".")), path)
	}
}
