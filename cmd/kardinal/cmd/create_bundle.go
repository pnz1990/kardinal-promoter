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
		opts   createBundleOptions
		dryRun bool
	)

	cmd := &cobra.Command{
		Use:   "bundle <pipeline>",
		Short: "Create a Bundle to trigger promotion through a Pipeline",
		Long: `Create a Bundle to trigger promotion through a Pipeline.

The pipeline name is a required positional argument; the Pipeline must exist.
An image or mixed Bundle needs at least one --image. A config or mixed Bundle
needs --config-commit, the commit of the config repository to promote;
--config-repo names that repository and defaults to the Pipeline's git.url.
An image Bundle with --config-repo or --config-commit is refused: it would
deploy only its images and ignore them.

--commit, --author and --ci-run-url set the Bundle's provenance, shown in the
PR body and the UI. kardinal records them as given: they are what the caller
asserts, as with the Bundle API.

The Bundle API (POST /api/v1/bundles) applies the same checks.

Use --dry-run to preview the promotion graph without creating any resources.`,
		Example: `  kardinal create bundle my-app --image ghcr.io/org/my-app:sha-abc1234 \
    --commit abc1234 --author "$GITHUB_ACTOR" --ci-run-url "$RUN_URL"
  kardinal create bundle my-app --type config --config-commit 9f8e7d6`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, ns, err := buildClient()
			if err != nil {
				return fmt.Errorf("create bundle: %w", err)
			}
			if dryRun {
				return createBundleDryRun(cmd.OutOrStdout(), c, ns, args[0], opts)
			}
			return createBundleFn(cmd.OutOrStdout(), c, ns, args[0], opts)
		},
	}

	cmd.Flags().StringArrayVar(&opts.Images, "image", nil,
		"Container image reference (can be specified multiple times); required for image and mixed Bundles")
	cmd.Flags().StringVar(&opts.Type, "type", "image", "Bundle type: image, config, or mixed")
	cmd.Flags().StringVar(&opts.ConfigCommit, "config-commit", "",
		"Commit SHA of the config repository to promote; required for config and mixed Bundles")
	cmd.Flags().StringVar(&opts.ConfigRepo, "config-repo", "",
		"Git URL of the config repository (default: the Pipeline's git.url)")
	cmd.Flags().StringVar(&opts.Commit, "commit", "", "Source commit SHA that produced the Bundle (provenance)")
	cmd.Flags().StringVar(&opts.Author, "author", "", "Author or CI actor of the build (provenance)")
	cmd.Flags().StringVar(&opts.CIRunURL, "ci-run-url", "",
		"Absolute http(s) URL of the CI run that built the Bundle (provenance)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false,
		"Preview the promotion graph without creating any cluster resources")
	compactAbove := graph.DefaultCompactAbove
	opts.CompactAbove = &compactAbove
	cmd.Flags().IntVar(&compactAbove, "graph-compact-above", graph.DefaultCompactAbove,
		"With --dry-run: the controller's --graph-compact-above (chart graph.compactAbove), the environment count above which the Graph is compact")

	return cmd
}

// createBundleOptions are the flags of kardinal create bundle.
type createBundleOptions struct {
	Images       []string
	Type         string
	ConfigRepo   string
	ConfigCommit string
	Commit       string
	Author       string
	CIRunURL     string
	// CompactAbove is the controller's --graph-compact-above, for --dry-run;
	// nil means graph.DefaultCompactAbove.
	CompactAbove *int
}

// bundleSpec turns the flags into a Bundle spec for pipeline and checks it
// with lifecycle.ValidateNewBundle, the Bundle API's rules (#1285).
func (o createBundleOptions) bundleSpec(pipeline string) (v1alpha1.BundleSpec, error) {
	// An image Bundle would ignore the config flags (#1353).
	if o.Type == "" || o.Type == "image" {
		switch {
		case o.ConfigRepo != "":
			return v1alpha1.BundleSpec{}, fmt.Errorf("create bundle: --config-repo needs --type config or mixed and --config-commit")
		case o.ConfigCommit != "":
			return v1alpha1.BundleSpec{}, fmt.Errorf("create bundle: --config-commit needs --type config or mixed")
		}
	}
	imageRefs, err := parseImageRefs(o.Images)
	if err != nil {
		return v1alpha1.BundleSpec{}, err
	}
	spec := v1alpha1.BundleSpec{Type: o.Type, Pipeline: pipeline, Images: imageRefs}
	if o.ConfigRepo != "" || o.ConfigCommit != "" {
		spec.ConfigRef = &v1alpha1.ConfigRef{GitRepo: o.ConfigRepo, CommitSHA: o.ConfigCommit}
	}
	if o.Commit != "" || o.Author != "" || o.CIRunURL != "" {
		spec.Provenance = &v1alpha1.BundleProvenance{CommitSHA: o.Commit, Author: o.Author, CIRunURL: o.CIRunURL}
	}
	if err := lifecycle.ValidateNewBundle(&spec); err != nil {
		return v1alpha1.BundleSpec{}, flagMessage(err)
	}
	return spec, nil
}

