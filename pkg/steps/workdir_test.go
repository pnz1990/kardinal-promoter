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

package steps_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/steps"
)

// TestWorkDirFor_UniquePerPromotionStep proves that sibling environments of
// one Bundle and same-named Pipelines in two namespaces get distinct
// checkouts (C05-steps-01, C05-steps-02), and that no segment escapes root.
func TestWorkDirFor_UniquePerPromotionStep(t *testing.T) {
	root := "/tmp/kardinal"
	base := steps.WorkDirFor(root, "team-a", "app", "app-v2", "prod-eu")

	distinct := []struct {
		name string
		dir  string
	}{
		{"sibling environment", steps.WorkDirFor(root, "team-a", "app", "app-v2", "prod-us")},
		{"other namespace", steps.WorkDirFor(root, "team-b", "app", "app-v2", "prod-eu")},
		{"other bundle", steps.WorkDirFor(root, "team-a", "app", "app-v3", "prod-eu")},
		{"other pipeline", steps.WorkDirFor(root, "team-a", "app2", "app-v2", "prod-eu")},
	}
	for _, tc := range distinct {
		t.Run(tc.name, func(t *testing.T) {
			assert.NotEqual(t, base, tc.dir)
		})
	}

	assert.NotEqual(t,
		steps.WorkDirFor(root, "ns", "p", "b", "prod/eu"),
		steps.WorkDirFor(root, "ns", "p", "b", "prod_eu"),
		"escaped segments must stay distinct")

	escapes := [][4]string{
		{"..", "..", "..", ".."},
		{"a", "../../etc", "b", "c"},
		{"a", "b", "c", "/abs"},
		{"", "", "", ""},
	}
	for _, e := range escapes {
		dir := steps.WorkDirFor(root, e[0], e[1], e[2], e[3])
		rel, err := filepath.Rel(root, dir)
		assert.NoError(t, err)
		assert.False(t, strings.HasPrefix(rel, ".."), "dir %q escapes root", dir)
		assert.Len(t, strings.Split(rel, string(filepath.Separator)), 4, "dir %q must have 4 segments", dir)
	}
}

// TestConfigSourceDir_OutsideWorkDir proves the config source checkout is a
// sibling of the working tree, never inside it (C05-steps-03).
func TestConfigSourceDir_OutsideWorkDir(t *testing.T) {
	wd := "/tmp/kardinal/ns/p/b/prod"
	src := steps.ConfigSourceDir(wd)
	rel, err := filepath.Rel(wd, src)
	assert.NoError(t, err)
	assert.True(t, strings.HasPrefix(rel, ".."), "config source %q must not be inside %q", src, wd)
	assert.Equal(t, src, steps.ConfigSourceDir(wd+"/"))
	for _, env := range []string{"prod.config-source", "prod#config-source"} {
		assert.NotEqual(t, src, steps.WorkDirFor("/tmp/kardinal", "ns", "p", "b", env),
			"config source must not collide with environment %q's work dir", env)
	}
}
