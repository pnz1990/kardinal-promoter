// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package urlsecret_test

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/urlsecret"
)

func TestHasAndRedact(t *testing.T) {
	for _, tc := range []struct {
		in, redacted string
	}{
		{"https://ci.example.com/run/1", "https://ci.example.com/run/1"},
		{"https://ci.example.com/run/1?token=s3cret", "https://ci.example.com/run/1?token=REDACTED"},
		{"https://ci.example.com/run/1?a=1&Access_Token=s3cret&b=2", "https://ci.example.com/run/1?a=1&Access_Token=REDACTED&b=2"},
		{"https://ci.example.com/run/1?%74oken=s3cret", "https://ci.example.com/run/1?%74oken=REDACTED"},
		{"https://ci.example.com/run/1#sig=s3cret", "https://ci.example.com/run/1#sig=REDACTED"},
		{"https://ci.example.com/run/1?a=1;key=s3cret", "https://ci.example.com/run/1?a=1;key=REDACTED"},
		{"https://ci.example.com/run/1?tokens=3&monkey=1", "https://ci.example.com/run/1?tokens=3&monkey=1"},
		{`see "https://x.example/?token=s3cret" here`, `see "https://x.example/?token=REDACTED" here`},
	} {
		assert.Equal(t, tc.redacted, urlsecret.Redact(tc.in), tc.in)
		assert.Equal(t, tc.redacted != tc.in, urlsecret.Has(tc.in), tc.in)
	}
	assert.True(t, urlsecret.Has("https://x.example/?token="), "an empty value is still a credential parameter")
}

// TestCRDRuleNamesEveryParam: the Bundle CRD's ciRunURL rule (admission
// for a Bundle created with kubectl) names exactly the parameters the Bundle
// API refuses and the receipts redact.
func TestCRDRuleNamesEveryParam(t *testing.T) {
	for _, f := range []string{
		filepath.Join("..", "..", "config", "crd", "bases", "kardinal.io_bundles.yaml"),
		filepath.Join("..", "..", "chart", "kardinal-promoter", "crds", "kardinal.io_bundles.yaml"),
	} {
		raw, err := os.ReadFile(f)
		require.NoError(t, err)
		m := regexp.MustCompile(`\(\?i\)\[\?&;#\]\(([^)]*)\)=`).FindSubmatch(raw)
		require.NotNil(t, m, "%s has the ciRunURL credential rule", f)
		got := strings.Split(string(m[1]), "|")
		sort.Strings(got)
		assert.Equal(t, urlsecret.Params(), got, f)
	}
}
