// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package framework

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

var dns1123Label = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

func TestNamespaceFor(t *testing.T) {
	long := "TestCore_" + strings.Repeat("VeryLongSubtestName/", 10)
	names := map[string]string{}
	for _, in := range []string{
		"TestCore_HappyPath",
		"TestCore_HappyPath/prod_pr-review",
		"TestCore_HappyPath/prod pr review",
		long,
		long + "x",
	} {
		got := namespaceFor(in, "n1")
		assert.LessOrEqual(t, len(got), 63, got)
		assert.Regexp(t, dns1123Label, got)
		assert.True(t, strings.HasPrefix(got, "e2e-"), got)
		if prev, dup := names[got]; dup {
			t.Errorf("%q and %q map to the same namespace %s", prev, in, got)
		}
		names[got] = in
	}
	assert.Equal(t, namespaceFor("TestX", "a"), namespaceFor("TestX", "a"), "must be stable")
	assert.NotEqual(t, namespaceFor("TestX", "a"), namespaceFor("TestX", "b"), "a new nonce gives a new name")
}
