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
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/kustomize/api/krusty"
	"sigs.k8s.io/kustomize/kyaml/filesys"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	parentsteps "github.com/kardinal-promoter/kardinal-promoter/pkg/steps"
)

// renderContainerImages builds dir with the real kustomize library and
// returns the container images of the Deployment "app", keyed by container
// name.
func renderContainerImages(t *testing.T, dir string) map[string]string {
	t.Helper()
	k := krusty.MakeKustomizer(krusty.MakeDefaultOptions())
	resMap, err := k.Run(filesys.MakeFsOnDisk(), dir)
	require.NoError(t, err, "kustomize build")
	images := map[string]string{}
	for _, r := range resMap.Resources() {
		if r.GetKind() != "Deployment" || r.GetName() != "app" {
			continue
		}
		containers, err := r.GetSlice("spec.template.spec.containers")
		require.NoError(t, err)
		for _, c := range containers {
			m, ok := c.(map[string]interface{})
			require.True(t, ok)
			images[fmt.Sprint(m["name"])] = fmt.Sprint(m["image"])
		}
	}
	require.NotEmpty(t, images, "rendered output has no Deployment app")
	return images
}

// deploymentYAML returns a Deployment "app" with one container per image,
// named c0, c1, ...
func deploymentYAML(images ...string) string {
	var b strings.Builder
	b.WriteString(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: app
spec:
  selector:
    matchLabels: {app: app}
  template:
    metadata:
      labels: {app: app}
    spec:
      containers:
`)
	for i, img := range images {
		fmt.Fprintf(&b, "      - name: c%d\n        image: %s\n", i, img)
	}
	return b.String()
}

// TestKustomizeSetImage_RenderedImage runs kustomize-set-image and then
// renders the environment with krusty, asserting the image that kustomize
// actually deploys. kustomize matches an images entry on its name only, so an
// entry whose name is a short name (with or without newName) does not touch a
// manifest that uses the full repository; the step must also write a
// full-repository entry, as `kustomize edit set image` does (C05-steps-09).
func TestKustomizeSetImage_RenderedImage(t *testing.T) {
	const (
		repo   = "ghcr.io/org/app"
		digest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	)
	tests := []struct {
		name      string
		images    string // images: block of the initial kustomization, may be empty
		manifests []string
		bundle    v1alpha1.ImageRef
		want      map[string]string
	}{
		{
			name:      "legacy short-name entry with newName, full-name manifest",
			images:    "- name: app\n  newName: ghcr.io/org/app\n  newTag: v1\n",
			manifests: []string{"ghcr.io/org/app:v1"},
			bundle:    v1alpha1.ImageRef{Repository: repo, Tag: "v2"},
			want:      map[string]string{"c0": "ghcr.io/org/app:v2"},
		},
		{
			name:      "legacy short-name entry with newName, short-name manifest",
			images:    "- name: app\n  newName: ghcr.io/org/app\n  newTag: v1\n",
			manifests: []string{"app"},
			bundle:    v1alpha1.ImageRef{Repository: repo, Tag: "v2"},
			want:      map[string]string{"c0": "ghcr.io/org/app:v2"},
		},
		{
			name:      "short-name entry without newName, full-name manifest",
			images:    "- name: app\n  newTag: v1\n",
			manifests: []string{"ghcr.io/org/app:v1"},
			bundle:    v1alpha1.ImageRef{Repository: repo, Tag: "v2"},
			want:      map[string]string{"c0": "ghcr.io/org/app:v2"},
		},
		{
			name:      "short-name entry without newName, short-name manifest",
			images:    "- name: app\n  newTag: v1\n",
			manifests: []string{"app:v1"},
			bundle:    v1alpha1.ImageRef{Repository: repo, Tag: "v2"},
			want:      map[string]string{"c0": "ghcr.io/org/app:v2"},
		},
		{
			name:      "no images list",
			manifests: []string{"ghcr.io/org/app:v1"},
			bundle:    v1alpha1.ImageRef{Repository: repo, Tag: "v2"},
			want:      map[string]string{"c0": "ghcr.io/org/app:v2"},
		},
		{
			name:      "full-name entry",
			images:    "- name: ghcr.io/org/app\n  newTag: v1\n",
			manifests: []string{"ghcr.io/org/app:v1"},
			bundle:    v1alpha1.ImageRef{Repository: repo, Tag: "v2"},
			want:      map[string]string{"c0": "ghcr.io/org/app:v2"},
		},
		{
			name:      "placeholder entry pointing at the repository",
			images:    "- name: placeholder\n  newName: ghcr.io/org/app\n  newTag: v1\n",
			manifests: []string{"placeholder", "ghcr.io/org/app:v1"},
			bundle:    v1alpha1.ImageRef{Repository: repo, Tag: "v2"},
			want:      map[string]string{"c0": "ghcr.io/org/app:v2", "c1": "ghcr.io/org/app:v2"},
		},
		{
			name:      "digest only, legacy entry, full-name manifest",
			images:    "- name: app\n  newName: ghcr.io/org/app\n  newTag: v1\n",
			manifests: []string{"ghcr.io/org/app:v1", "app"},
			bundle:    v1alpha1.ImageRef{Repository: repo, Digest: digest},
			want:      map[string]string{"c0": "ghcr.io/org/app@" + digest, "c1": "ghcr.io/org/app@" + digest},
		},
		{
			name:      "short-name entry pointing elsewhere is left alone",
			images:    "- name: app\n  newName: ghcr.io/other/app\n  newTag: v1\n",
			manifests: []string{"ghcr.io/org/app:v1", "app"},
			bundle:    v1alpha1.ImageRef{Repository: repo, Tag: "v2"},
			want:      map[string]string{"c0": "ghcr.io/org/app:v2", "c1": "ghcr.io/other/app:v1"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			workDir := t.TempDir()
			envPath := filepath.Join(workDir, "environments", "prod")
			kust := "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n- deployment.yaml\n"
			if tc.images != "" {
				kust += "images:\n" + tc.images
			}
			writeKustomization(t, envPath, kust)
			require.NoError(t, os.WriteFile(filepath.Join(envPath, "deployment.yaml"),
				[]byte(deploymentYAML(tc.manifests...)), 0o644))

			res, err := mustLookup(t, "kustomize-set-image").Execute(context.Background(),
				makeKustomizeState(workDir, "prod", []v1alpha1.ImageRef{tc.bundle}))
			require.NoError(t, err)
			require.Equal(t, parentsteps.StepSuccess, res.Status)

			assert.Equal(t, tc.want, renderContainerImages(t, envPath),
				"kustomization after the step:\n%s", readKustomization(t, envPath))
		})
	}
}
