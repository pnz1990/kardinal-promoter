// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

// TestAPIReferenceIsUpToDate fails when docs/reference/api.md does not match
// the CRDs in config/crd/bases.
func TestAPIReferenceIsUpToDate(t *testing.T) {
	want, err := render("../../config/crd/bases")
	require.NoError(t, err)
	got, err := os.ReadFile("../../docs/reference/api.md")
	require.NoError(t, err)
	if line, w, g := firstDiff(string(want), string(got)); line > 0 {
		t.Errorf("docs/reference/api.md is stale; run: make api-docs\nfirst difference at line %d:\n  generated: %s\n  committed: %s", line, w, g)
	}
}

// firstDiff returns the first line (1-based) where a and b differ, or 0.
func firstDiff(a, b string) (int, string, string) {
	al, bl := strings.Split(a, "\n"), strings.Split(b, "\n")
	for i := 0; i < len(al) || i < len(bl); i++ {
		var x, y string
		if i < len(al) {
			x = al[i]
		}
		if i < len(bl) {
			y = bl[i]
		}
		if x != y || i >= len(al) || i >= len(bl) {
			return i + 1, x, y
		}
	}
	return 0, "", ""
}

func TestAPIReferenceListsEveryField(t *testing.T) {
	page, err := render("../../config/crd/bases")
	require.NoError(t, err)
	for _, row := range []string{
		"| `spec.environments[].approval` | string |  |",
		"| `spec.expression` | string | yes |",
		"| `status.conditions[].type` | string | yes |",
	} {
		assert.Contains(t, string(page), row)
	}
	assert.NotContains(t, string(page), "`apiVersion`", "standard object fields are not listed")
}

func TestTypeNameAndCell(t *testing.T) {
	str := apiextv1.JSONSchemaProps{Type: "string"}
	assert.Equal(t, "[]string", typeName(apiextv1.JSONSchemaProps{Type: "array", Items: &apiextv1.JSONSchemaPropsOrArray{Schema: &str}}))
	assert.Equal(t, "map[string]string", typeName(apiextv1.JSONSchemaProps{Type: "object", AdditionalProperties: &apiextv1.JSONSchemaPropsOrBool{Schema: &str}}))
	assert.Equal(t, "int or string", typeName(apiextv1.JSONSchemaProps{XIntOrString: true}))
	assert.Equal(t, "string (date-time)", typeName(apiextv1.JSONSchemaProps{Type: "string", Format: "date-time"}))
	assert.Equal(t, "any", typeName(apiextv1.JSONSchemaProps{}))

	s := apiextv1.JSONSchemaProps{
		Description: "Mode is a|b.\nSee <env>.",
		Enum:        []apiextv1.JSON{{Raw: []byte(`"a"`)}, {Raw: []byte(`"b"`)}},
		Default:     &apiextv1.JSON{Raw: []byte(`"a"`)},
	}
	assert.Equal(t, "Mode is a\\|b. See &lt;env&gt;. One of: `a`, `b`. Default: `a`.", cell(s))
	assert.False(t, strings.Contains(cell(s), "\n"))
}

func TestFirstDiff(t *testing.T) {
	line, _, _ := firstDiff("a\nb\n", "a\nb\n")
	assert.Zero(t, line)
	line, w, g := firstDiff("a\nb\n", "a\nc\n")
	assert.Equal(t, []interface{}{2, "b", "c"}, []interface{}{line, w, g})
	line, _, _ = firstDiff("a\nb", "a")
	assert.Equal(t, 2, line)
}
