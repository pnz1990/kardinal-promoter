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

package cmd

import (
	"context"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/types"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

func newDiffCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "diff <bundle-a> <bundle-b>",
		Short: "Show artifact differences between two Bundles",
		Long: `Diff compares the images and provenance of two Bundles in the namespace.

Images are matched by repository. A tag cell reads "(absent)" when the Bundle
has no image for that repository, and "-" when the image is pinned by digest
only. CHANGED is "*" on rows that differ. Commit and author are listed under
PROVENANCE.`,
		Example: `  kardinal diff my-app-sha-abc1234 my-app-sha-def5678`,
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, ns, err := buildClient()
			if err != nil {
				return fmt.Errorf("diff: %w", err)
			}
			return diffFn(cmd.OutOrStdout(), c, ns, args[0], args[1])
		},
	}
}

// diffFn is the testable implementation of the diff command.
// It compares images and provenance between two Bundle CRDs.
func diffFn(w io.Writer, c sigs_client.Client, ns, nameA, nameB string) error {
	ctx := context.Background()

	var bA v1alpha1.Bundle
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: nameA}, &bA); err != nil {
		return fmt.Errorf("get bundle %q: %w", nameA, err)
	}

	var bB v1alpha1.Bundle
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: nameB}, &bB); err != nil {
		return fmt.Errorf("get bundle %q: %w", nameB, err)
	}

	return FormatDiffTable(w, &bA, &bB)
}

// FormatDiffTable writes a table comparing the images and provenance of two
// Bundles. Images are matched by repository (the n-th image of a repository
// in A with the n-th in B). A tag cell is "(absent)" when the Bundle has no
// such image and "-" when the image has no tag (digest only). CHANGED is "*"
// on rows whose values differ.
//
// Example output:
//
//	ARTIFACT               BUNDLE-A (v1.28.0)   BUNDLE-B (v1.29.0)   CHANGED
//	ghcr.io/myorg/my-app   1.28.0               1.29.0               *
//	  digest               sha256:def456...     sha256:abc123...     *
//	PROVENANCE
//	  commit               def456ab             abc123cd             *
//	  author               dependabot[bot]      engineer-name        *
func FormatDiffTable(w io.Writer, a, b *v1alpha1.Bundle) error {
	var buf strings.Builder
	tw := tabwriter.NewWriter(&buf, 0, 0, 3, ' ', 0)
	row := func(label, av, bv string, changed bool) {
		mark := ""
		if changed {
			mark = "*"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", label, av, bv, mark)
	}
	_, _ = fmt.Fprintf(tw, "ARTIFACT\tBUNDLE-A (%s)\tBUNDLE-B (%s)\tCHANGED\n", a.Name, b.Name)

	for _, p := range pairImages(a.Spec.Images, b.Spec.Images) {
		aTag, bTag := imageTagCell(p.a), imageTagCell(p.b)
		row(p.repo, aTag, bTag, aTag != bTag)
		aDigest, bDigest := imageDigest(p.a), imageDigest(p.b)
		if aDigest != "" || bDigest != "" {
			row("  digest", orDashes(truncDigest(aDigest)), orDashes(truncDigest(bDigest)), aDigest != bDigest)
		}
	}

	aCommit, bCommit := bundleCommitShort(a), bundleCommitShort(b)
	aAuthor, bAuthor := bundleAuthor(a), bundleAuthor(b)
	if aCommit != "" || bCommit != "" || aAuthor != "" || bAuthor != "" {
		_, _ = fmt.Fprint(tw, "PROVENANCE\t\t\t\n")
		if aCommit != "" || bCommit != "" {
			row("  commit", orDashes(aCommit), orDashes(bCommit), aCommit != bCommit)
		}
		if aAuthor != "" || bAuthor != "" {
			row("  author", orDashes(aAuthor), orDashes(bAuthor), aAuthor != bAuthor)
		}
	}

	if err := tw.Flush(); err != nil {
		return fmt.Errorf("flush diff table: %w", err)
	}
	// tabwriter pads empty trailing cells; trim them. Every row ends in "\n".
	var out strings.Builder
	for _, l := range strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n") {
		out.WriteString(strings.TrimRight(l, " ") + "\n")
	}
	if _, err := io.WriteString(w, out.String()); err != nil {
		return fmt.Errorf("write diff table: %w", err)
	}
	return nil
}

// imagePair is one ARTIFACT row: an image of A and/or B with the same repository.
type imagePair struct {
	repo string
	a, b *v1alpha1.ImageRef
}

// pairImages matches the n-th image of a repository in a with the n-th image
// of that repository in b. Order: a's images, then images only in b.
func pairImages(a, b []v1alpha1.ImageRef) []imagePair {
	var pairs []imagePair
	byRepo := make(map[string][]int) // repository -> indexes into pairs still unmatched in b
	for i := range a {
		byRepo[a[i].Repository] = append(byRepo[a[i].Repository], len(pairs))
		pairs = append(pairs, imagePair{repo: a[i].Repository, a: &a[i]})
	}
	for i := range b {
		repo := b[i].Repository
		if idx := byRepo[repo]; len(idx) > 0 {
			pairs[idx[0]].b = &b[i]
			byRepo[repo] = idx[1:]
			continue
		}
		pairs = append(pairs, imagePair{repo: repo, b: &b[i]})
	}
	return pairs
}

func imageTagCell(img *v1alpha1.ImageRef) string {
	switch {
	case img == nil:
		return "(absent)"
	case img.Tag == "":
		return "-"
	default:
		return img.Tag
	}
}

func imageDigest(img *v1alpha1.ImageRef) string {
	if img == nil {
		return ""
	}
	return img.Digest
}

func orDashes(s string) string {
	if s == "" {
		return "--"
	}
	return s
}

// truncDigest returns the first 15 chars of a digest with trailing ellipsis,
// or the full digest if shorter. Returns "" for an empty input.
func truncDigest(d string) string {
	if len(d) > 15 {
		return d[:15] + "..."
	}
	return d
}

// bundleCommitShort returns the first 8 characters of the Bundle's config
// commit, or else of its source commit.
func bundleCommitShort(b *v1alpha1.Bundle) string {
	sha := ""
	switch {
	case b.Spec.ConfigRef != nil && b.Spec.ConfigRef.CommitSHA != "":
		sha = b.Spec.ConfigRef.CommitSHA
	case b.Spec.Provenance != nil:
		sha = b.Spec.Provenance.CommitSHA
	}
	return sha[:min(8, len(sha))]
}

// bundleAuthor returns the Bundle author from provenance (if any).
func bundleAuthor(b *v1alpha1.Bundle) string {
	if b.Spec.Provenance != nil {
		return b.Spec.Provenance.Author
	}
	return ""
}
