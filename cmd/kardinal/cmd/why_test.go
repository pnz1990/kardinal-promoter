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
	}{
		{"approval", "prod", func(p *v1alpha1.Pipeline) []sigs_client.Object {
			p.Status.Environments = []v1alpha1.PipelineEnvironmentStatus{{Name: "prod", IncomingBundle: "demo-zhxk2", IncomingVersion: "sha-9349a3f",
				State: "Pending", BlockedBy: []string{"prod-approval"}}}
			g := explainGateInstance("demo", "demo-zhxk2", "prod", "prod-approval", "true", false, true, "prod waits for your approval (bundle.version=sha-9349a3f: waiting for approvals: 0 of 1)")
			g.Spec.Approval = &v1alpha1.GateApprovalPolicy{Required: 1}
			return []sigs_client.Object{g}
		}},
		{"approval, the caller is allowed", "prod", func(p *v1alpha1.Pipeline) []sigs_client.Object {
			p.Status.Environments = []v1alpha1.PipelineEnvironmentStatus{{Name: "prod", IncomingBundle: "demo-zhxk2", IncomingVersion: "sha-9349a3f",
				State: "Pending", BlockedBy: []string{"prod-approval"}}}
			g := explainGateInstance("demo", "demo-zhxk2", "prod", "prod-approval", "true", false, true, "waiting for approvals: 0 of 1")
			g.Spec.Approval = &v1alpha1.GateApprovalPolicy{Required: 1, AllowedUsers: []string{"kubernetes-admin"}}
			return []sigs_client.Object{g}
		}},
		{"approval by others", "prod", func(p *v1alpha1.Pipeline) []sigs_client.Object {
			p.Status.Environments = []v1alpha1.PipelineEnvironmentStatus{{Name: "prod", IncomingBundle: "demo-zhxk2", IncomingVersion: "sha-9349a3f",
				State: "Pending", BlockedBy: []string{"cab"}}}
			g := explainGateInstance("demo", "demo-zhxk2", "prod", "cab", "true", false, true, "waiting for approvals: 0 of 1")
			g.Spec.Approval = &v1alpha1.GateApprovalPolicy{Required: 1, AllowedUsers: []string{"alice", "bob"}, AllowedGroups: []string{"release-managers"}}
			return []sigs_client.Object{g}
		}},
		{"approval, the caller created the version", "prod", func(p *v1alpha1.Pipeline) []sigs_client.Object {
			p.Status.Environments = []v1alpha1.PipelineEnvironmentStatus{{Name: "prod", IncomingBundle: "demo-zhxk2", IncomingVersion: "sha-9349a3f",
				State: "Pending", BlockedBy: []string{"cab"}}}
			g := explainGateInstance("demo", "demo-zhxk2", "prod", "cab", "true", false, true, "waiting for approvals: 0 of 1")
			g.Spec.Approval = &v1alpha1.GateApprovalPolicy{Required: 1, ExcludeAuthor: true}
			b := whyBundle("demo-zhxk2", "sha-9349a3f")
			b.Annotations = map[string]string{"kardinal.io/created-by": "kubernetes-admin"}
			return []sigs_client.Object{g, b}
		}},
		{"approval with one of two", "prod", func(p *v1alpha1.Pipeline) []sigs_client.Object {
			p.Status.Environments = []v1alpha1.PipelineEnvironmentStatus{{Name: "prod", Bundle: "demo-a", Version: "1.4.0",
				IncomingBundle: "demo-b", IncomingVersion: "1.5.0", State: "Pending", BlockedBy: []string{"cab"}}}
			g := explainGateInstance("demo", "demo-b", "prod", "cab", "true", false, true, "waiting for approvals: 1 of 2")
			g.Spec.Approval = &v1alpha1.GateApprovalPolicy{Required: 2}
			g.Status.Approvals = []v1alpha1.GateApprovalStatus{{User: "alice", Decision: "approve", Counted: true}, {User: "eve", Decision: "approve"}}
			return []sigs_client.Object{g}
		}},
		{"a gate that opens by itself", "prod", func(p *v1alpha1.Pipeline) []sigs_client.Object {
			p.Status.Environments = []v1alpha1.PipelineEnvironmentStatus{{Name: "prod", Bundle: "demo-a", Version: "1.4.0",
				IncomingBundle: "demo-b", IncomingVersion: "1.5.0", State: "Pending", BlockedBy: []string{"no-weekend-deploys"}}}
			g := explainGateInstance("demo", "demo-b", "prod", "no-weekend-deploys", "!schedule.isWeekend", false, true,
				"Production deployments are blocked on weekends (bundle.version=1.5.0: !schedule.isWeekend = false)")
			g.Spec.Message = "Production deployments are blocked on weekends"
			return []sigs_client.Object{g}
		}},
		{"a gate that needs a person", "prod", func(p *v1alpha1.Pipeline) []sigs_client.Object {
			p.Status.Environments = []v1alpha1.PipelineEnvironmentStatus{{Name: "prod", Bundle: "demo-a", Version: "1.4.0",
				IncomingBundle: "demo-b", IncomingVersion: "1.5.0", State: "Pending", BlockedBy: []string{"label"}}}
			g := explainGateInstance("demo", "demo-b", "prod", "label", `bundle.labels.approved == "yes"`, false, true,
				`(bundle.version=1.5.0: bundle.labels.approved == "yes" = false)`)
			g.Spec.RecheckInterval = "1m"
			return []sigs_client.Object{g}
		}},
		{"held", "prod", func(p *v1alpha1.Pipeline) []sigs_client.Object {
			p.Spec.Holds = []v1alpha1.EnvironmentHold{{Environment: "prod", Bundle: "demo-a", Reason: "INC-42", CreatedBy: "alice"}}
			p.Status.Environments = []v1alpha1.PipelineEnvironmentStatus{{Name: "prod", Bundle: "demo-a", Version: "1.4.0",
				IncomingBundle: "demo-b", IncomingVersion: "1.5.0", State: "Pending", Held: true}}
			return []sigs_client.Object{whyBundle("demo-a", "1.4.0")}
		}},
		{"paused", "prod", func(p *v1alpha1.Pipeline) []sigs_client.Object {
			p.Spec.Paused = true
			p.Status.Environments = []v1alpha1.PipelineEnvironmentStatus{{Name: "prod", Bundle: "demo-a", Version: "1.4.0",
				IncomingBundle: "demo-b", IncomingVersion: "1.5.0", State: "Pending"}}
			return nil
		}},
		{"its PR", "prod", func(p *v1alpha1.Pipeline) []sigs_client.Object {
			p.Status.Environments = []v1alpha1.PipelineEnvironmentStatus{{Name: "prod", Bundle: "demo-a", Version: "1.4.0",
				IncomingBundle: "demo-b", IncomingVersion: "1.5.0", State: "WaitingForMerge", PRURL: "https://github.com/acme/gitops/pull/42"}}
			return nil
		}},
		{"failed", "prod", func(p *v1alpha1.Pipeline) []sigs_client.Object {
			p.Status.Environments = []v1alpha1.PipelineEnvironmentStatus{{Name: "prod", Bundle: "demo-a", Version: "1.4.0",
				IncomingBundle: "demo-b", IncomingVersion: "1.5.0", State: "Failed"}}
			return []sigs_client.Object{explainStep("demo", "demo-b", "prod", "Failed", "health check timed out after 10m", time.Now())}
		}},
		{"promoting", "prod", func(p *v1alpha1.Pipeline) []sigs_client.Object {
			p.Status.Environments = []v1alpha1.PipelineEnvironmentStatus{{Name: "prod", Bundle: "demo-a", Version: "1.4.0",
				IncomingBundle: "demo-b", IncomingVersion: "1.5.0", State: "HealthChecking", Since: &since}}
			return nil
		}},
		{"waits for upstream", "prod", func(p *v1alpha1.Pipeline) []sigs_client.Object {
			p.Status.Environments = []v1alpha1.PipelineEnvironmentStatus{
				{Name: "test", Bundle: "demo-b", Version: "1.5.0", State: "Verified"},
				{Name: "uat", Bundle: "demo-a", Version: "1.4.0", IncomingBundle: "demo-b", IncomingVersion: "1.5.0", State: "Promoting"},
				{Name: "prod", Bundle: "demo-a", Version: "1.4.0", IncomingBundle: "demo-b", IncomingVersion: "1.5.0", State: "Pending"}}
			return nil
		}},
		{"nothing waits", "prod", func(p *v1alpha1.Pipeline) []sigs_client.Object {
			p.Status.Environments = []v1alpha1.PipelineEnvironmentStatus{{Name: "prod", Bundle: "demo-b", Version: "1.5.0", State: "Verified"}}
			return nil
		}},
		{"no version yet", "prod", func(p *v1alpha1.Pipeline) []sigs_client.Object {
			p.Status.Environments = []v1alpha1.PipelineEnvironmentStatus{{Name: "prod"}}
			return nil
		}},
		{"every environment", "", func(p *v1alpha1.Pipeline) []sigs_client.Object {
			p.Status.Environments = []v1alpha1.PipelineEnvironmentStatus{
				{Name: "test", Bundle: "demo-b", Version: "1.5.0", State: "Verified"},
				{Name: "uat", Bundle: "demo-b", Version: "1.5.0", State: "Verified"},
				{Name: "prod", Bundle: "demo-a", Version: "1.4.0", IncomingBundle: "demo-b", IncomingVersion: "1.5.0",
					State: "WaitingForMerge", PRURL: "https://github.com/acme/gitops/pull/7"}}
			return nil
		}},
	}
	var golden strings.Builder
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
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
