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
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

var updateInbox = flag.Bool("update-inbox", false, "rewrite testdata/inbox.golden")

// inboxCluster is a cluster with one of each thing that waits: approvals on
// the caller and on others (one the caller created under excludeAuthor), a
// gate that needs a person and one that opens by itself, a hold, a paused
// pipeline, a PR to merge and one the SCM merges.
func inboxCluster(t *testing.T) sigs_client.Client {
	t.Helper()
	pipe := func(name, ns string, envs ...v1alpha1.PipelineEnvironmentStatus) *v1alpha1.Pipeline {
		p := policyPipeline(name)
		p.Namespace = ns
		for _, e := range envs {
			p.Spec.Environments = append(p.Spec.Environments, v1alpha1.EnvironmentSpec{Name: e.Name})
		}
		p.Status.Environments = envs
		return p
	}
	gate := func(pipeline, bundle, env, name, expr string, approval *v1alpha1.GateApprovalPolicy) *v1alpha1.PolicyGate {
		g := explainGateInstance(pipeline, bundle, env, name, expr, false, true, "")
		g.Spec.Approval = approval
		return g
	}
	wait := func(env, bundle, version string, blockedBy ...string) v1alpha1.PipelineEnvironmentStatus {
		return v1alpha1.PipelineEnvironmentStatus{Name: env, IncomingBundle: bundle, IncomingVersion: version, State: "Pending", BlockedBy: blockedBy}
	}
	shop := pipe("shop", "default",
		wait("uat", "shop-b", "2.0.0", "soak"),
		wait("prod", "shop-b", "2.0.0", "cab"),
		wait("canary", "shop-b", "2.0.0", "release-managers"),
		wait("eu", "shop-b", "2.0.0", "labels"))
	mine := whyBundle("shop-b", "2.0.0")
	mine.Annotations = map[string]string{"kardinal.io/created-by": "alice"}
	web := pipe("web", "default",
		v1alpha1.PipelineEnvironmentStatus{Name: "prod", Bundle: "web-a", Version: "1.4.0", IncomingBundle: "web-b", IncomingVersion: "1.5.0",
			State: "WaitingForMerge", PRURL: "https://github.com/acme/gitops/pull/42"},
		v1alpha1.PipelineEnvironmentStatus{Name: "auto", IncomingBundle: "web-b", IncomingVersion: "1.5.0", State: "WaitingForMerge"})
	web.Spec.Environments[1].PR = &v1alpha1.PRConfig{Merge: &v1alpha1.PRMergeConfig{Auto: true}}
	held := pipe("api", "default", v1alpha1.PipelineEnvironmentStatus{Name: "prod", Bundle: "api-a", Version: "3.1.0", IncomingBundle: "api-b", IncomingVersion: "3.2.0", State: "Pending", Held: true})
	held.Spec.Holds = []v1alpha1.EnvironmentHold{{Environment: "prod", Bundle: "api-a", Reason: "INC-42", CreatedBy: "bob"}}
	paused := pipe("batch", "team-b", v1alpha1.PipelineEnvironmentStatus{Name: "prod", Bundle: "batch-a", Version: "0.9.0", State: "Verified"})
	paused.Spec.Paused = true
	return policyClient(t, shop, web, held, paused, mine, whyBundle("api-a", "3.1.0"),
		gate("shop", "shop-b", "uat", "soak", "upstream.test.soakMinutes >= 30", nil),
		gate("shop", "shop-b", "prod", "cab", "true", &v1alpha1.GateApprovalPolicy{Required: 2, AllowedGroups: []string{"release-managers"}}),
		gate("shop", "shop-b", "canary", "release-managers", "true", &v1alpha1.GateApprovalPolicy{Required: 1, ExcludeAuthor: true}),
		gate("shop", "shop-b", "eu", "labels", `bundle.labels.approved == "yes"`, nil))
}

// TestInbox_Golden (CLI UX 4): what waits for a person, on you first with
// the command that moves it on, then on others with who may; -A names the
// namespace in each command; -o json has both lists.
//
// Covers CLI-INBOX-01.
func TestInbox_Golden(t *testing.T) {
	c := inboxCluster(t)
	stubIdentity(t, Identity{Username: "alice", Groups: []string{"release-managers", "system:authenticated"}})
	prev := canI
	t.Cleanup(func() { canI = prev })
	// alice may approve and unhold in default, not override gates or resume
	// in team-b.
	canI = func(_ context.Context, _ sigs_client.Client, verb, resource, ns string) bool {
		return ns == "default" && (resource == "approvals" || (resource == "pipelines" && verb == "update"))
	}
	var golden bytes.Buffer
	for _, ns := range []string{"default", ""} {
		golden.WriteString("$ kardinal inbox" + map[string]string{"default": "", "": " -A"}[ns] + "\n")
		require.NoError(t, inboxFn(context.Background(), &golden, c, ns))
		golden.WriteString("\n")
	}
	path := filepath.Join("testdata", "inbox.golden")
	if *updateInbox {
		require.NoError(t, os.WriteFile(path, golden.Bytes(), 0o644))
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, string(want), golden.String())

	globalOutput = "json"
	t.Cleanup(func() { globalOutput = "" })
	var buf bytes.Buffer
	require.NoError(t, inboxFn(context.Background(), &buf, c, "default"))
	var out inboxOutput
	require.NoError(t, json.Unmarshal(buf.Bytes(), &out), buf.String())
	require.Len(t, out.OnYou, 2)
	assert.Equal(t, "approval", out.OnYou[1].Kind)
	assert.Equal(t, "kardinal approve shop prod", out.OnYou[1].Next)
	assert.Equal(t, "2.0.0", out.OnYou[1].Version)

	// Nothing waits.
	empty := policyClient(t, policyPipeline("quiet", "prod"))
	buf.Reset()
	globalOutput = ""
	require.NoError(t, inboxFn(context.Background(), &buf, empty, "default"))
	assert.Equal(t, "Nothing waits for a person.\n", buf.String())
}
