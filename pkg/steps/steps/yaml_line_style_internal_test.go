// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package steps

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	parentsteps "github.com/kardinal-promoter/kardinal-promoter/pkg/steps"
)

// TestYAMLUpdate_KeepsLineStyle (#2182): yaml-update writes a file with its
// own line endings and byte order mark, in place (the splice) and when it
// re-encodes the whole document (here a flow mapping, which the splice
// does not edit): a CRLF file stays CRLF, a leading BOM stays. In each case
// the result is exactly the edit of the same file in LF without a BOM, in
// the file's style. Mixed line endings: TestYAMLUpdate_MixedLineEndings.
//
// Covers STEP-YAML-STYLE-01.
func TestYAMLUpdate_KeepsLineStyle(t *testing.T) {
	const bom = "\ufeff"
	edit := func(t *testing.T, doc, path string) string {
		t.Helper()
		dir := t.TempDir()
		envDir := filepath.Join(dir, "environments", "prod")
		require.NoError(t, os.MkdirAll(envDir, 0o755))
		file := filepath.Join(envDir, "values.yaml")
		require.NoError(t, os.WriteFile(file, []byte(doc), 0o644))
		state := &parentsteps.StepState{
			WorkDir: dir,
			Environment: v1alpha1.EnvironmentSpec{Name: "prod", Update: v1alpha1.UpdateConfig{Strategy: "yaml",
				YAML: &v1alpha1.YAMLUpdateConfig{Updates: []v1alpha1.YAMLUpdate{{File: "values.yaml", Path: path}}}}},
			Bundle:  v1alpha1.BundleSpec{Images: []v1alpha1.ImageRef{{Repository: "ghcr.io/org/app", Tag: "2.0.0"}}},
			Outputs: map[string]string{},
		}
		res, got := runEditor(t, &yamlUpdateStep{}, state, file, []byte(doc))
		require.Equal(t, parentsteps.StepSuccess, res.Status, res.Message)
		return string(got)
	}
	crlf := func(s string) string { return strings.ReplaceAll(s, "\n", "\r\n") }
	const block = "image:\n  tag: \"1.0.0\" # pinned\n  repository: ghcr.io/org/app\nreplicas: 2\nnote: |\n  two\n  lines\n"
	const flow = "image: {tag: '1.0.0', pull: Always}\nreplicas: 2\n"
	const wide = "image:\n    tag: \"1.0.0\"\n    repository: ghcr.io/org/app\nother:\n    deep:   1    # spaced\n"
	for _, tc := range []struct {
		name, doc, lf, path string
		wantCRLF, wantBOM   bool
	}{
		{name: "CRLF, spliced", doc: crlf(block), lf: block, path: "image.tag", wantCRLF: true},
		// Spliced in place, not re-encoded: the 4-space indentation, which
		// a re-encode writes with 2, stays as it was.
		{name: "CRLF, 4-space indentation kept", doc: crlf(wide), lf: wide, path: "image.tag", wantCRLF: true},
		{name: "CRLF, re-encoded", doc: crlf(flow), lf: flow, path: "image.tag", wantCRLF: true},
		{name: "a BOM, spliced", doc: bom + block, lf: block, path: "image.tag", wantBOM: true},
		{name: "a BOM, re-encoded", doc: bom + flow, lf: flow, path: "image.tag", wantBOM: true},
		{name: "a BOM and CRLF", doc: bom + crlf(block), lf: block, path: "image.tag", wantBOM: true, wantCRLF: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := edit(t, tc.doc, tc.path)
			want := edit(t, tc.lf, tc.path)
			if tc.wantCRLF {
				want = crlf(want)
			}
			if tc.wantBOM {
				want = bom + want
			}
			assert.Equal(t, want, got)
			assert.Equal(t, tc.wantBOM, strings.HasPrefix(got, bom), "the byte order mark")
			if tc.wantCRLF {
				assert.Equal(t, strings.Count(got, "\n"), strings.Count(got, "\r\n"), "every line ends with CRLF:\n%q", got)
			} else {
				assert.NotContains(t, got, "\r", "every line ends with LF:\n%q", got)
			}
			assert.Equal(t, got, edit(t, got, tc.path), "idempotent")
			if tc.lf == wide {
				assert.Contains(t, got, "other:\r\n    deep:   1    # spaced\r\n", "the other lines are byte for byte as they were")
			}
		})
	}
}

// TestYAMLUpdate_MixedLineEndings (#2182, QA-3): in a file with mixed line
// endings every line the edit does not touch keeps its own ending, and the
// lines the edit writes end with the dominant one (the one more lines end
// with; LF on a tie).
//
// Covers STEP-YAML-STYLE-01.
func TestYAMLUpdate_MixedLineEndings(t *testing.T) {
	edit := func(t *testing.T, doc, path string) string {
		t.Helper()
		dir := t.TempDir()
		envDir := filepath.Join(dir, "environments", "prod")
		require.NoError(t, os.MkdirAll(envDir, 0o755))
		file := filepath.Join(envDir, "values.yaml")
		require.NoError(t, os.WriteFile(file, []byte(doc), 0o644))
		state := &parentsteps.StepState{
			WorkDir: dir,
			Environment: v1alpha1.EnvironmentSpec{Name: "prod", Update: v1alpha1.UpdateConfig{Strategy: "yaml",
				YAML: &v1alpha1.YAMLUpdateConfig{Updates: []v1alpha1.YAMLUpdate{{File: "values.yaml", Path: path}}}}},
			Bundle:  v1alpha1.BundleSpec{Images: []v1alpha1.ImageRef{{Repository: "ghcr.io/org/app", Tag: "2.0.0"}}},
			Outputs: map[string]string{},
		}
		res, got := runEditor(t, &yamlUpdateStep{}, state, file, []byte(doc))
		require.Equal(t, parentsteps.StepSuccess, res.Status, res.Message)
		return string(got)
	}
	for _, tc := range []struct {
		name, doc, path, want string
	}{
		{name: "mostly CRLF, an LF line kept",
			doc:  "a: 1\r\nimage:\r\n  tag: \"1.0.0\"\r\nlast: x\n",
			path: "image.tag", want: "a: 1\r\nimage:\r\n  tag: \"2.0.0\"\r\nlast: x\n"},
		{name: "mostly LF, a CRLF line kept",
			doc:  "a: 1\r\nimage:\n  tag: \"1.0.0\"\nlast: x\n",
			path: "image.tag", want: "a: 1\r\nimage:\n  tag: \"2.0.0\"\nlast: x\n"},
		{name: "a new line takes the dominant ending",
			doc:  "a: 1\r\nb: 2\r\nlast: x\n",
			path: "image.tag", want: "a: 1\r\nb: 2\r\nlast: x\nimage:\r\n  tag: 2.0.0\r\n"},
		{name: "the edited line takes the dominant ending",
			doc:  "a: 1\nimage:\n  tag: \"1.0.0\"\r\nlast: x\n",
			path: "image.tag", want: "a: 1\nimage:\n  tag: \"2.0.0\"\nlast: x\n"},
		// yaml-update ends the file with a newline, as for an LF file; it
		// takes the dominant ending.
		{name: "no newline at the end: the one added is the dominant one",
			doc:  "image:\r\n  tag: \"1.0.0\"\r\nlast: x",
			path: "image.tag", want: "image:\r\n  tag: \"2.0.0\"\r\nlast: x\r\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := edit(t, tc.doc, tc.path)
			assert.Equal(t, tc.want, got)
			assert.Equal(t, got, edit(t, got, tc.path), "idempotent")
		})
	}
}
