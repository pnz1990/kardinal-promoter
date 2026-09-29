// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestGateNodeK8sNameKeepsSeparator pins the "--" in every gate instance
// name. The PolicyGate CRD exempts names containing "--" from its 63-character
// rule (api/v1alpha1/policygate_types.go), because instance names are longer
// than 63. A form without "--" would be rejected by the API server. Template,
// namespace and environment names are each at most 63 characters, so the
// "--" is at index 191 or earlier, and the hash cut keeps it.
func TestGateNodeK8sNameKeepsSeparator(t *testing.T) {
	long := strings.Repeat("a", 63)
	tests := []struct {
		name                   string
		bundle, gate, ns, env  string
		wantHashed, wantPrefix bool
	}{
		{name: "exact", bundle: "demo-x7k2m", gate: "no-weekend-deploys", ns: "platform-policies", env: "prod"},
		{name: "dotted bundle", bundle: "demo-v1.2.3", gate: "no-weekend-deploys", ns: "platform-policies", env: "prod", wantHashed: true},
		{name: "dotted gate", bundle: "demo-x7k2m", gate: "org.no-weekend", ns: "platform-policies", env: "prod", wantHashed: true},
		{name: "empty namespace", bundle: "demo-x7k2m", gate: "g", env: "prod"},
		{name: "longest parts", bundle: strings.Repeat("b", 253), gate: long, ns: long, env: long, wantHashed: true, wantPrefix: true},
		{name: "longest dotted parts", bundle: strings.Repeat("b.", 126), gate: strings.Repeat("g.", 31) + "g", ns: long, env: long, wantHashed: true, wantPrefix: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := gateNodeK8sName(tt.bundle, tt.gate, tt.ns, tt.env)
			assert.Contains(t, got, "--")
			assert.LessOrEqual(t, len(got), maxObjectNameLen)
			assert.Equal(t, got, gateNodeK8sName(tt.bundle, tt.gate, tt.ns, tt.env), "deterministic")
			ns := tt.ns
			if ns == "" {
				ns = "default"
			}
			exact := tt.gate + "-" + ns + "-" + tt.env + "--" + tt.bundle
			if tt.wantHashed {
				assert.NotEqual(t, exact, got)
				assert.Regexp(t, `-[0-9a-f]{8}$`, got, "hash suffix")
			} else {
				assert.Equal(t, exact, got)
			}
			if tt.wantPrefix {
				assert.Len(t, got, maxObjectNameLen, "cut to the name limit")
			}
		})
	}
}
