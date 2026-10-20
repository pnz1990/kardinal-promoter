// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package helm

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/util/jsonpath"
)

// sharingSection is docs/policy-gates.md's "Sharing gates with other
// namespaces" section, and its fenced blocks of language lang.
func sharingSection(t *testing.T, lang string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot(t), "docs", "policy-gates.md"))
	require.NoError(t, err)
	_, sec, ok := strings.Cut(string(b), "### Sharing gates with other namespaces\n")
	require.True(t, ok, "the section is in docs/policy-gates.md")
	if i := strings.Index(sec, "\n#"); i >= 0 && !strings.HasPrefix(sec[i+1:], "# ") {
		sec = sec[:i]
	}
	var out []string
	for _, part := range strings.Split(sec, "```"+lang+"\n")[1:] {
		block, _, _ := strings.Cut(part, "```")
		out = append(out, block)
	}
	return out
}

// TestPolicyNamespacesQuery (#2286): the command of docs/policy-gates.md
// that lists the Pipelines a later release refuses, run through client-go's
// jsonpath as kubectl's printer runs it (missing keys allowed) and its awk
// filter, prints exactly the Pipelines whose PolicyNamespacesShared condition
// is False, beside other conditions in any order; not one without the
// condition, without status, or with it True. TestPolicyNamespacesDocsOnAPIServer
// runs the same command with kubectl on an API server.
//
// Covers GATE-POLNS-03.
func TestPolicyNamespacesQuery(t *testing.T) {
	t.Parallel()
	var query string
	for _, block := range sharingSection(t, "bash") {
		if strings.HasPrefix(block, "kubectl get pipelines -A") {
			query = strings.TrimSpace(block)
		}
	}
	require.NotEmpty(t, query, "the section's kubectl get pipelines command")
	get, filter, ok := strings.Cut(query, " | ")
	require.True(t, ok, "kubectl ... | awk ...: %s", query)
	_, tmpl, ok := strings.Cut(get, "-o jsonpath='")
	require.True(t, ok, get)
	tmpl = strings.TrimSuffix(tmpl, "'")
	require.Equal(t, `awk -F'\t' '$2=="False"{print $1}'`, filter, "the filter this test applies")

	list := map[string]interface{}{"items": []interface{}{
		pipelineJSON("team-a", "refused", map[string]string{"Ready": "True", "PolicyNamespacesShared": "False"}),
		pipelineJSON("team-b", "first", map[string]string{"PolicyNamespacesShared": "False", "Ready": "False", "Paused": "True"}),
		pipelineJSON("team-a", "shared", map[string]string{"Ready": "True"}),
		pipelineJSON("team-a", "true", map[string]string{"PolicyNamespacesShared": "True"}),
		pipelineJSON("team-b", "new", nil),
	}}
	jp := jsonpath.New("query").AllowMissingKeys(true)
	require.NoError(t, jp.Parse(tmpl))
	var out bytes.Buffer
	require.NoError(t, jp.Execute(&out, list))
	var got []string
	for _, line := range strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) > 1 && fields[1] == "False" {
			got = append(got, fields[0])
		}
	}
	assert.Equal(t, []string{"team-a/refused", "team-b/first"}, got, "jsonpath printed:\n%s", out.String())
}

// pipelineJSON is a Pipeline as the API server lists it, with conditions
// (type: status) in any order (Go's map order), or no status when nil.
func pipelineJSON(ns, name string, conditions map[string]string) map[string]interface{} {
	p := map[string]interface{}{"metadata": map[string]interface{}{"namespace": ns, "name": name}}
	if conditions != nil {
		var cs []interface{}
		for typ, status := range conditions {
			cs = append(cs, map[string]interface{}{"type": typ, "status": status})
		}
		p["status"] = map[string]interface{}{"conditions": cs}
	}
	// Round-trip through JSON, as a decoded list is.
	b, _ := json.Marshal(p)
	var out map[string]interface{}
	_ = json.Unmarshal(b, &out)
	return out
}
