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
		Args:  cobra.ExactArgs(2),
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

// FormatDiffTable writes a tabwriter-formatted artifact diff table comparing
// two Bundles. The format matches docs/cli-reference.md.
//
// Example output:
//
//	ARTIFACT                     BUNDLE-A (v1.28.0)    BUNDLE-B (v1.29.0)
//	ghcr.io/myorg/my-app         1.28.0                1.29.0
//	  digest                     sha256:def456...       sha256:abc123...
//	  commit                     def456                 abc123
//	  author                     dependabot[bot]        engineer-name
func FormatDiffTable(w io.Writer, a, b *v1alpha1.Bundle) error {
	tw := tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)

	// Print header with bundle names.
	if _, err := fmt.Fprintf(tw, "ARTIFACT\tBUNDLE-A (%s)\tBUNDLE-B (%s)\n", a.Name, b.Name); err != nil {
		return fmt.Errorf("write diff header: %w", err)
	}

	// Build image maps keyed by repository.
	aImages := make(map[string]v1alpha1.ImageRef)
	for _, img := range a.Spec.Images {
		aImages[img.Repository] = img
	}
	bImages := make(map[string]v1alpha1.ImageRef)
	for _, img := range b.Spec.Images {
		bImages[img.Repository] = img
	}

	// Collect all repositories in deterministic order:
	// images in A first (in spec order), then images only in B.
	seen := make(map[string]bool)
	var repos []string
	for _, img := range a.Spec.Images {
		if !seen[img.Repository] {
			repos = append(repos, img.Repository)
			seen[img.Repository] = true
		}
	}
	for _, img := range b.Spec.Images {
		if !seen[img.Repository] {
			repos = append(repos, img.Repository)
			seen[img.Repository] = true
		}
	}

	for _, repo := range repos {
		aImg := aImages[repo] // zero value if absent
		bImg := bImages[repo] // zero value if absent

		aTag := aImg.Tag
		if aTag == "" {
			aTag = "(absent)"
		}
		bTag := bImg.Tag
		if bTag == "" {
			bTag = "(absent)"
		}
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\n", repo, aTag, bTag); err != nil {
			return fmt.Errorf("write diff row: %w", err)
		}

		// Sub-rows for digest.
		aDigest := truncDigest(aImg.Digest)
		bDigest := truncDigest(bImg.Digest)
		if aDigest != "" || bDigest != "" {
			if aDigest == "" {
				aDigest = "--"
			}
			if bDigest == "" {
				bDigest = "--"
			}
			if _, err := fmt.Fprintf(tw, "  digest\t%s\t%s\n", aDigest, bDigest); err != nil {
				return fmt.Errorf("write diff digest row: %w", err)
			}
		}
	}

	// Provenance diff (commit + author).
	aCommit := commitSHA(a)
	bCommit := commitSHA(b)
	if aCommit != "" || bCommit != "" {
		if aCommit == "" {
			aCommit = "--"
		}
		if bCommit == "" {
			bCommit = "--"
		}
		if _, err := fmt.Fprintf(tw, "  commit\t%s\t%s\n", aCommit, bCommit); err != nil {
			return fmt.Errorf("write diff commit row: %w", err)
		}
	}

	aAuthor := author(a)
	bAuthor := author(b)
	if aAuthor != "" || bAuthor != "" {
		if aAuthor == "" {
			aAuthor = "--"
		}
		if bAuthor == "" {
			bAuthor = "--"
		}
		if _, err := fmt.Fprintf(tw, "  author\t%s\t%s\n", aAuthor, bAuthor); err != nil {
			return fmt.Errorf("write diff author row: %w", err)
		}
	}

	if err := tw.Flush(); err != nil {
		return fmt.Errorf("flush diff table: %w", err)
	}
	return nil
}

// truncDigest returns the first 15 chars of a digest with trailing ellipsis,
// or the full digest if shorter. Returns "" for an empty input.
func truncDigest(d string) string {
	if d == "" {
		return ""
	}
	if len(d) > 15 {
		return d[:15] + "..."
	}
	return d
}

// commitSHA returns the source commit SHA from the bundle's provenance (if any).
func commitSHA(b *v1alpha1.Bundle) string {
	if b.Spec.ConfigRef != nil && b.Spec.ConfigRef.CommitSHA != "" {
		return b.Spec.ConfigRef.CommitSHA[:min(8, len(b.Spec.ConfigRef.CommitSHA))]
	}
	if b.Spec.Provenance != nil && b.Spec.Provenance.CommitSHA != "" {
		return b.Spec.Provenance.CommitSHA[:min(8, len(b.Spec.Provenance.CommitSHA))]
	}
	return ""
}

// author returns the bundle author from provenance (if any).
func author(b *v1alpha1.Bundle) string {
	if b.Spec.Provenance != nil {
		return b.Spec.Provenance.Author
	}
	return ""
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
