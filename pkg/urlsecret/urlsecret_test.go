// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package urlsecret_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/urlsecret"
)

// Vector is one URL of testdata/vectors.json: whether every layer refuses
// it as a CI run URL (the Bundle API, the CRD, the UI's link), and what a
// receipt makes of it.
type Vector struct {
	URL      string `json:"url"`
	Refused  bool   `json:"refused"`
	Redacted string `json:"redacted"`
	Why      string `json:"why"`
}

// Vectors reads the table every layer must agree on.
func Vectors(t *testing.T) []Vector {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "vectors.json"))
	require.NoError(t, err)
	var v []Vector
	require.NoError(t, json.Unmarshal(raw, &v))
	require.NotEmpty(t, v)
	return v
}

// TestVectors (#2158): a URL is refused exactly when it has user info or a
// credential parameter, and RedactString leaves nothing of the credential
// (and changes nothing else), idempotently.
func TestVectors(t *testing.T) {
	for _, v := range Vectors(t) {
		t.Run(v.Why, func(t *testing.T) {
			assert.Equal(t, v.Refused, urlsecret.Has(v.URL) || urlsecret.HasUserInfo(v.URL), v.URL)
			got := urlsecret.RedactString(v.URL)
			assert.Equal(t, v.Redacted, got)
			assert.Equal(t, got, urlsecret.RedactString(got), "idempotent")
			assert.NotContains(t, got, "S3CRET")
			assert.NotContains(t, got, "AKIA")
		})
	}
}

func TestIsParam(t *testing.T) {
	for name, want := range map[string]bool{
		"token": true, "Access-Token": true, "ACCESS_TOKEN": true, "circle-token": true, "%74oken": true,
		"x-goog-credential": true, "aws_access_key_id": true, "secret_key": true, "_sig": true,
		"tokens": false, "monkey": false, "keyboard": false, "attempt": false, "check_suite_focus": false,
		"tokenize": false, "codes": false,
	} {
		assert.Equal(t, want, urlsecret.IsParam(name), name)
	}
}

// TestRedactString_KnownTokens: tokens known by their shape go wherever they
// are; a commit SHA or an image digest is not one.
func TestRedactString_KnownTokens(t *testing.T) {
	for in, want := range map[string]string{
		"built by ghp_0123456789abcdefghijABCDEFGHIJ for alice":        "built by REDACTED for alice",
		"token glpat-0123456789abcdefghij-x":                           "token REDACTED",
		"key AKIAIOSFODNN7EXAMPLE used":                                "key REDACTED used",
		"github_pat_11ABCDEFG0123456789_abcdefghijklmnopqrstuvwxyz":    "REDACTED",
		"commit 3d1e5f0c9a8b7c6d5e4f3a2b1c0d9e8f7a6b5c4d":              "commit 3d1e5f0c9a8b7c6d5e4f3a2b1c0d9e8f7a6b5c4d",
		"ghcr.io/acme/app@sha256:9f2c00000000000000000000000000000001": "ghcr.io/acme/app@sha256:9f2c00000000000000000000000000000001",
	} {
		assert.Equal(t, want, urlsecret.RedactString(in), in)
	}
}

// ciRunURLRules are the rules on spec.provenance.ciRunURL of a Bundle CRD
// file.
func ciRunURLRules(t *testing.T, file string) []string {
	t.Helper()
	raw, err := os.ReadFile(file)
	require.NoError(t, err)
	var crd struct {
		Spec struct {
			Versions []struct {
				Schema struct {
					OpenAPIV3Schema struct {
						Properties map[string]struct {
							Properties map[string]struct {
								Properties map[string]struct {
									Validations []struct {
										Rule string `json:"rule"`
									} `json:"x-kubernetes-validations"`
								} `json:"properties"`
							} `json:"properties"`
						} `json:"properties"`
					} `json:"openAPIV3Schema"`
				} `json:"schema"`
			} `json:"versions"`
		} `json:"spec"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &crd))
	require.NotEmpty(t, crd.Spec.Versions)
	var out []string
	for _, r := range crd.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties["spec"].Properties["provenance"].Properties["ciRunURL"].Validations {
		out = append(out, r.Rule)
	}
	return out
}

// TestCRDRules: the Bundle CRD's ciRunURL rules (both copies) are exactly
// CRDRules, the rule this package decides.
func TestCRDRules(t *testing.T) {
	want := urlsecret.CRDRules()
	sort.Strings(want)
	for _, f := range []string{
		filepath.Join("..", "..", "config", "crd", "bases", "kardinal.io_bundles.yaml"),
		filepath.Join("..", "..", "chart", "kardinal-promoter", "crds", "kardinal.io_bundles.yaml"),
	} {
		got := ciRunURLRules(t, f)
		sort.Strings(got)
		assert.Equal(t, want, got, "%s: regenerate the marker in api/v1alpha1/bundle_types.go from urlsecret.CRDRules", f)
	}
}

// TestWebMirrorsTheRule: web/src/prLink.ts names the same suffixes and
// exact names.
func TestWebMirrorsTheRule(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "web", "src", "prLink.ts"))
	require.NoError(t, err)
	list := func(name string) []string {
		m := regexp.MustCompile(`const ` + name + ` = \[([^\]]*)\]`).FindSubmatch(raw)
		require.NotNil(t, m, "prLink.ts has %s", name)
		var out []string
		for _, q := range regexp.MustCompile(`'([^']*)'`).FindAllSubmatch(m[1], -1) {
			out = append(out, string(q[1]))
		}
		return out
	}
	assert.Equal(t, urlsecret.Suffixes, list("CREDENTIAL_SUFFIXES"))
	assert.Equal(t, urlsecret.Exact, list("CREDENTIAL_EXACT"))
	tests, err := os.ReadFile(filepath.Join("..", "..", "web", "src", "prLink.test.ts"))
	require.NoError(t, err)
	assert.True(t, strings.Contains(string(tests), "pkg/urlsecret/testdata/vectors.json"), "the web tests run the vectors")
}
