// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
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

package cmd

import (
	"context"
	"fmt"
	"regexp"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// imageRepoPattern matches valid OCI image repository references.
// Valid: nginx, docker.io/library/nginx, ghcr.io/org/repo
// Invalid: "not valid@@@", " ", spaces, multiple colons in unusual positions
// This is a best-effort check; the full OCI spec is more complex.
var imageRepoPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._\-/]*$`)

func newCreateCmd() *cobra.Command {
	create := &cobra.Command{
		Use:   "create",
		Short: "Create kardinal resources",
	}
	create.AddCommand(newCreateBundleCmd())
	return create
}

func newCreateBundleCmd() *cobra.Command {
	var (
		images     []string
		bundleType string
	)

	cmd := &cobra.Command{
		Use:   "bundle <pipeline>",
		Short: "Create a Bundle to trigger promotion through a Pipeline",
		Long: `Create a Bundle to trigger promotion through a Pipeline.

The pipeline name is a required positional argument.
Specify one or more container images with --image.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, ns, err := buildClient()
			if err != nil {
				return fmt.Errorf("create bundle: %w", err)
			}
			return createBundleFn(cmd.OutOrStdout(), c, ns, args[0], images, bundleType)
		},
	}

	cmd.Flags().StringArrayVar(&images, "image", nil, "Container image reference (can be specified multiple times)")
	cmd.Flags().StringVar(&bundleType, "type", "image", "Bundle type: image, config, or mixed")

	return cmd
}

// createBundleFn is the testable implementation of create bundle.
func createBundleFn(w interface{ Write([]byte) (int, error) }, c sigs_client.Client, ns, pipeline string, images []string, bundleType string) error {
	var imageRefs []v1alpha1.ImageRef
	for _, img := range images {
		repo, tag := splitImageRef(img)
		// Validate that the repository portion looks like a valid OCI image reference.
		// This prevents silently-succeeding bundles with obviously wrong image strings
		// (e.g. "not-valid-image@@@") that would later fail kustomize-set-image.
		if repo != "" && !imageRepoPattern.MatchString(repo) {
			return fmt.Errorf("invalid image repository %q: must match [a-zA-Z0-9][a-zA-Z0-9._-/]* (e.g. ghcr.io/org/image)", repo)
		}
		imageRefs = append(imageRefs, v1alpha1.ImageRef{
			Repository: repo,
			Tag:        tag,
		})
	}

	bundle := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: pipeline + "-",
			Namespace:    ns,
		},
		Spec: v1alpha1.BundleSpec{
			Type:     bundleType,
			Pipeline: pipeline,
			Images:   imageRefs,
		},
	}

	if err := c.Create(context.Background(), bundle); err != nil {
		return fmt.Errorf("create bundle for pipeline %s: %w", pipeline, err)
	}

	if _, err := fmt.Fprintf(w,
		"Bundle %s created for pipeline %s\n"+
			"Track with: kardinal get bundles %s\n",
		bundle.Name, pipeline, pipeline,
	); err != nil {
		return fmt.Errorf("write output: %w", err)
	}

	return nil
}

// splitImageRef splits "repo:tag" or "repo@digest" into (repo, tag).
func splitImageRef(img string) (string, string) {
	// Handle digest first
	for i, c := range img {
		if c == '@' {
			return img[:i], img[i+1:]
		}
	}
	// Handle tag (last colon that doesn't have a slash after it = tag separator)
	for i := len(img) - 1; i >= 0; i-- {
		if img[i] == ':' && i > 0 {
			rest := img[i+1:]
			hasSlash := false
			for _, ch := range rest {
				if ch == '/' {
					hasSlash = true
					break
				}
			}
			if !hasSlash {
				return img[:i], rest
			}
		}
	}
	return img, ""
}
