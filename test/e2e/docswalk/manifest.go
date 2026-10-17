// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Package docswalk is the block manifest of the docs walkthrough
// (test/e2e/live TestDocs_Walkthrough): for each page it walks, which bash
// blocks it runs and which it skips, and why. Check holds the pages to it, so
// a docs change that adds a block fails a unit test (test/examples) on the
// pull request, not only the live suite.
package docswalk

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Block classifies the bash blocks of a walked page: the blocks that
// contain match (or, with exact, are exactly match) are run by the
// walkthrough, or skipped for the reason skip gives. Every bash block of a
// walked page must match exactly one entry, so a block added to a page fails
// the walkthrough until it is run or skipped here.
type Block struct {
	// Match is text the block contains (with Exact, the whole block).
	Match string
	Exact bool
	// Skip is why the walkthrough does not run the block; empty: it runs.
	Skip string
}

// Manifest is the walkthrough's pages (paths under docs/) and their blocks.
var Manifest = map[string][]Block{
	"quickstart.md": {
		{Match: "uname -s", Skip: "installs a released CLI; the suite runs this checkout's"},
		{Match: "kind create cluster --name kardinal", Skip: "the suite has its cluster, and the fork is a Forgejo repo laid out as kardinal-demo"},
		{Match: "kardinal quickstart --repo"},
		{Match: "kardinal why demo prod"},
		// Both approve blocks: the first runs; approvals.md's approve stands
		// in for the second, on the version ship --wait stopped.
		{Match: "kardinal approve demo prod"},
		{Match: "kardinal status demo"},
		{Match: "kardinal dashboard", Skip: "port-forwards and opens a browser until Ctrl-C"},
		{Match: "kardinal ship demo --image"},
		{Match: "echo $?"},
		{Match: "kind delete cluster", Skip: "the suite deletes its cluster"},
	},
	"approvals.md": {
		{Match: "kardinal inbox"},
		{Match: "kardinal approve my-app prod --comment"},
		{Match: "kardinal approve my-app-v1-29-0", Skip: "names a Bundle the page invents (my-app-v1-29-0)"},
	},
	"rollback.md": {
		{Match: "kardinal rollback my-app prod\n", Exact: true},
		{Match: "kardinal rollback my-app --env prod\n", Exact: true, Skip: "the same rollback, with --env"},
		{Match: "--to my-app-v1-27-0", Skip: "names versions the page invents"},
		{Match: `kardinal hold my-app prod --reason "INC-4521: freeze prod`},
		{Match: "kardinal rollback my-app prod --hold", Skip: "a second rollback; hold and unhold run above"},
		{Match: "kardinal rollback my-app --env prod --hold", Skip: "a second rollback; hold and unhold run above"},
		{Match: "holdStates", Skip: "needs a hold whose rollback version is missing"},
		{Match: "kardinal unhold my-app prod\n", Exact: true, Skip: "runs in the hold block above"},
		{Match: "kubectl get rollbackpolicy", Skip: "needs a RollbackPolicy that tripped"},
		{Match: "kardinal override my-app --env prod --gate no-weekend-deploys", Skip: "overrides a weekend gate on the page's my-app"},
		{Match: "kardinal history my-app"},
	},
	"policy-gates.md": {
		{Match: "name: uat-soak", Skip: "a 5-minute soak; the walkthrough's gates are first-pipeline's"},
		{Match: "kubectl get scheduleclock kardinal-clock"},
		{Match: "gate-template-generation", Skip: "names a version by placeholder; TestGate_EditReachesInFlight reads the annotations"},
		// policy list, explain and simulate run; policy test needs a file the
		// page does not write.
		{Match: "# List all PolicyGates"},
	},
	"pause.md": {
		{Match: "kardinal pause my-app --reason"},
		{Match: "kubectl get auditevents -n <namespace>", Skip: "a lookup with the reader's own namespace"},
		{Match: "kardinal pause my-app --env prod"},
	},
	"agentic/connect-an-agent.md": {
		{Match: "claude mcp add", Skip: "configures an agent's own client"},
		{Match: "kubectl config set-credentials release-agent"},
	},
	"first-pipeline.md": {
		{Match: "kardinal version", Skip: "shows v0.12.0; the suite runs this checkout's CLI and controller"},
		{Match: "kind: ApplicationSet"},
		{Match: "kubectl wait --for=create -n argocd"},
		{Match: "--from-literal=token=<your-github-pat>"},
		{Match: "kardinal init --name kardinal-test-app"},
		{Match: "kind: Pipeline", Skip: "the same Pipeline by hand, instead of kardinal init"},
		{Match: "kardinal-test-app   -         -"},
		{Match: "kubectl logs -n kardinal-system", Skip: "a troubleshooting tip"},
		{Match: "kardinal create bundle kardinal-test-app --image $TEST_IMAGE"},
		{Match: "kind: Bundle", Skip: "the same version as YAML, instead of create bundle"},
		{Match: "# Watch the pipeline status", Skip: "a moment in the promotion; its states depend on timing"},
		{Match: "kubectl get deployment kardinal-test-app -n kardinal-test-app-test", Skip: "a troubleshooting tip"},
		{Match: "wait-for-merge   In progress", Skip: "a moment in the promotion; its states depend on timing"},
		{Match: "Live   Live   Live"},
		{Match: "name: no-weekend-deploys"},
		{Match: "kardinal create bundle kardinal-test-app --image ghcr.io/pnz1990/kardinal-test-app:main"},
		{Match: "no-weekend-deploys   Blocking"},
		{Match: "kardinal policy simulate --pipeline kardinal-test-app"},
	},
}

// BashBlocks returns the bodies of the ```bash blocks of a Markdown page, in
// order, each dedented by its fence's indent.
func BashBlocks(text string) []string {
	var out, cur []string
	lang, indent, in := "", "", false
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimLeft(line, " ")
		switch {
		case !in && strings.HasPrefix(trimmed, "```"):
			in, lang, indent, cur = true, strings.TrimPrefix(trimmed, "```"), line[:len(line)-len(trimmed)], nil
		case in && strings.TrimSpace(trimmed) == "```":
			in = false
			if lang == "bash" {
				out = append(out, strings.Join(cur, "\n")+"\n")
			}
		case in:
			cur = append(cur, strings.TrimPrefix(line, indent))
		}
	}
	return out
}

// Problems checks the pages under root's docs/ against Manifest: every bash
// block of a page must match exactly one entry, and every entry a block.
func Problems(root string) ([]string, error) {
	var out []string
	for page, entries := range Manifest {
		data, err := os.ReadFile(filepath.Join(root, "docs", page))
		if err != nil {
			return nil, fmt.Errorf("read docs/%s: %w", page, err)
		}
		used := make([]bool, len(entries))
		for i, block := range BashBlocks(string(data)) {
			var hits []int
			for j, en := range entries {
				if (en.Exact && block == en.Match) || (!en.Exact && strings.Contains(block, en.Match)) {
					hits = append(hits, j)
				}
			}
			for _, h := range hits {
				used[h] = true // an ambiguous block is reported once, not its entries too
			}
			if len(hits) != 1 {
				out = append(out, fmt.Sprintf("docs/%s bash block %d matches %d entries of test/e2e/docswalk Manifest, want 1: run it in the walkthrough, or list it with Skip and the reason:\n%s",
					page, i+1, len(hits), block))
			}
		}
		for j, en := range entries {
			if !used[j] {
				out = append(out, fmt.Sprintf("docs/%s: Manifest entry %q matches no block; remove it", page, en.Match))
			}
		}
	}
	return out, nil
}
