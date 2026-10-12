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
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

var updateWhy = flag.Bool("update-why", false, "rewrite testdata/why.golden")

// whyBundle is a Bundle of pipeline demo shipping app:tag.
func whyBundle(name, tag string) *v1alpha1.Bundle {
	b := explainBundle(name, "Promoting", time.Now().Add(-time.Hour))
	b.Spec.Images = []v1alpha1.ImageRef{{Repository: "ghcr.io/acme/app", Tag: tag}}
	return b
}

// TestWhy_Golden (C7): why answers each case in a sentence, then the
// version, what holds it and the command that moves it on; no kind names.
//
// Covers CLI-WHY-01.
func TestWhy_Golden(t *testing.T) {
	now := time.Date(2026, 10, 11, 9, 3, 0, 0, time.UTC)
	prev := whyNow
	whyNow = func() time.Time { return now }
	t.Cleanup(func() { whyNow = prev })
	since := metav1.NewTime(now.Add(-90 * time.Second))
	stubIdentity(t, Identity{Username: "kubernetes-admin", Groups: []string{"system:masters", "system:authenticated"}})
	cases := []struct {
		name  string
		env   string
		setup func(p *v1alpha1.Pipeline) []sigs_client.Object
		// whoamiFails: the SelfSubjectReview fails, the caller is unknown.
		whoamiFails bool
	}{
		{"approval", "prod", func(p *v1alpha1.Pipeline) []sigs_client.Object {
			p.Status.Environments = []v1alpha1.PipelineEnvironmentStatus{{Name: "prod", IncomingBundle: "demo-zhxk2", IncomingVersion: "sha-9349a3f",
				State: "Pending", BlockedBy: []string{"prod-approval"}}}
			g := explainGateInstance("demo", "demo-zhxk2", "prod", "prod-approval", "true", false, true, "prod waits for your approval (waiting for approvals: 0 of 1; version sha-9349a3f)")
			g.Spec.Approval = &v1alpha1.GateApprovalPolicy{Required: 1}
			return []sigs_client.Object{g}
		}, false},
		{"approval, the caller is allowed", "prod", func(p *v1alpha1.Pipeline) []sigs_client.Object {
			p.Status.Environments = []v1alpha1.PipelineEnvironmentStatus{{Name: "prod", IncomingBundle: "demo-zhxk2", IncomingVersion: "sha-9349a3f",
				State: "Pending", BlockedBy: []string{"prod-approval"}}}
			g := explainGateInstance("demo", "demo-zhxk2", "prod", "prod-approval", "true", false, true, "waiting for approvals: 0 of 1")
			g.Spec.Approval = &v1alpha1.GateApprovalPolicy{Required: 1, AllowedUsers: []string{"kubernetes-admin"}}
			return []sigs_client.Object{g}
		}, false},
		{"approval, the caller is in an allowed group", "prod", func(p *v1alpha1.Pipeline) []sigs_client.Object {
			p.Status.Environments = []v1alpha1.PipelineEnvironmentStatus{{Name: "prod", IncomingBundle: "demo-zhxk2", IncomingVersion: "sha-9349a3f",
				State: "Pending", BlockedBy: []string{"cab"}}}
			g := explainGateInstance("demo", "demo-zhxk2", "prod", "cab", "true", false, true, "waiting for approvals: 0 of 1")
			g.Spec.Approval = &v1alpha1.GateApprovalPolicy{Required: 1, AllowedUsers: []string{"alice"}, AllowedGroups: []string{"system:authenticated"}}
			return []sigs_client.Object{g}
		}, false},
		{"approval, the caller is unknown (SelfSubjectReview failed)", "prod", func(p *v1alpha1.Pipeline) []sigs_client.Object {
			p.Status.Environments = []v1alpha1.PipelineEnvironmentStatus{{Name: "prod", IncomingBundle: "demo-zhxk2", IncomingVersion: "sha-9349a3f",
				State: "Pending", BlockedBy: []string{"cab"}}}
			g := explainGateInstance("demo", "demo-zhxk2", "prod", "cab", "true", false, true, "waiting for approvals: 0 of 1")
			g.Spec.Approval = &v1alpha1.GateApprovalPolicy{Required: 1, AllowedGroups: []string{"rm"}}
			return []sigs_client.Object{g}
		}, true},
		{"approval, the version has no creator (excludeAuthor)", "prod", func(p *v1alpha1.Pipeline) []sigs_client.Object {
			p.Status.Environments = []v1alpha1.PipelineEnvironmentStatus{{Name: "prod", IncomingBundle: "demo-zhxk2", IncomingVersion: "sha-9349a3f",
				State: "Pending", BlockedBy: []string{"cab"}}}
			g := explainGateInstance("demo", "demo-zhxk2", "prod", "cab", "true", false, true, "excludeAuthor cannot be enforced")
			g.Spec.Approval = &v1alpha1.GateApprovalPolicy{Required: 1, ExcludeAuthor: true}
			return []sigs_client.Object{g, whyBundle("demo-zhxk2", "sha-9349a3f")}
		}, false},
		{"approval, a component created the version (excludeAuthor)", "prod", func(p *v1alpha1.Pipeline) []sigs_client.Object {
			p.Status.Environments = []v1alpha1.PipelineEnvironmentStatus{{Name: "prod", IncomingBundle: "demo-zhxk2", IncomingVersion: "sha-9349a3f",
				State: "Pending", BlockedBy: []string{"cab"}}}
			g := explainGateInstance("demo", "demo-zhxk2", "prod", "cab", "true", false, true, "excludeAuthor cannot be enforced")
			g.Spec.Approval = &v1alpha1.GateApprovalPolicy{Required: 1, ExcludeAuthor: true}
			b := whyBundle("demo-zhxk2", "sha-9349a3f")
			b.Annotations = map[string]string{"kardinal.io/created-by": "subscription:demo-images"}
			return []sigs_client.Object{g, b}
		}, false},
		{"approval by others", "prod", func(p *v1alpha1.Pipeline) []sigs_client.Object {
			p.Status.Environments = []v1alpha1.PipelineEnvironmentStatus{{Name: "prod", IncomingBundle: "demo-zhxk2", IncomingVersion: "sha-9349a3f",
				State: "Pending", BlockedBy: []string{"cab"}}}
			g := explainGateInstance("demo", "demo-zhxk2", "prod", "cab", "true", false, true, "waiting for approvals: 0 of 1")
			g.Spec.Approval = &v1alpha1.GateApprovalPolicy{Required: 1, AllowedUsers: []string{"alice", "bob"}, AllowedGroups: []string{"release-managers"}}
			return []sigs_client.Object{g}
		}, false},
		{"approval, the caller created the version", "prod", func(p *v1alpha1.Pipeline) []sigs_client.Object {
			p.Status.Environments = []v1alpha1.PipelineEnvironmentStatus{{Name: "prod", IncomingBundle: "demo-zhxk2", IncomingVersion: "sha-9349a3f",
				State: "Pending", BlockedBy: []string{"cab"}}}
			g := explainGateInstance("demo", "demo-zhxk2", "prod", "cab", "true", false, true, "waiting for approvals: 0 of 1")
			g.Spec.Approval = &v1alpha1.GateApprovalPolicy{Required: 1, ExcludeAuthor: true}
			b := whyBundle("demo-zhxk2", "sha-9349a3f")
			b.Annotations = map[string]string{"kardinal.io/created-by": "kubernetes-admin"}
			return []sigs_client.Object{g, b}
		}, false},
		{"approval with one of two", "prod", func(p *v1alpha1.Pipeline) []sigs_client.Object {
			p.Status.Environments = []v1alpha1.PipelineEnvironmentStatus{{Name: "prod", Bundle: "demo-a", Version: "1.4.0",
				IncomingBundle: "demo-b", IncomingVersion: "1.5.0", State: "Pending", BlockedBy: []string{"cab"}}}
			g := explainGateInstance("demo", "demo-b", "prod", "cab", "true", false, true, "waiting for approvals: 1 of 2")
			g.Spec.Approval = &v1alpha1.GateApprovalPolicy{Required: 2}
			// As the Graph copies them: alice's counts, eve's is for another version.
			g.Spec.Approvals = []v1alpha1.GateApproval{{User: "alice", Decision: "approve", Bundle: "demo-b", Environment: "prod"},
				{User: "eve", Decision: "approve", Bundle: "demo-a", Environment: "prod"}}
			g.Status.Approvals = []v1alpha1.GateApprovalStatus{{User: "alice", Decision: "approve", Counted: true}, {User: "eve", Decision: "approve"}}
			return []sigs_client.Object{g}
		}, false},
		{"approval, the caller approved already (the server's tally)", "prod", func(p *v1alpha1.Pipeline) []sigs_client.Object {
			p.Status.Environments = []v1alpha1.PipelineEnvironmentStatus{{Name: "prod", Bundle: "demo-a", Version: "1.4.0",
				IncomingBundle: "demo-b", IncomingVersion: "1.5.0", State: "Pending", BlockedBy: []string{"cab"}}}
			g := explainGateInstance("demo", "demo-b", "prod", "cab", "true", false, true, "waiting for approvals: 1 of 2")
			g.Spec.Approval = &v1alpha1.GateApprovalPolicy{Required: 2}
			g.Spec.Approvals = []v1alpha1.GateApproval{{User: "kubernetes-admin", Decision: "approve", Bundle: "demo-b", Environment: "prod"}}
			return []sigs_client.Object{g}
		}, false},
		{"approval, rejected by two", "prod", func(p *v1alpha1.Pipeline) []sigs_client.Object {
			p.Status.Environments = []v1alpha1.PipelineEnvironmentStatus{{Name: "prod", Bundle: "demo-a", Version: "1.4.0",
				IncomingBundle: "demo-b", IncomingVersion: "1.5.0", State: "Pending", BlockedBy: []string{"cab"}}}
			g := explainGateInstance("demo", "demo-b", "prod", "cab", "true", false, true, "rejected")
			g.Spec.Approval = &v1alpha1.GateApprovalPolicy{Required: 1}
			g.Spec.Approvals = []v1alpha1.GateApproval{{User: "bob", Decision: "reject", Bundle: "demo-b", Environment: "prod"},
				{User: "alice", Decision: "reject", Bundle: "demo-b", Environment: "prod"}}
			return []sigs_client.Object{g}
		}, false},
		{"approval, too many Approvals", "prod", func(p *v1alpha1.Pipeline) []sigs_client.Object {
			p.Status.Environments = []v1alpha1.PipelineEnvironmentStatus{{Name: "prod", Bundle: "demo-a", Version: "1.4.0",
				IncomingBundle: "demo-b", IncomingVersion: "1.5.0", State: "Pending", BlockedBy: []string{"cab"}}}
			g := explainGateInstance("demo", "demo-b", "prod", "cab", "true", false, true, "more than 100 Approvals")
			g.Spec.Approval = &v1alpha1.GateApprovalPolicy{Required: 1}
			for i := 0; i <= 100; i++ {
				g.Spec.Approvals = append(g.Spec.Approvals, v1alpha1.GateApproval{User: fmt.Sprintf("u%d", i), Decision: "approve", Bundle: "demo-b", Environment: "prod"})
			}
			return []sigs_client.Object{g, whyBundle("demo-b", "1.5.0")}
		}, false},
		{"approval, held and rejected (held wins)", "prod", func(p *v1alpha1.Pipeline) []sigs_client.Object {
			p.Status.Environments = []v1alpha1.PipelineEnvironmentStatus{{Name: "prod", Bundle: "demo-a", Version: "1.4.0",
				IncomingBundle: "demo-b", IncomingVersion: "1.5.0", State: "Pending", BlockedBy: []string{"cab"}}}
			g := explainGateInstance("demo", "demo-b", "prod", "cab", "true", false, true, "excludeAuthor cannot be enforced")
			g.Spec.Approval = &v1alpha1.GateApprovalPolicy{Required: 1, ExcludeAuthor: true}
			g.Spec.Approvals = []v1alpha1.GateApproval{{User: "bob", Decision: "reject", Bundle: "demo-b", Environment: "prod"}}
			return []sigs_client.Object{g, whyBundle("demo-b", "1.5.0")}
		}, false},
		{"approval, the version not read, rejected", "prod", func(p *v1alpha1.Pipeline) []sigs_client.Object {
			p.Status.Environments = []v1alpha1.PipelineEnvironmentStatus{{Name: "prod", Bundle: "demo-a", Version: "1.4.0",
				IncomingBundle: "demo-gone", IncomingVersion: "1.6.0", State: "Pending", BlockedBy: []string{"cab"}}}
			g := explainGateInstance("demo", "demo-gone", "prod", "cab", "true", false, true, "rejected")
			g.Spec.Approval = &v1alpha1.GateApprovalPolicy{Required: 1, ExcludeAuthor: true}
			g.Spec.Approvals = []v1alpha1.GateApproval{{User: "bob", Decision: "reject", Bundle: "demo-gone", Environment: "prod"}}
			return []sigs_client.Object{g}
		}, false},
		{"a gate that opens by itself", "prod", func(p *v1alpha1.Pipeline) []sigs_client.Object {
			p.Status.Environments = []v1alpha1.PipelineEnvironmentStatus{{Name: "prod", Bundle: "demo-a", Version: "1.4.0",
				IncomingBundle: "demo-b", IncomingVersion: "1.5.0", State: "Pending", BlockedBy: []string{"no-weekend-deploys"}}}
			g := explainGateInstance("demo", "demo-b", "prod", "no-weekend-deploys", "!schedule.isWeekend", false, true,
				"Production deployments are blocked on weekends (!schedule.isWeekend = false; version 1.5.0)")
			g.Spec.Message = "Production deployments are blocked on weekends"
			g.Status.BundleVersion = "1.5.0"
			return []sigs_client.Object{g}
		}, false},
		{"a gate that needs a person", "prod", func(p *v1alpha1.Pipeline) []sigs_client.Object {
			p.Status.Environments = []v1alpha1.PipelineEnvironmentStatus{{Name: "prod", Bundle: "demo-a", Version: "1.4.0",
				IncomingBundle: "demo-b", IncomingVersion: "1.5.0", State: "Pending", BlockedBy: []string{"label"}}}
			g := explainGateInstance("demo", "demo-b", "prod", "label", `bundle.labels.approved == "yes"`, false, true,
				`bundle.labels.approved == "yes" = false (version 1.5.0)`)
			g.Spec.RecheckInterval = "1m"
			g.Status.BundleVersion = "1.5.0"
			return []sigs_client.Object{g}
		}, false},
		{"held", "prod", func(p *v1alpha1.Pipeline) []sigs_client.Object {
			p.Spec.Holds = []v1alpha1.EnvironmentHold{{Environment: "prod", Bundle: "demo-a", Reason: "INC-42", CreatedBy: "alice"}}
			p.Status.Environments = []v1alpha1.PipelineEnvironmentStatus{{Name: "prod", Bundle: "demo-a", Version: "1.4.0",
				IncomingBundle: "demo-b", IncomingVersion: "1.5.0", State: "Pending", Held: true}}
			return []sigs_client.Object{whyBundle("demo-a", "1.4.0")}
		}, false},
		{"paused", "prod", func(p *v1alpha1.Pipeline) []sigs_client.Object {
			p.Spec.Paused = true
			p.Status.Environments = []v1alpha1.PipelineEnvironmentStatus{{Name: "prod", Bundle: "demo-a", Version: "1.4.0",
				IncomingBundle: "demo-b", IncomingVersion: "1.5.0", State: "Pending"}}
			return nil
		}, false},
		{"its PR", "prod", func(p *v1alpha1.Pipeline) []sigs_client.Object {
			p.Status.Environments = []v1alpha1.PipelineEnvironmentStatus{{Name: "prod", Bundle: "demo-a", Version: "1.4.0",
				IncomingBundle: "demo-b", IncomingVersion: "1.5.0", State: "WaitingForMerge", PRURL: "https://github.com/acme/gitops/pull/42"}}
			return nil
		}, false},
		{"failed", "prod", func(p *v1alpha1.Pipeline) []sigs_client.Object {
			p.Status.Environments = []v1alpha1.PipelineEnvironmentStatus{{Name: "prod", Bundle: "demo-a", Version: "1.4.0",
				IncomingBundle: "demo-b", IncomingVersion: "1.5.0", State: "Failed"}}
			return []sigs_client.Object{explainStep("demo", "demo-b", "prod", "Failed", "health check timed out after 10m", time.Now())}
		}, false},
		{"the newest version failed before it started (the walkthrough's GateMirror)", "", func(p *v1alpha1.Pipeline) []sigs_client.Object {
			p.Status.Environments = []v1alpha1.PipelineEnvironmentStatus{{Name: "test", Bundle: "demo-a", Version: "sha-9349a3f", State: "Verified"},
				{Name: "prod", Bundle: "demo-a", Version: "sha-9349a3f", State: "Verified"}}
			p.Status.FailedVersion = &v1alpha1.PipelineFailedVersion{Bundle: "demo-bj579", Version: "sha-9349a3f", Reason: "GraphRejected",
				Message: `kro rejected the Graph: compile node "GateMirror": found no matching overload for '_+_'`}
			return nil
		}, false},
		{"the newest version's Graph could not be built", "prod", func(p *v1alpha1.Pipeline) []sigs_client.Object {
			p.Status.Environments = []v1alpha1.PipelineEnvironmentStatus{{Name: "prod", Bundle: "demo-a", Version: "1.4.0", State: "Verified"}}
			p.Status.FailedVersion = &v1alpha1.PipelineFailedVersion{Bundle: "demo-b", Version: "1.5.0", Reason: "GraphBuildFailed",
				Message: `build: gate "soak": unknown variable x — fix the Pipeline, Bundle or PolicyGate it names; a change to the Pipeline or its PolicyGates retries this Bundle`}
			return nil
		}, false},
		{"the newest version failed in uat; why prod", "prod", func(p *v1alpha1.Pipeline) []sigs_client.Object {
			p.Status.Environments = []v1alpha1.PipelineEnvironmentStatus{{Name: "prod", Bundle: "demo-a", Version: "1.4.0", State: "Verified"}}
			p.Status.FailedVersion = &v1alpha1.PipelineFailedVersion{Bundle: "demo-b", Version: "1.5.0", Reason: "StepFailed",
				Message: "environment uat: health check timeout", Environment: "uat"}
			return nil
		}, false},
		{"promoting", "prod", func(p *v1alpha1.Pipeline) []sigs_client.Object {
			p.Status.Environments = []v1alpha1.PipelineEnvironmentStatus{{Name: "prod", Bundle: "demo-a", Version: "1.4.0",
				IncomingBundle: "demo-b", IncomingVersion: "1.5.0", State: "HealthChecking", Since: &since}}
			return nil
		}, false},
		{"waits for upstream", "prod", func(p *v1alpha1.Pipeline) []sigs_client.Object {
			p.Status.Environments = []v1alpha1.PipelineEnvironmentStatus{
				{Name: "test", Bundle: "demo-b", Version: "1.5.0", State: "Verified"},
				{Name: "uat", Bundle: "demo-a", Version: "1.4.0", IncomingBundle: "demo-b", IncomingVersion: "1.5.0", State: "Promoting"},
				{Name: "prod", Bundle: "demo-a", Version: "1.4.0", IncomingBundle: "demo-b", IncomingVersion: "1.5.0", State: "Pending"}}
			return nil
		}, false},
		{"nothing waits", "prod", func(p *v1alpha1.Pipeline) []sigs_client.Object {
			p.Status.Environments = []v1alpha1.PipelineEnvironmentStatus{{Name: "prod", Bundle: "demo-b", Version: "1.5.0", State: "Verified"}}
			return nil
		}, false},
		{"no version yet", "prod", func(p *v1alpha1.Pipeline) []sigs_client.Object {
			p.Status.Environments = []v1alpha1.PipelineEnvironmentStatus{{Name: "prod"}}
			return nil
		}, false},
		{"every environment", "", func(p *v1alpha1.Pipeline) []sigs_client.Object {
			p.Status.Environments = []v1alpha1.PipelineEnvironmentStatus{
				{Name: "test", Bundle: "demo-b", Version: "1.5.0", State: "Verified"},
				{Name: "uat", Bundle: "demo-b", Version: "1.5.0", State: "Verified"},
				{Name: "prod", Bundle: "demo-a", Version: "1.4.0", IncomingBundle: "demo-b", IncomingVersion: "1.5.0",
					State: "WaitingForMerge", PRURL: "https://github.com/acme/gitops/pull/7"}}
			return nil
		}, false},
	}
	var golden strings.Builder
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.whoamiFails {
				old := identityOf
				identityOf = func(context.Context, sigs_client.Client) (Identity, error) {
					return Identity{}, fmt.Errorf("selfsubjectreviews is forbidden")
				}
				t.Cleanup(func() { identityOf = old })
			}
			p := policyPipeline("demo", "test", "uat", "prod")
			objs := append([]sigs_client.Object{p}, tc.setup(p)...)
			c := policyClient(t, objs...)
			var out bytes.Buffer
			require.NoError(t, whyFn(context.Background(), &out, c, "default", "demo", tc.env))
			for _, kind := range []string{"PolicyGate", "PromotionStep", "Bundle "} {
				assert.NotContains(t, out.String(), kind, "no kind names")
			}
			fmt.Fprintf(&golden, "== %s (kardinal why %s)\n%s\n", tc.name, strings.TrimSpace("demo "+tc.env), out.String())
		})
	}
	path := filepath.Join("testdata", "why.golden")
	if *updateWhy {
		require.NoError(t, os.WriteFile(path, []byte(golden.String()), 0o644))
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, string(want), golden.String())
}
