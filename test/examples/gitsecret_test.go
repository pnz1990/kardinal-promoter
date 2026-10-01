// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package examples

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sigyaml "sigs.k8s.io/yaml"
)

// gitSecretProblems reports every Pipeline without spec.git.secretRef.name.
// Every promotion pushes to the GitOps repo with that Secret's token; without
// one the push is unauthenticated and fails, even on a public repo.
func gitSecretProblems(docs []doc) []string {
	var problems []string
	for _, d := range docs {
		if str(d.obj, "kind") != "Pipeline" || str(d.obj, "spec", "git", "secretRef", "name") != "" {
			continue
		}
		problems = append(problems, fmt.Sprintf("%s has no spec.git.secretRef: its promotions cannot push", d))
	}
	sort.Strings(problems)
	return problems
}

// TestExamplePipelinesNameAGitSecret checks that every example Pipeline,
// including the ones the example charts render, can push.
func TestExamplePipelinesNameAGitSecret(t *testing.T) {
	assert.Empty(t, gitSecretProblems(allDocs(t)))
}

func TestGitSecretCheckCatchesKnownMistakes(t *testing.T) {
	tests := []struct {
		name, git string
		want      string // "" means no problem
	}{
		// examples/multi-tenant/chart before it rendered secretRef: the
		// promotions failed with "authentication required".
		{name: "no secretRef", git: "{url: https://github.com/myorg/gitops}",
			want: "x.yaml#0 Pipeline/p has no spec.git.secretRef"},
		{name: "secretRef without a name", git: "{url: https://github.com/myorg/gitops, secretRef: {}}",
			want: "x.yaml#0 Pipeline/p has no spec.git.secretRef"},
		{name: "secretRef", git: "{url: https://github.com/myorg/gitops, secretRef: {name: github-token}}"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var obj map[string]interface{}
			require.NoError(t, sigyaml.Unmarshal([]byte("kind: Pipeline\nmetadata: {name: p}\nspec: {git: "+tt.git+"}\n"), &obj))
			problems := gitSecretProblems([]doc{{source: "x.yaml", obj: obj}})
			if tt.want == "" {
				assert.Empty(t, problems)
				return
			}
			require.Len(t, problems, 1)
			assert.True(t, strings.HasPrefix(problems[0], tt.want), problems[0])
		})
	}
}
