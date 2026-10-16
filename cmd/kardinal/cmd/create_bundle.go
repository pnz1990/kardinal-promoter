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
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
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
		Long: `Create a Bundle: the version to promote through <pipeline>. An image Bundle
needs --image; a config Bundle --config-commit; a mixed one both. --commit,
--author and --ci-run-url record its provenance for the PR body and the UI;
--commit-time, when the commit was made, starts the lead time metrics.
--dry-run shows the promotion graph and creates nothing.`,
		Example: `  kardinal create bundle my-app --image ghcr.io/org/my-app:sha-abc1234 \
    --commit abc1234 --commit-time "$(git show -s --format=%cI)" \
    --author "$GITHUB_ACTOR" --ci-run-url "$RUN_URL"
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

	addBundleFlags(cmd, &opts, &dryRun)

	withReference(cmd, createBundleReference)
	return withStructuredOutput(cmd)
}

// addBundleFlags registers the flags of a Bundle (create bundle, ship) on
// cmd, into opts and dryRun.
func addBundleFlags(cmd *cobra.Command, opts *createBundleOptions, dryRun *bool) {
	cmd.Flags().StringArrayVar(&opts.Images, "image", nil,
		"Container image reference (can be specified multiple times); required for image and mixed Bundles")
	cmd.Flags().StringVar(&opts.Type, "type", "image", "Bundle type: image, config or mixed (chart Bundles come from POST /api/v1/bundles or a Helm Subscription: this command has no chart flags)")
	cmd.Flags().StringVar(&opts.ConfigCommit, "config-commit", "",
		"Commit SHA of the config repository to promote; required for config and mixed Bundles")
	cmd.Flags().StringVar(&opts.ConfigRepo, "config-repo", "",
		"Git URL of the config repository (default: the Pipeline's git.url)")
	cmd.Flags().StringVar(&opts.Commit, "commit", "", "Source commit SHA that produced the Bundle (provenance)")
	cmd.Flags().StringVar(&opts.Author, "author", "", "Author or CI actor of the build (provenance)")
	cmd.Flags().StringVar(&opts.CIRunURL, "ci-run-url", "",
		"Absolute http(s) URL of the CI run that built the Bundle (provenance)")
	cmd.Flags().StringVar(&opts.CommitTime, "commit-time", "",
		"When the source commit was made, RFC 3339 (provenance; lead time starts at it, e.g. \"$(git show -s --format=%cI)\")")
	cmd.Flags().BoolVar(dryRun, "dry-run", false,
		"Preview the promotion graph without creating any cluster resources")
	compactAbove := graph.DefaultCompactAbove
	opts.CompactAbove = &compactAbove
	cmd.Flags().IntVar(&compactAbove, "graph-compact-above", graph.DefaultCompactAbove,
		"With --dry-run: the controller's --graph-compact-above (chart graph.compactAbove), the environment count above which the Graph is compact")
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
	// CommitTime is --commit-time: when the source commit was made
	// (RFC 3339), the start of the lead time metrics.
	CommitTime string
	// CompactAbove is the controller's --graph-compact-above, for --dry-run;
	// nil means graph.DefaultCompactAbove.
	CompactAbove *int
	// Labels go on the new Bundle (quickstart marks its own).
	Labels map[string]string
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
	if o.Commit != "" || o.Author != "" || o.CIRunURL != "" || o.CommitTime != "" {
		spec.Provenance = &v1alpha1.BundleProvenance{CommitSHA: o.Commit, Author: o.Author, CIRunURL: o.CIRunURL}
	}
	if o.CommitTime != "" {
		t, err := time.Parse(time.RFC3339, o.CommitTime)
		if err != nil {
			return v1alpha1.BundleSpec{}, fmt.Errorf("create bundle: --commit-time %q is not an RFC 3339 time (git show -s --format=%%cI prints one)", o.CommitTime)
		}
		ct := metav1.NewTime(t)
		spec.Provenance.CommitTime = &ct
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
	bundle, err := newBundle(context.Background(), c, ns, pipeline, opts)
	if err != nil {
		return err
	}
	if structured() {
		return writeStructured(w, createdOf(bundle))
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

// newBundle creates the Bundle opts describe for pipeline, after checking
// the flags and that the Pipeline exists.
func newBundle(ctx context.Context, c sigs_client.Client, ns, pipeline string, opts createBundleOptions) (*v1alpha1.Bundle, error) {
	spec, err := opts.bundleSpec(pipeline)
	if err != nil {
		return nil, err
	}
	if _, err := getPipeline(ctx, c, ns, pipeline); err != nil {
		return nil, err
	}

	bundle := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: pipeline + "-",
			Namespace:    ns,
			Labels:       opts.Labels,
		},
		Spec: spec,
	}
	// Record sub-second creation order so supersession picks the newer of two
	// Bundles created in the same second.
	lifecycle.StampCreatedAt(bundle, time.Now())
	if err := stampCreator(ctx, c, bundle); err != nil {
		return nil, fmt.Errorf("create bundle for pipeline %s: %w", pipeline, err)
	}

	if err := c.Create(ctx, bundle); err != nil {
		return nil, fmt.Errorf("create bundle for pipeline %s: %w", pipeline, err)
	}
	return bundle, nil
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
	if structured() {
		out := dryRunResult{Pipeline: pipelineName, Namespace: ns, Type: spec.Type, Version: scm.BundleVersion(spec),
			Nodes: result.NodeCount, Compact: result.Compact, Environments: []dryRunEnvironment{}}
		for _, env := range order {
			g := gatesByEnv[env]
			sort.Strings(g)
			out.Environments = append(out.Environments, dryRunEnvironment{Name: env, Gates: g})
		}
		return writeStructured(w, out)
	}

	// Print a human-readable summary
	_, _ = fmt.Fprintf(w, "[DRY-RUN] Bundle %q for pipeline %q\n", bundle.Name, pipelineName)
	if result.Compact {
		_, _ = fmt.Fprintf(w, "\nPromotion graph: %d node(s), compact shape\n", result.NodeCount)
	} else {
		_, _ = fmt.Fprintf(w, "\nPromotion graph: %d node(s)\n", result.NodeCount)
	}
	_, _ = fmt.Fprintf(w, "\nEnvironments in promotion order:\n")
	for _, env := range order {
		line := "  " + sty.Sym("\u2022", "*") + " " + env
		if g := gatesByEnv[env]; len(g) > 0 {
			sort.Strings(g)
			line += " (gates: " + strings.Join(g, ", ") + ")"
		}
		_, _ = fmt.Fprintln(w, line)
	}

	_, _ = fmt.Fprintf(w, "\nNo resources were created. Remove --dry-run to apply.\n")
	return nil
}

// dryRunResult is create bundle --dry-run -o json|yaml: the Graph a Bundle
// would get, created nowhere.
type dryRunResult struct {
	Pipeline  string `json:"pipeline"`
	Namespace string `json:"namespace"`
	Type      string `json:"type,omitempty"`
	Version   string `json:"version,omitempty"`
	// Nodes is the Graph's node count, and Compact whether it has the
	// compact shape.
	Nodes   int  `json:"nodes"`
	Compact bool `json:"compact,omitempty"`
	// Environments are in promotion order, each with the gates the Graph
	// would create there.
	Environments []dryRunEnvironment `json:"environments"`
}

// dryRunEnvironment is one environment of a dry-run Graph.
type dryRunEnvironment struct {
	Name  string   `json:"name"`
	Gates []string `json:"gates,omitempty"`
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

// createBundleReference is the detail of the command for its reference page.
const createBundleReference = `The pipeline name is a required positional argument; the Pipeline must exist.
An image or mixed Bundle needs at least one --image. A config or mixed Bundle
needs --config-commit, the commit of the config repository to promote;
--config-repo names that repository and defaults to the Pipeline's git.url.
An image Bundle with --config-repo or --config-commit is refused: it would
deploy only its images and ignore them.

--commit, --author and --ci-run-url set the Bundle's provenance, shown in the
PR body and the UI. kardinal records them as given: they are what the caller
asserts, as with the Bundle API.

The Bundle API (POST /api/v1/bundles) applies the same checks.

Use --dry-run to preview the promotion graph without creating any resources.`
