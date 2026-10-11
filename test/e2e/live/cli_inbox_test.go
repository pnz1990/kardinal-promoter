//go:build e2e

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

package live

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
)

// TestCLI_Inbox runs kardinal inbox against two pipelines whose prod waits
// for an approval: one the caller may give (Waiting on you, with the approve
// command), one only carol may (Waiting on someone else, WHO carol). After
// the caller approves, theirs leaves the inbox; -A names each namespace.
// Covers CLI-INBOX-02.
func TestCLI_Inbox(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	c := e.CLI(t)
	me := whoAmI(t, e)
	approvalApp := func(allowed string) (*app, string) {
		a := newArgoApp(t, e, "test", "prod")
		a.apply(t, a.pipeline(nil))
		g := framework.Gate(a.ns, "cab", "prod", "true", "")
		g.Spec.Approval = &v1alpha1.GateApprovalPolicy{Required: 1, AllowedUsers: []string{allowed}}
		e.CreateGate(t, g)
		waitPipelineValid(t, e, a.ns, pipelineName)
		b := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2)
		e.WaitStepState(t, a.ns, pipelineName, b, "test", "Verified", promoteTimeout)
		return a, b
	}
	mine, bm := approvalApp(me)
	theirs, bt := approvalApp("carol")

	inbox := func(ns string) inboxView {
		t.Helper()
		var v inboxView
		require.NoError(t, json.Unmarshal([]byte(c.Must(ns, "inbox", "-o", "json")), &v))
		return v
	}
	framework.Eventually(t, gateTimeout, "both approvals in the inboxes", func(context.Context) (bool, string) {
		m, o := inbox(mine.ns), inbox(theirs.ns)
		return len(m.OnYou) == 1 && len(o.Others) == 1, c.Must(mine.ns, "inbox") + c.Must(theirs.ns, "inbox")
	})
	out := c.Must(mine.ns, "inbox")
	assert.True(t, strings.HasPrefix(out, "Waiting on you (1):\n"), out)
	// The commands name the namespace: it is not the kubeconfig's.
	assert.Regexp(t, `\n  podinfo +prod +`+fixtures.V2+` \(`+bm+`\) +approval at cab · 0 of 1 +kardinal approve podinfo prod -n `+mine.ns+`\n`, out)
	out = c.Must(theirs.ns, "inbox")
	assert.True(t, strings.HasPrefix(out, "Waiting on someone else (1):\n"), out)
	assert.Regexp(t, `\n  podinfo +prod +`+fixtures.V2+` \(`+bt+`\) +approval at cab · 0 of 1 +carol\n`, out)

	all := c.Must("", "inbox", "-A")
	assert.Contains(t, all, "kardinal approve podinfo prod -n "+mine.ns)
	assert.Contains(t, all, theirs.ns+"/podinfo")

	c.Must(mine.ns, "approve", pipelineName, "prod", "--yes")
	framework.Eventually(t, gateTimeout, "the approved one leaves the inbox", func(context.Context) (bool, string) {
		out := c.Must(mine.ns, "inbox")
		return !strings.Contains(out, "approval at cab"), out
	})

	// A hold and a pause the caller may release are on them.
	c.Must(mine.ns, "hold", pipelineName, "test", "--reason", "e2e freeze")
	out = c.Must(mine.ns, "inbox")
	assert.Regexp(t, `\n  podinfo +test +.* held on .* \(e2e freeze\) +kardinal unhold podinfo test -n `+mine.ns+`\n`, out)
	c.Must(mine.ns, "unhold", pipelineName, "test")
	c.Must(theirs.ns, "pause", pipelineName)
	out = c.Must(theirs.ns, "inbox")
	assert.Contains(t, out, "the pipeline is paused")
	assert.Contains(t, out, "kardinal resume podinfo -n "+theirs.ns)
	c.Must(theirs.ns, "resume", pipelineName)
}

// inboxView is kardinal inbox -o json.
type inboxView struct {
	OnYou  []map[string]any `json:"onYou"`
	Others []map[string]any `json:"others"`
}