// flagMessage names the CLI flag in the shared validation messages, which
// name the Bundle fields.
func flagMessage(err error) error {
	msg := strings.NewReplacer(
		"at least one entry in images", "at least one --image",
		"configRef.commitSHA", "--config-commit",
		"provenance.ciRunURL", "--ci-run-url",
	).Replace(err.Error())
	return fmt.Errorf("create bundle: %s", msg)
}

// createBundleFn is the testable implementation of create bundle. It checks
// the flags and that the Pipeline exists before it creates anything.
func createBundleFn(w io.Writer, c sigs_client.Client, ns, pipeline string, opts createBundleOptions) error {
	spec, err := opts.bundleSpec(pipeline)
	if err != nil {
		return err
	}
	ctx := context.Background()
	if _, err := getPipeline(ctx, c, ns, pipeline); err != nil {
		return err
	}

	bundle := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: pipeline + "-",
			Namespace:    ns,
		},
		Spec: spec,
	}
	// Record sub-second creation order so supersession picks the newer of two
	// Bundles created in the same second.
	lifecycle.StampCreatedAt(bundle, time.Now())

	if err := c.Create(ctx, bundle); err != nil {
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
// without writing any resources to the cluster. It checks the flags like
// createBundleFn, fetches the Pipeline CRD, builds an in-memory Bundle, runs it
// through graph.Builder.Build, and prints the resulting graph summary.
func createBundleDryRun(w io.Writer, c sigs_client.Client, ns, pipelineName string, opts createBundleOptions) error {
	spec, err := opts.bundleSpec(pipelineName)
	if err != nil {
		return err
	}

	// Fetch the Pipeline from the cluster (read-only — dry-run is cluster-aware but not cluster-mutating)
	pipePtr, err := getPipeline(context.Background(), c, ns, pipelineName)
	if err != nil {
		return fmt.Errorf("dry-run: %w", err)
	}
	pipe := *pipePtr

	// Build an in-memory Bundle (not created on cluster)
	bundle := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pipelineName + "-dry-run",
			Namespace: ns,
		},
		Spec: spec,
	}

	// The gate templates the controller would pass to the builder (controller
	// default policy namespaces, or the Pipeline's spec.policyNamespaces).
	gates, err := translator.CollectGates(context.Background(), c, nil, &pipe)
	if err != nil {
		return fmt.Errorf("dry-run: collect policy gates: %w", err)
	}

	// Run graph.Builder.Build — pure function, no cluster writes
	b := graph.NewBuilder()
	if opts.CompactAbove != nil {
		b.CompactAbove = *opts.CompactAbove
	}
	result, err := b.Build(graph.BuildInput{
		Pipeline:    &pipe,
		Bundle:      bundle,
		PolicyGates: gates,
	})
	if err != nil {
		return fmt.Errorf("dry-run: %w", err) // Build errors start with "build: "
	}

	// Print a human-readable summary
	_, _ = fmt.Fprintf(w, "[DRY-RUN] Bundle %q for pipeline %q\n", bundle.Name, pipelineName)
	if result.Compact {
		_, _ = fmt.Fprintf(w, "\nPromotion graph: %d node(s), compact shape\n", result.NodeCount)
	} else {
		_, _ = fmt.Fprintf(w, "\nPromotion graph: %d node(s)\n", result.NodeCount)
	}
	_, _ = fmt.Fprintf(w, "\nEnvironments in promotion order:\n")

	// The gate instances the Graph would create, per environment.
	gatesByEnv := map[string][]string{}
	for _, g := range result.GateInstances {
		env := g.Labels["kardinal.io/environment"]
		gatesByEnv[env] = append(gatesByEnv[env], g.Labels["kardinal.io/gate-name"])
	}
	// Promotion order is the dependsOn order the Graph follows, not the order
	// the environments are declared in.
	order, err := graph.PromotedEnvironments(&pipe, bundle)
	if err != nil {
		return fmt.Errorf("dry-run: environment order: %w", err)
	}
	for _, env := range order {
		line := "  \u2022 " + env
		if g := gatesByEnv[env]; len(g) > 0 {
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
