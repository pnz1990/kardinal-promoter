// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHelmValuesPage(t *testing.T) {
	page, err := HelmValuesPage([]byte(`# Default values.

# -- Number of replicas
replicaCount: 1

image:
  # -- Image repository
  repository: ghcr.io/x
  tag: ""

# -- Embedded UI.
ui:
  # -- Allowed hosts (<fullname>.<ns>)
  allowedHosts: []

# -- Pod security context
podSecurityContext:
  runAsNonRoot: true

# A section note, not a description.
#   bash hack/install-kro.sh
graph:
  # -- Retire delays. A | in text.
  #   example: 1m
  retire:
    superseded: ""
`))
	require.NoError(t, err)
	for _, row := range []string{
		"| `replicaCount` | `1` | Number of replicas |",
		"| `image.repository` | `ghcr.io/x` | Image repository |",
		"| `image.tag` | `\"\"` |  |",
		"| `ui` |  | Embedded UI. |",
		"| `ui.allowedHosts` | `[]` | Allowed hosts (&lt;fullname&gt;.&lt;ns&gt;) |",
		"| `podSecurityContext` | `{\"runAsNonRoot\":true}` | Pod security context |",
		"| `graph.retire` | `{\"superseded\":\"\"}` | Retire delays. A \\| in text. |",
	} {
		assert.Contains(t, page, row)
	}
	assert.NotContains(t, page, "section note")
	assert.NotContains(t, page, "example: 1m")
}

func TestControllerFlagsPage(t *testing.T) {
	page := ControllerFlagsPage(`Usage of kardinal-controller:
  -audit-retention
    	Delete AuditEvents past their retention. (default true)
  -bundle-workers int
    	How many Bundles are reconciled at once. (default 4)
  -leader-elect
    	Enable leader election.
  -policy-namespaces string
    	Comma-separated namespaces
    	scanned for org gates. (default "platform-policies")
`)
	for _, row := range []string{
		"| `--audit-retention` | bool | `true` | Delete AuditEvents past their retention. |",
		"| `--bundle-workers` | int | `4` | How many Bundles are reconciled at once. |",
		"| `--leader-elect` | bool | `false` | Enable leader election. |",
		"| `--policy-namespaces` | string | `\"platform-policies\"` | Comma-separated namespaces scanned for org gates. |",
	} {
		assert.Contains(t, page, row)
	}
}
