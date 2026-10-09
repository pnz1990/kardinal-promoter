// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package v1alpha1_test

import (
	"os"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/yaml"
)

// TestPRStatusRepoPattern: the PRStatus CRD rejects a spec.repo the SCM API
// would read as another path (percent escapes, backslashes, "?", "#",
// control characters), the first line of defence before the allowlist's
// own segment check (QA on #1483). The pattern is the generated CRD's, and
// the chart ships the same file.
func TestPRStatusRepoPattern(t *testing.T) {
	var pattern string
	for _, path := range []string{"../../config/crd/bases/kardinal.io_prstatuses.yaml", "../../chart/kardinal-promoter/crds/kardinal.io_prstatuses.yaml"} {
		raw, err := os.ReadFile(path)
		require.NoError(t, err)
		var crd apiextensionsv1.CustomResourceDefinition
		require.NoError(t, yaml.Unmarshal(raw, &crd))
		repo := crd.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties["spec"].Properties["repo"]
		require.NotEmpty(t, repo.Pattern, path)
		if pattern == "" {
			pattern = repo.Pattern
		}
		assert.Equal(t, pattern, repo.Pattern, "%s matches config/crd/bases", path)
	}
	re := regexp.MustCompile(pattern)
	for repo, want := range map[string]bool{
		"":                             true, // the placeholder
		"acme/my-service":              true,
		"group/sub/project.name":       true,
		"acme/My Project/gitops":       true, // an Azure DevOps project with a space
		"acme/Project/my gitops":       true, // an Azure DevOps repository with a space
		"acme/My Project/my gitops":    true,
		"acme/My  Project/gitops":      false, // a double space
		"acme/Project/gitops ":         false, // a trailing space
		"acme/%2e%2e%2fvictim%2frepo":  false,
		`acme\victim`:                  false,
		"acme/x?y":                     false,
		"acme/x#y":                     false,
		"acme/a\tb":                    false,
		"acme/a\nb":                    false,
		"acme//victim":                 false,
		"/acme/repo":                   false,
		"acme/ leading-space":          false,
		"https://github.com/acme/repo": false,
	} {
		assert.Equal(t, want, re.MatchString(repo), "%q", repo)
	}
}
