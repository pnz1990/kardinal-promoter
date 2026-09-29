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
	"io"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/translator"
)

// imageRepoPattern matches an image repository (the reference without its tag
// or digest): an optional registry host[:port]/ followed by lowercase path
// components, as in the distribution reference grammar.
// Valid: nginx, docker.io/library/nginx, ghcr.io/org/repo, localhost:5000/app
// Invalid: "not valid@@@", " ", uppercase path components
var imageRepoPattern = regexp.MustCompile(
	`^(?:[a-zA-Z0-9](?:[a-zA-Z0-9.-]*[a-zA-Z0-9])?(?::[0-9]+)?/)?` +
		`[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*(?:/[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*)*$`)

// imageDigestPattern matches an OCI content digest (algorithm:encoded).
var imageDigestPattern = regexp.MustCompile(`^[a-z0-9]+(?:[+._-][a-z0-9]+)*:[a-zA-Z0-9=_-]+$`)

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
		dryRun     bool
	)

	cmd := &cobra.Command{
		Use:   "bundle <pipeline>",
		Short: "Create a Bundle to trigger promotion through a Pipeline",
		Long: `Create a Bundle to trigger promotion through a Pipeline.

The pipeline name is a required positional argument.
Specify one or more container images with --image.

Use --dry-run to preview the promotion graph without creating any resources.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, ns, err := buildClient()
			if err != nil {
				return fmt.Errorf("create bundle: %w", err)
			}
			if dryRun {
				return createBundleDryRun(cmd.OutOrStdout(), c, ns, args[0], images, bundleType)
			}
			return createBundleFn(cmd.OutOrStdout(), c, ns, args[0], images, bundleType)
		},
	}

	cmd.Flags().StringArrayVar(&images, "image", nil, "Container image reference (can be specified multiple times)")
	cmd.Flags().StringVar(&bundleType, "type", "image", "Bundle type: image, config, or mixed")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false,
		"Preview the promotion graph without creating any cluster resources")

	return cmd
}

// createBundleFn is the testable implementation of create bundle.
func createBundleFn(w interface{ Write([]byte) (int, error) }, c sigs_client.Client, ns, pipeline string, images []string, bundleType string) error {
	imageRefs, err := parseImageRefs(images)
	if err != nil {
		return err
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
	// Record sub-second creation order so supersession picks the newer of two
	// Bundles created in the same second.
	lifecycle.StampCreatedAt(bundle, time.Now())

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

// createBundleDryRun previews the promotion graph that would be created for a Bundle,
// without writing any resources to the cluster. It fetches the Pipeline CRD,
// builds an in-memory Bundle, runs it through graph.Builder.Build, and prints
// the resulting graph summary.
func createBundleDryRun(w io.Writer, c sigs_client.Client, ns, pipelineName string, images []string, bundleType string) error {
	imageRefs, err := parseImageRefs(images)
	if err != nil {
		return err
	}

	// Fetch the Pipeline from the cluster (read-only — dry-run is cluster-aware but not cluster-mutating)
	var pipe v1alpha1.Pipeline
	if err := c.Get(context.Background(), sigs_client.ObjectKey{Namespace: ns, Name: pipelineName}, &pipe); err != nil {
		return fmt.Errorf("dry-run: fetch pipeline %q: %w", pipelineName, err)
	}

	// Build an in-memory Bundle (not created on cluster)
	bundle := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pipelineName + "-dry-run",
			Namespace: ns,
		},
		Spec: v1alpha1.BundleSpec{
			Type:     bundleType,
			Pipeline: pipelineName,
			Images:   imageRefs,
		},
	}

	// The gate templates the controller would pass to the builder (controller
	// default policy namespaces, or the Pipeline's spec.policyNamespaces).
	gates, err := translator.CollectGates(context.Background(), c, nil, &pipe)
	if err != nil {
		return fmt.Errorf("dry-run: collect policy gates: %w", err)
	}

	// Run graph.Builder.Build — pure function, no cluster writes
	b := graph.NewBuilder()
	result, err := b.Build(graph.BuildInput{
		Pipeline:    &pipe,
		Bundle:      bundle,
		PolicyGates: gates,
	})
	if err != nil {
		return fmt.Errorf("dry-run: graph build failed: %w", err)
	}

	// Print a human-readable summary
	_, _ = fmt.Fprintf(w, "[DRY-RUN] Bundle %q for pipeline %q\n", bundle.Name, pipelineName)
	_, _ = fmt.Fprintf(w, "\nPromotion graph: %d node(s)\n", result.NodeCount)
	_, _ = fmt.Fprintf(w, "\nEnvironments in promotion order:\n")

	// The gate instances the Graph would create, per environment.
	gatesByEnv := map[string][]string{}
	for _, node := range result.Graph.Spec.Nodes {
		if node.Template["kind"] != "PolicyGate" {
			continue
		}
		meta, _ := node.Template["metadata"].(map[string]interface{})
		labels, _ := meta["labels"].(map[string]interface{})
		env, _ := labels["kardinal.io/environment"].(string)
		name, _ := labels["kardinal.io/gate-name"].(string)
		gatesByEnv[env] = append(gatesByEnv[env], name)
	}
	for _, env := range pipe.Spec.Environments {
		line := "  \u2022 " + env.Name
		if g := gatesByEnv[env.Name]; len(g) > 0 {
			sort.Strings(g)
			line += " (gates: " + strings.Join(g, ", ") + ")"
		}
		_, _ = fmt.Fprintln(w, line)
	}

	_, _ = fmt.Fprintf(w, "\nNo resources were created. Remove --dry-run to apply.\n")
	return nil
}

// parseImageRefs turns --image values into ImageRefs. "repo@sha256:..." sets
// Digest, not Tag, and "repo:tag@sha256:..." sets both.
func parseImageRefs(images []string) ([]v1alpha1.ImageRef, error) {
	var refs []v1alpha1.ImageRef
	for _, img := range images {
		repo, tag, digest := splitImageRef(img)
		// Validate that the repository portion looks like a valid OCI image reference.
		// This prevents silently-succeeding bundles with obviously wrong image strings
		// (e.g. "not-valid-image@@@") that would later fail kustomize-set-image.
		if repo != "" && !imageRepoPattern.MatchString(repo) {
			return nil, fmt.Errorf("invalid image repository %q: want [host[:port]/]path (e.g. ghcr.io/org/image)", repo)
		}
		if digest != "" && !imageDigestPattern.MatchString(digest) {
			return nil, fmt.Errorf("invalid image digest %q in %q: must look like sha256:<hex>", digest, img)
		}
		refs = append(refs, v1alpha1.ImageRef{Repository: repo, Tag: tag, Digest: digest})
	}
	return refs, nil
}

// splitImageRef splits "repo", "repo:tag", "repo@digest" or
// "repo:tag@digest" into (repo, tag, digest). A colon before the last slash
// is a registry port, not a tag separator.
func splitImageRef(img string) (repo, tag, digest string) {
	if i := strings.LastIndex(img, "@"); i >= 0 {
		img, digest = img[:i], img[i+1:]
	}
	if i := strings.LastIndex(img, ":"); i > 0 && i > strings.LastIndex(img, "/") {
		return img[:i], img[i+1:], digest
	}
	return img, "", digest
}
