//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
)

// walkBlock classifies the bash blocks of a walked page: the blocks that
// contain match (or, with exact, are exactly match) are run by the
// walkthrough, or skipped for the reason skip gives. Every bash block of a
// walked page must match exactly one entry, so a block added to a page fails
// the walkthrough until it is run or skipped here.
type walkBlock struct {
	match string
	exact bool
	skip  string
}

var walkManifest = map[string][]walkBlock{
	"quickstart.md": {
		{match: "uname -s", skip: "installs a released CLI; the suite runs this checkout's"},
		{match: "kind create cluster --name kardinal", skip: "the suite has its cluster, and the fork is a Forgejo repo laid out as kardinal-demo"},
		{match: "kardinal quickstart --repo"},
		{match: "kardinal why demo prod"},
		// Both approve blocks: the first runs; approvals.md's approve stands
		// in for the second, on the version ship --wait stopped.
		{match: "kardinal approve demo prod"},
		{match: "kardinal status demo"},
		{match: "kardinal dashboard", skip: "port-forwards and opens a browser until Ctrl-C"},
		{match: "kardinal ship demo --image"},
		{match: "echo $?"},
		{match: "kind delete cluster", skip: "the suite deletes its cluster"},
	},
	"approvals.md": {
		{match: "kardinal inbox"},
		{match: "kardinal approve my-app prod --comment"},
		{match: "kardinal approve my-app-v1-29-0", skip: "names a Bundle the page invents (my-app-v1-29-0)"},
	},
	"rollback.md": {
		{match: "kardinal rollback my-app prod\n", exact: true},
		{match: "kardinal rollback my-app --env prod\n", exact: true, skip: "the same rollback, with --env"},
		{match: "--to my-app-v1-27-0", skip: "names versions the page invents"},
		{match: `kardinal hold my-app prod --reason "INC-4521: freeze prod`},
		{match: "kardinal rollback my-app prod --hold", skip: "a second rollback; hold and unhold run above"},
		{match: "kardinal rollback my-app --env prod --hold", skip: "a second rollback; hold and unhold run above"},
		{match: "holdStates", skip: "needs a hold whose rollback version is missing"},
		{match: "kardinal unhold my-app prod\n", exact: true, skip: "runs in the hold block above"},
		{match: "kubectl get rollbackpolicy", skip: "needs a RollbackPolicy that tripped"},
		{match: "kardinal override my-app --env prod --gate no-weekend-deploys", skip: "overrides a weekend gate on the page's my-app"},
		{match: "kardinal history my-app"},
	},
	"policy-gates.md": {
		{match: "name: uat-soak", skip: "a 5-minute soak; the walkthrough's gates are first-pipeline's"},
		{match: "kubectl get scheduleclock kardinal-clock"},
		// policy list, explain and simulate run; policy test needs a file the
		// page does not write.
		{match: "# List all PolicyGates"},
	},
	"pause.md": {
		{match: "kardinal pause my-app\n"},
		{match: "kardinal pause my-app --env prod"},
	},
	"agentic/connect-an-agent.md": {
		{match: "claude mcp add", skip: "configures an agent's own client"},
		{match: "kubectl config set-credentials release-agent"},
	},
	"first-pipeline.md": {
		{match: "kardinal version", skip: "shows v0.11.0; the suite runs this checkout's CLI and controller"},
		{match: "kind: ApplicationSet"},
		{match: "--from-literal=token=<your-github-pat>"},
		{match: "kardinal init --name kardinal-test-app"},
		{match: "kind: Pipeline", skip: "the same Pipeline by hand, instead of kardinal init"},
		{match: "kardinal-test-app   -         -"},
		{match: "kubectl logs -n kardinal-system", skip: "a troubleshooting tip"},
		{match: "kardinal create bundle kardinal-test-app --image $TEST_IMAGE"},
		{match: "kind: Bundle", skip: "the same version as YAML, instead of create bundle"},
		{match: "# Watch the pipeline status", skip: "a moment in the promotion; its states depend on timing"},
		{match: "kubectl get deployment kardinal-test-app -n kardinal-test-app-test", skip: "a troubleshooting tip"},
		{match: "wait-for-merge   In progress", skip: "a moment in the promotion; its states depend on timing"},
		{match: "Live   Live   Live"},
		{match: "name: no-weekend-deploys"},
		{match: "kardinal create bundle kardinal-test-app --image ghcr.io/pnz1990/kardinal-test-app:main"},
		{match: "kardinal explain kardinal-test-app --env prod"},
		{match: "kardinal policy simulate --pipeline kardinal-test-app"},
	},
}

// checkWalkManifest fails for a bash block of a walked page that no entry,
// or more than one, classifies, and for an entry that matches no block.
func checkWalkManifest(t *testing.T) {
	t.Helper()
	for page, entries := range walkManifest {
		used := make([]bool, len(entries))
		for i, block := range framework.PageBashBlocks(t, page) {
			var hits []int
			for j, en := range entries {
				if (en.exact && block == en.match) || (!en.exact && strings.Contains(block, en.match)) {
					hits = append(hits, j)
				}
			}
			if assert.Len(t, hits, 1, "docs/%s bash block %d: classify it in walkManifest (run, or skip with a reason); it matches %d entries:\n%s",
				page, i+1, len(hits), block) {
				used[hits[0]] = true
			}
		}
		for j, en := range entries {
			assert.True(t, used[j], "walkManifest entry %q of %s matches no block", en.match, page)
		}
	}
}
