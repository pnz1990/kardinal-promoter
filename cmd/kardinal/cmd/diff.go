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
		Long: `Show artifact differences between two Bundles.

Compares images, digests, commits, and authors between two Bundle CRDs.

Example:
  kardinal diff nginx-demo-v1-28-0 nginx-demo-v1-29-0`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, ns, err := buildClient()
			if err != nil {
				return fmt.Errorf("diff: %w", err)
			}
			return diffFn(cmd.OutOrStdout(), c, ns, args[0], args[1])
		},
	}
}

// diffFn is the testable implementation of kardinal diff.
func diffFn(w interface{ Write([]byte) (int, error) }, c sigs_client.Client, ns, bundleA, bundleB string) error {
	ctx := context.Background()

	var a, b v1alpha1.Bundle
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: bundleA}, &a); err != nil {
		return fmt.Errorf("get bundle %q: %w", bundleA, err)
	}
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: bundleB}, &b); err != nil {
		return fmt.Errorf("get bundle %q: %w", bundleB, err)
	}

	rows := buildDiffRows(a, b)
	return formatDiffTable(w, bundleA, bundleB, rows)
}

// DiffRow represents one row in the diff output.
type DiffRow struct {
	Artifact string
	A        string
	B        string
}

// buildDiffRows builds a list of diff rows from two Bundles.
func buildDiffRows(a, b v1alpha1.Bundle) []DiffRow {
	var rows []DiffRow

	// Compare images.
	// Build a lookup of repository → ImageRef for each bundle.
	aImages := make(map[string]v1alpha1.ImageRef)
	for _, img := range a.Spec.Images {
		aImages[img.Repository] = img
	}
	bImages := make(map[string]v1alpha1.ImageRef)
	for _, img := range b.Spec.Images {
		bImages[img.Repository] = img
	}

	// Collect all repositories seen in either bundle, preserving order.
	repos := make([]string, 0)
	seen := make(map[string]bool)
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
		ai := aImages[repo]
		bi := bImages[repo]

		// Image row: show repository with tag.
		aTag := ai.Tag
		if aTag == "" {
			aTag = "(absent)"
		}
		bTag := bi.Tag
		if bTag == "" {
			bTag = "(absent)"
		}
		rows = append(rows, DiffRow{Artifact: repo, A: aTag, B: bTag})

		// Sub-rows: digest, commit (from provenance of the bundle, approximated per-image via tag).
		aDigest := ai.Digest
		if aDigest == "" {
			aDigest = "--"
		}
		bDigest := bi.Digest
		if bDigest == "" {
			bDigest = "--"
		}
		if aDigest != bDigest {
			rows = append(rows, DiffRow{
				Artifact: "  digest",
				A:        shortenDigest(aDigest),
				B:        shortenDigest(bDigest),
			})
		}
	}

	// Provenance diff rows.
	aP := provenance(a)
	bP := provenance(b)

	if aP.commit != bP.commit {
		rows = append(rows, DiffRow{Artifact: "  commit", A: aP.commit, B: bP.commit})
	}
	if aP.author != bP.author {
		rows = append(rows, DiffRow{Artifact: "  author", A: aP.author, B: bP.author})
	}
	if aP.ciRunURL != bP.ciRunURL && (aP.ciRunURL != "" || bP.ciRunURL != "") {
		rows = append(rows, DiffRow{Artifact: "  ci-run", A: aP.ciRunURL, B: bP.ciRunURL})
	}

	return rows
}

type provenanceData struct {
	commit   string
	author   string
	ciRunURL string
}

func provenance(b v1alpha1.Bundle) provenanceData {
	if b.Spec.Provenance == nil {
		return provenanceData{commit: "--", author: "--"}
	}
	p := b.Spec.Provenance
	commit := p.CommitSHA
	if commit == "" {
		commit = "--"
	}
	author := p.Author
	if author == "" {
		author = "--"
	}
	return provenanceData{commit: commit, author: author, ciRunURL: p.CIRunURL}
}

// shortenDigest truncates a sha256 digest to sha256:abc123... for display.
func shortenDigest(d string) string {
	if len(d) > 19 {
		return d[:19] + "..."
	}
	return d
}

// formatDiffTable writes the diff table to w.
func formatDiffTable(w io.Writer, bundleA, bundleB string, rows []DiffRow) error {
	tw := tabwriter.NewWriter(w, 0, 0, 4, ' ', 0)
	header := fmt.Sprintf("ARTIFACT\t%s\t%s", bundleA, bundleB)
	if _, err := fmt.Fprintln(tw, header); err != nil {
		return fmt.Errorf("write diff header: %w", err)
	}

	if len(rows) == 0 {
		if _, err := fmt.Fprintln(tw, "(no differences)"); err != nil {
			return fmt.Errorf("write diff empty: %w", err)
		}
		return tw.Flush()
	}

	for _, r := range rows {
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\n", r.Artifact, r.A, r.B); err != nil {
			return fmt.Errorf("write diff row: %w", err)
		}
	}
	return tw.Flush()
}
