// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package steps

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	yaml "go.yaml.in/yaml/v3"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	parentsteps "github.com/kardinal-promoter/kardinal-promoter/pkg/steps"
)

// styledValues are a value written in every scalar style the editors meet:
// plain, quoted, literal and folded with each chomping indicator and an
// explicit indentation, and block scalars whose last lines are more-indented
// spaces (text, #1965), blank lines and comments.
var styledValues = []string{
	"plain",
	"'single quoted'",
	`"double \" quoted"`,
	"|\n  line one\n  line two",
	"|-\n  stripped",
	"|+\n  kept\n",
	"|2\n    two more spaces",
	">\n  folded\n  text",
	">-\n  folded strip",
	">+\n  folded keep\n",
	">\n 0\n  ",     // the #1965 crasher: a more-indented line of spaces is text
	">\n  a\n    ",  // two more spaces
	"|\n  a\n   \n", // a more-indented blank line, then an empty one
	"|\n  a\n  ",    // spaces at the indentation: a blank line, not text
	"|\n  a\n  # in the scalar",
	"|\n  a\n# a comment after",
	"|\n  a\n\n\n",
	">2-\n   explicit\n   ",
	"\"quoted over\n  # two lines, not a comment\"",
	"'single over\n  #two lines, it''s text'",
	"\"0\n#\"", // the second crasher: a quoted line that starts with #
}

// styledDocs are documents of styledValues: alone, before another key,
// nested one level, with comments around, before a document end marker and
// before a line of spaces and a tab.
func styledDocs() []string {
	indent := func(v, by string) string {
		lines := strings.Split(v, "\n")
		for i := 1; i < len(lines); i++ {
			if lines[i] != "" {
				lines[i] = by + lines[i]
			}
		}
		return strings.Join(lines, "\n")
	}
	var docs []string
	for _, v := range styledValues {
		docs = append(docs,
			fmt.Sprintf("a: %s\n", v),
			fmt.Sprintf("# head\na: %s\nb: 1 # tail\n", v),
			fmt.Sprintf("spec:\n  a: %s\n  b: x\nafter: y\n", indent(v, "  ")),
			fmt.Sprintf("list:\n- %s\nz: 2\n", indent(v, "  ")),
			fmt.Sprintf("a: %s\n...\n", v),
			fmt.Sprintf("a: %s\n \t\n", v))
	}
	// Some combinations are not YAML (a tab line after a quoted scalar):
	// the editors refuse those, which the fuzz properties check.
	valid := docs[:0]
	for _, d := range docs {
		var n yaml.Node
		if yaml.Unmarshal([]byte(d), &n) == nil {
			valid = append(valid, d)
		}
	}
	return valid
}

// TestYAMLEditors_KeepScalarStyles (#1965, #1907): kustomize-set-image and
// yaml-update, run on a document holding any scalar style, change only the
// entry they edit: every other byte stays, whatever the style, chomping,
// trailing spaces or comments of the values they do not own.
func TestYAMLEditors_KeepScalarStyles(t *testing.T) {
	for i, doc := range styledDocs() {
		t.Run(fmt.Sprintf("kustomize/%d", i), func(t *testing.T) {
			checkKustomizeProperty(t, doc, "ghcr.io/org/app", "1.2.3", "")
			got := runKustomizeOnce(t, doc)
			same, ok := onlyTopKeyChanged([]byte(doc), got, "images")
			require.True(t, ok && same, "only the images entry is added, byte for byte; doc:\n%q\nafter:\n%q", doc, got)
		})
		t.Run(fmt.Sprintf("yaml-update/%d", i), func(t *testing.T) {
			checkYAMLUpdateProperty(t, doc, "image.tag", "1.2.3")
		})
	}
}

// runKustomizeOnce runs kustomize-set-image on doc and returns the file.
func runKustomizeOnce(t *testing.T, doc string) []byte {
	t.Helper()
	dir := t.TempDir()
	envDir := filepath.Join(dir, "environments", "prod")
	require.NoError(t, os.MkdirAll(envDir, 0o755))
	file := filepath.Join(envDir, "kustomization.yaml")
	require.NoError(t, os.WriteFile(file, []byte(doc), 0o644))
	state := &parentsteps.StepState{
		WorkDir:     dir,
		Environment: v1alpha1.EnvironmentSpec{Name: "prod"},
		Bundle:      v1alpha1.BundleSpec{Images: []v1alpha1.ImageRef{{Repository: "ghcr.io/org/app", Tag: "1.2.3"}}},
		Outputs:     map[string]string{},
	}
	res, got := runEditor(t, &kustomizeSetImageStep{}, state, file, []byte(doc))
	require.Equal(t, parentsteps.StepSuccess, res.Status, res.Message)
	return got
}
