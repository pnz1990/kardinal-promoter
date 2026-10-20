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
	"io"
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
	pipelinerec "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/pipeline"
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
// Covers CLI-WHY-01, CLI-WHY-ONITSWAY-01.
func TestWhy_Golden(t *testing.T) {
	now := time.Date(2026, 10, 11, 9, 3, 0, 0, time.UTC)
	prev := whyNow
	whyNow = func() time.Time { return now }
	t.Cleanup(func() { whyNow = prev })
	prevNS := contextNamespace
	contextNamespace = func() string { return "default" } // the golden's commands need no -n
	t.Cleanup(func() { contextNamespace = prevNS })
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
		{"a soak gate: how far it soaked and when it opens", "prod", func(p *v1alpha1.Pipeline) []sigs_client.Object {
			p.Status.Environments = []v1alpha1.PipelineEnvironmentStatus{{Name: "prod", Bundle: "demo-a", Version: "1.4.0",
				IncomingBundle: "demo-b", IncomingVersion: "1.5.0", State: "Pending", BlockedBy: []string{"uat-soak"}}}
			g := explainGateInstance("demo", "demo-b", "prod", "uat-soak", "bundle.upstreamSoakMinutes >= 3", false, true,
				"bundle.upstreamSoakMinutes >= 3 = false; version 1.5.0")
			g.Spec.Message = "A version must run in uat for 3 minutes before prod"
			g.Spec.RecheckInterval = "30s"
			b := whyBundle("demo-b", "1.5.0")
			checked := metav1.NewTime(now.Add(-24 * time.Second))
			b.Status.Environments = []v1alpha1.EnvironmentStatus{{Name: "uat", Phase: "Verified", HealthCheckedAt: &checked}}
			return []sigs_client.Object{g, b}
		}, false},
		{"a soak gate before its upstream is live", "prod", func(p *v1alpha1.Pipeline) []sigs_client.Object {
			p.Status.Environments = []v1alpha1.PipelineEnvironmentStatus{{Name: "prod", Bundle: "demo-a", Version: "1.4.0",
				IncomingBundle: "demo-b", IncomingVersion: "1.5.0", State: "Pending", BlockedBy: []string{"uat-soak"}}}
			g := explainGateInstance("demo", "demo-b", "prod", "uat-soak", "upstream.uat.soakMinutes > 9", false, true,
				"upstream.uat.soakMinutes > 9 = false; version 1.5.0")
			return []sigs_client.Object{g, whyBundle("demo-b", "1.5.0")}
		}, false},
		{"a soak gate behind an approval", "prod", func(p *v1alpha1.Pipeline) []sigs_client.Object {
			p.Status.Environments = []v1alpha1.PipelineEnvironmentStatus{{Name: "prod", Bundle: "demo-a", Version: "1.4.0",
				IncomingBundle: "demo-b", IncomingVersion: "1.5.0", State: "Pending", BlockedBy: []string{"prod-approval", "uat-soak"}}}
			soak := explainGateInstance("demo", "demo-b", "prod", "uat-soak", "bundle.upstreamSoakMinutes >= 3", false, true,
				"bundle.upstreamSoakMinutes >= 3 = false; version 1.5.0")
			appr := explainGateInstance("demo", "demo-b", "prod", "prod-approval", "true", false, true, "waiting for approvals: 0 of 1")
			appr.Spec.Approval = &v1alpha1.GateApprovalPolicy{Required: 1}
			return []sigs_client.Object{soak, appr, whyBundle("demo-b", "1.5.0")}
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
			// The controller's record of the real Bundle: kro's error with the
			// expression echoed and its caret line, which failedVersion drops.
			b := whyBundle("demo-bj579", "sha-9349a3f")
			b.Status.Phase = "Failed"
			msg := `kro rejected the Graph: compile node "GateMirror": variable at "spec.live.gates" ("(PolicyGates + ApprovalGates).filter(...)"): ` +
				`ERROR: <input>:1:14: found no matching overload for '_+_' applied to '(list(__type_PolicyGates.@idx), list(__type_ApprovalGates.@idx))'` +
				"\n | (PolicyGates + ApprovalGates).filter(g, g.metadata.?labels[?\"kardinal.io/environment\"].orValue(\"\") == Mirror.environment)\n | .............^"
			b.Status.Conditions = []metav1.Condition{{Type: "Ready", Status: "False", Reason: "Failed", Message: "promotion failed: " + msg},
				{Type: "Failed", Status: "True", Reason: "GraphRejected", Message: msg}}
			p.Status.FailedVersion = pipelinerec.FailedVersion("demo", []v1alpha1.Bundle{*b})
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
		// #1940: a newer version still upstream is on its way.
		{"nothing waits here, a newer version is on its way", "prod", func(p *v1alpha1.Pipeline) []sigs_client.Object {
			p.Status.Environments = []v1alpha1.PipelineEnvironmentStatus{
				{Name: "test", Bundle: "demo-b", Version: "1.5.0", State: "Verified"},
				{Name: "prod", Bundle: "demo-a", Version: "1.4.0", State: "Verified"}}
			a, b := whyBundle("demo-a", "1.4.0"), whyBundle("demo-b", "1.5.0")
			a.Status.Phase = "Verified"
			return []sigs_client.Object{a, b}
		}, false},
		// #1940: a ChangeWindow gate says when its window opens, to the minute.
		{"a ChangeWindow gate", "prod", func(p *v1alpha1.Pipeline) []sigs_client.Object {
			p.Status.Environments = []v1alpha1.PipelineEnvironmentStatus{{Name: "prod", Bundle: "demo-a", Version: "1.4.0",
				IncomingBundle: "demo-b", IncomingVersion: "1.5.0", State: "Pending", BlockedBy: []string{"prod-hours"}}}
			g := explainGateInstance("demo", "demo-b", "prod", "prod-hours", `changewindow.isAllowed("prod-hours")`, false, true,
				`changewindow.isAllowed("prod-hours") = false`)
			cw := &v1alpha1.ChangeWindow{ObjectMeta: metav1.ObjectMeta{Name: "prod-hours"},
				Spec: v1alpha1.ChangeWindowSpec{Type: "recurring", Schedule: &v1alpha1.ChangeWindowSchedule{AllowedHours: "09:30-17:00"}}}
			return []sigs_client.Object{g, cw}
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
			// The failed-version sentence quotes the controller's message,
			// kinds and all; every other line is in the CLI's words.
			var own []string
			for _, l := range strings.Split(out.String(), "\n") {
				if !strings.HasPrefix(l, "The newest version ") {
					own = append(own, l)
				}
			}
			for _, kind := range []string{"PolicyGate", "PromotionStep", "Bundle "} {
				assert.NotContains(t, strings.Join(own, "\n"), kind, "no kind names")
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

// TestWhy_HintsNameTheNamespace (#1940): a command why and explain print
// names -n <ns> when the pipeline's namespace is not the kubeconfig
// context's, as kardinal inbox does, so it runs as is when copied; in the
// context's namespace it does not.
func TestWhy_HintsNameTheNamespace(t *testing.T) {
	stubIdentity(t, Identity{Username: "kubernetes-admin"})
	setup := func() sigs_client.Client {
		p := policyPipeline("demo", "test", "prod")
		p.Status.Environments = []v1alpha1.PipelineEnvironmentStatus{{Name: "prod", Bundle: "demo-a", Version: "1.4.0",
			IncomingBundle: "demo-b", IncomingVersion: "1.5.0", State: "Pending", BlockedBy: []string{"prod-approval", "soak"}}}
		p.Spec.Holds = nil
		approval := explainGateInstance("demo", "demo-b", "prod", "prod-approval", "true", false, true, "waiting for approvals: 0 of 1")
		approval.Spec.Approval = &v1alpha1.GateApprovalPolicy{Required: 1}
		soak := explainGateInstance("demo", "demo-b", "prod", "soak", "schedule.hour >= 9", false, true, "outside hours")
		at := time.Now().Add(-time.Hour)
		return policyClient(t, p, approval, soak, explainBundle("demo-b", "Promoting", at), explainStep("demo", "demo-b", "test", "Verified", "", at))
	}
	commands := func(out string) []string {
		var cmds []string
		for _, l := range strings.Split(out, "\n") {
			if i := strings.Index(l, "kardinal "); i >= 0 {
				cmds = append(cmds, strings.TrimSpace(l[i:]))
			}
		}
		return cmds
	}
	prev := contextNamespace
	t.Cleanup(func() { contextNamespace = prev })
	for _, here := range []string{"elsewhere", "default"} {
		contextNamespace = func() string { return here }
		c := setup()
		var why, table, explain bytes.Buffer
		setForTest(t, &errOut, io.Writer(&explain)) // explain's hints go to stderr
		require.NoError(t, whyFn(context.Background(), &why, c, "default", "demo", "prod"))
		require.NoError(t, explainHinted(t.Context(), &table, c, "default", "demo", "prod", false))
		require.NotEmpty(t, commands(explain.String()), "explain hints:\n%s", explain.String())
		cmds := append(commands(why.String()), commands(explain.String())...)
		require.NotEmpty(t, cmds, "why:\n%s\nexplain:\n%s", why.String(), explain.String())
		for _, cmd := range cmds {
			if here == "default" {
				assert.NotContains(t, cmd, " -n ", "in the context's namespace: %s", cmd)
			} else {
				assert.Contains(t, cmd, " -n default", "outside the context's namespace: %s", cmd)
			}
		}
	}
}

// TestWhy_StatusLagsTheSteps (#2022): the controller writes a step's move
// between running states into status.environments up to 2 seconds late
// (#1719). why answers from the steps it reads, so in that window it never
// says an environment that is promoting has no version, or runs an older
// one with nothing waiting. An environment without a step keeps its status
// entry (a fleet's targets are the controller's).
func TestWhy_StatusLagsTheSteps(t *testing.T) {
	stubIdentity(t, Identity{Username: "kubernetes-admin"})
	prev := contextNamespace
	contextNamespace = func() string { return "default" }
	t.Cleanup(func() { contextNamespace = prev })
	at := time.Now().Add(-time.Hour)
	agentBundle := func() *v1alpha1.Bundle {
		b := whyBundle("demo-b", "1.5.0")
		b.Spec.Actor = &v1alpha1.Actor{Kind: v1alpha1.ActorAgent, Principal: "system:serviceaccount:ci:fixer",
			Agent: "fixer", Sponsor: "alice"}
		return b
	}
	cases := []struct {
		name   string
		status []v1alpha1.PipelineEnvironmentStatus
		objs   func() []sigs_client.Object
		want   []string
		reject string
	}{
		{name: "the PR opened, status not written yet",
			status: []v1alpha1.PipelineEnvironmentStatus{{Name: "test", Bundle: "demo-b", Version: "1.5.0", State: "Verified"}, {Name: "prod"}},
			objs: func() []sigs_client.Object {
				pr := explainStep("demo", "demo-b", "prod", "WaitingForMerge", "", at)
				pr.Status.PRURL = "https://github.com/acme/gitops/pull/1"
				return []sigs_client.Object{agentBundle(), explainStep("demo", "demo-b", "test", "Verified", "", at), pr}
			},
			want:   []string{"prod waits for PR #1 to merge.", "1.5.0 (demo-b), by agent fixer for alice"},
			reject: "has no version yet"},
		{name: "promoting over an older version, status says it runs it",
			status: []v1alpha1.PipelineEnvironmentStatus{{Name: "test", Bundle: "demo-a", Version: "1.4.0", State: "Verified"}, {Name: "prod"}},
			objs: func() []sigs_client.Object {
				old := whyBundle("demo-a", "1.4.0")
				old.CreationTimestamp = metav1.NewTime(at.Add(-time.Hour))
				old.Status.Phase = "Verified"
				return []sigs_client.Object{old, agentBundle(),
					explainStep("demo", "demo-a", "test", "Verified", "", at.Add(-time.Hour)),
					explainStep("demo", "demo-b", "test", "HealthChecking", "", at)}
			},
			want:   []string{"test is promoting 1.5.0 (demo-b): checking health."},
			reject: "nothing is waiting"},
		{name: "an environment without a step keeps its status entry",
			status: []v1alpha1.PipelineEnvironmentStatus{{Name: "test", Bundle: "demo-b", Version: "1.5.0", State: "Verified"},
				{Name: "prod", Bundle: "demo-a", Version: "1.4.0", State: "Verified"}},
			objs: func() []sigs_client.Object {
				return []sigs_client.Object{agentBundle(), explainStep("demo", "demo-b", "test", "Verified", "", at)}
			},
			want: []string{"prod runs 1.4.0"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := policyPipeline("demo", "test", "prod")
			p.Status.Environments = tc.status
			c := policyClient(t, append([]sigs_client.Object{p}, tc.objs()...)...)
			var out bytes.Buffer
			require.NoError(t, whyFn(context.Background(), &out, c, "default", "demo", ""))
			for _, w := range tc.want {
				assert.Contains(t, out.String(), w)
			}
			if tc.reject != "" {
				assert.NotContains(t, out.String(), tc.reject)
			}
		})
	}
}
