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
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

var updateInbox = flag.Bool("update-inbox", false, "rewrite testdata/inbox.golden")

// inboxObjects is a cluster with one of each thing that waits, and the
// cases QA named: approvals on the caller and on others, already approved by
// the caller, rejected, the caller the author, excludeAuthor with no creator
// and with a component creator; a gate that needs a person and one that
// opens by itself; a hold; a paused pipeline in another namespace; a PR to
// merge and one the SCM merges. Waits are minutes old, oldest first.
func inboxObjects() []sigs_client.Object {
	now := time.Now()
	ago := func(m int) *metav1.Time { t := metav1.NewTime(now.Add(-time.Duration(m) * time.Minute)); return &t }
	pipe := func(name, ns string, envs ...v1alpha1.PipelineEnvironmentStatus) *v1alpha1.Pipeline {
		p := policyPipeline(name)
		p.Namespace = ns
		for _, e := range envs {
			p.Spec.Environments = append(p.Spec.Environments, v1alpha1.EnvironmentSpec{Name: e.Name})
		}
		p.Status.Environments = envs
		return p
	}
	gate := func(ns, pipeline, bundle, env, name, expr string, approval *v1alpha1.GateApprovalPolicy, decisions ...v1alpha1.GateApproval) *v1alpha1.PolicyGate {
		g := explainGateInstance(pipeline, bundle, env, name, expr, false, true, "")
		g.Namespace = ns
		g.Spec.Approval = approval
		for i := range decisions {
			decisions[i].Bundle, decisions[i].Environment = bundle, env
		}
		g.Spec.Approvals = decisions
		return g
	}
	wait := func(env, bundle, version string, minutes int, blockedBy ...string) v1alpha1.PipelineEnvironmentStatus {
		return v1alpha1.PipelineEnvironmentStatus{Name: env, IncomingBundle: bundle, IncomingVersion: version, State: "Pending",
			BlockedBy: blockedBy, Since: ago(minutes)}
	}
	bundle := func(name, ns, tag, creator string) *v1alpha1.Bundle {
		b := whyBundle(name, tag)
		b.Namespace = ns
		if creator != "" {
			b.Annotations = map[string]string{"kardinal.io/created-by": creator}
		}
		return b
	}
	shop := pipe("shop", "default",
		wait("uat", "shop-b", "2.0.0", 50, "soak"),
		wait("prod", "shop-b", "2.0.0", 40, "cab"),
		wait("canary", "shop-b", "2.0.0", 30, "release-managers"),
		wait("eu", "shop-b", "2.0.0", 20, "labels"),
		wait("us", "shop-b", "2.0.0", 10, "two"),
		wait("ap", "shop-b", "2.0.0", 5, "nope"))
	auth := pipe("auth", "default",
		wait("prod", "auth-b", "5.0.0", 15, "cab"),
		wait("dr", "auth-b", "5.0.0", 12, "author"))
	sub := pipe("feed", "default", wait("prod", "feed-b", "0.1.0", 8, "cab"))
	// A version inbox cannot read (deleted, or no get on Bundles): who
	// created it is unknown, not absent.
	ghost := pipe("ghost", "default", wait("prod", "ghost-b", "", 7, "cab"))
	// More than 100 Approvals, and excludeAuthor held with a reject: no one
	// can give them (held wins over rejected, as the reconciler decides).
	many := pipe("many", "default", wait("prod", "many-b", "4.0.0", 6, "cab"), wait("dr", "many-b", "4.0.0", 4, "author"))
	var flood []v1alpha1.GateApproval
	for i := 0; i <= 100; i++ {
		flood = append(flood, v1alpha1.GateApproval{User: fmt.Sprintf("u%d", i), Decision: "approve"})
	}
	web := pipe("web", "default",
		v1alpha1.PipelineEnvironmentStatus{Name: "prod", Bundle: "web-a", Version: "1.4.0", IncomingBundle: "web-b", IncomingVersion: "1.5.0",
			State: "WaitingForMerge", PRURL: "https://github.com/acme/gitops/pull/42", Since: ago(60)},
		v1alpha1.PipelineEnvironmentStatus{Name: "auto", IncomingBundle: "web-b", IncomingVersion: "1.5.0", State: "WaitingForMerge"})
	web.Spec.Environments[1].PR = &v1alpha1.PRConfig{Merge: &v1alpha1.PRMergeConfig{Auto: true}}
	held := pipe("api", "default", v1alpha1.PipelineEnvironmentStatus{Name: "prod", Bundle: "api-a", Version: "3.1.0",
		IncomingBundle: "api-b", IncomingVersion: "3.2.0", State: "Pending", Held: true, Since: ago(90)})
	held.Spec.Holds = []v1alpha1.EnvironmentHold{{Environment: "prod", Bundle: "api-a", Reason: "INC-42", CreatedBy: "bob"}}
	paused := pipe("batch", "team-b", v1alpha1.PipelineEnvironmentStatus{Name: "prod", Bundle: "batch-a", Version: "0.9.0", State: "Verified"})
	paused.Spec.Paused = true
	approve := func(user string, groups ...string) v1alpha1.GateApproval {
		return v1alpha1.GateApproval{User: user, Decision: "approve", Groups: groups}
	}
	reject := func(user string) v1alpha1.GateApproval { return v1alpha1.GateApproval{User: user, Decision: "reject"} }
	return []sigs_client.Object{
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}}, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team-b"}},
		shop, auth, sub, web, held, paused,
		bundle("shop-b", "default", "2.0.0", "alice"), bundle("auth-b", "default", "5.0.0", ""),
		bundle("feed-b", "default", "0.1.0", "subscription:default/feed"), bundle("api-a", "default", "3.1.0", "bob"),
		gate("default", "shop", "shop-b", "uat", "soak", "upstream.test.soakMinutes >= 30", nil),
		gate("default", "shop", "shop-b", "prod", "cab", "true", &v1alpha1.GateApprovalPolicy{Required: 2, AllowedGroups: []string{"release-managers"}}),
		gate("default", "shop", "shop-b", "canary", "release-managers", "true", &v1alpha1.GateApprovalPolicy{Required: 1, ExcludeAuthor: true}),
		gate("default", "shop", "shop-b", "eu", "labels", `bundle.labels.approved == "yes"`, nil),
		gate("default", "shop", "shop-b", "us", "two", "true", &v1alpha1.GateApprovalPolicy{Required: 2}, approve("alice", "release-managers")),
		gate("default", "shop", "shop-b", "ap", "nope", "true", &v1alpha1.GateApprovalPolicy{Required: 1}, reject("carol")),
		gate("default", "auth", "auth-b", "prod", "cab", "true", &v1alpha1.GateApprovalPolicy{Required: 1, AllowedUsers: []string{"dave", "erin"}}),
		gate("default", "auth", "auth-b", "dr", "author", "true", &v1alpha1.GateApprovalPolicy{Required: 1, ExcludeAuthor: true}),
		gate("default", "feed", "feed-b", "prod", "cab", "true", &v1alpha1.GateApprovalPolicy{Required: 1, ExcludeAuthor: true}),
		ghost, gate("default", "ghost", "ghost-b", "prod", "cab", "true", &v1alpha1.GateApprovalPolicy{Required: 1, ExcludeAuthor: true}),
		many, bundle("many-b", "default", "4.0.0", ""),
		gate("default", "many", "many-b", "prod", "cab", "true", &v1alpha1.GateApprovalPolicy{Required: 1}, flood...),
		gate("default", "many", "many-b", "dr", "author", "true", &v1alpha1.GateApprovalPolicy{Required: 1, ExcludeAuthor: true}, reject("carol")),
	}
}

// inboxRun is inbox in ns with the caller alice (unless id fails), who may
// approve, unhold and resume in default, not override, and nothing in
// team-b; the kubeconfig's namespace is default. It returns stdout and
// stderr.
func inboxRun(t *testing.T, c sigs_client.Client, ns string, idErr error) (string, string) {
	t.Helper()
	out, errs, err := inboxRunE(t, c, ns, idErr)
	require.NoError(t, err)
	return out, errs
}

// inboxRunE is inboxRun that returns inbox's error.
func inboxRunE(t *testing.T, c sigs_client.Client, ns string, idErr error) (string, string, error) {
	t.Helper()
	prevID, prevCan, prevNS, prevErr := identityOf, canI, contextNamespace, errOut
	t.Cleanup(func() { identityOf, canI, contextNamespace, errOut = prevID, prevCan, prevNS, prevErr })
	identityOf = func(context.Context, sigs_client.Client) (Identity, error) {
		return Identity{Username: "alice", Groups: []string{"release-managers", "system:authenticated"}}, idErr
	}
	canI = func(_ context.Context, _ sigs_client.Client, verb, resource, sub, ns string) bool {
		return ns == "default" && resource != "policygates"
	}
	contextNamespace = func() string { return "default" }
	var out, errb bytes.Buffer
	errOut = &errb
	err := inboxFn(context.Background(), &out, c, ns)
	return out.String(), errb.String(), err
}

// TestInbox_Golden (CLI UX 4, #1765 QA): what waits for a person, on you
// first (the server's approval tally and your rights) with the command,
// then on others with who; rejected approvals are left out; commands carry
// -n outside the kubeconfig's namespace; -A that may not list cluster-wide
// fails with the fix-it; an unknown caller is noted.
//
// Covers CLI-INBOX-01.
func TestInbox_Golden(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(rootScheme).WithObjects(inboxObjects()...).Build()
	var golden bytes.Buffer
	run := func(title string, cl sigs_client.Client, ns string, idErr error) {
		out, errs := inboxRun(t, cl, ns, idErr)
		golden.WriteString("$ " + title + "\n" + out)
		if errs != "" {
			golden.WriteString("stderr: " + errs)
		}
		golden.WriteString("\n")
	}
	run("kardinal inbox", c, "default", nil)
	run("kardinal inbox -n team-b", c, "team-b", nil)
	run("kardinal inbox -A", c, "", nil)
	// -A without the right to list Pipelines cluster-wide, nor in team-b.
	limited := fake.NewClientBuilder().WithScheme(rootScheme).WithObjects(inboxObjects()...).WithInterceptorFuncs(interceptor.Funcs{
		List: func(ctx context.Context, cl sigs_client.WithWatch, list sigs_client.ObjectList, opts ...sigs_client.ListOption) error {
			o := &sigs_client.ListOptions{}
			o.ApplyOptions(opts)
			if _, ok := list.(*v1alpha1.PipelineList); ok && (o.Namespace == "" || o.Namespace == "team-b") {
				return apierrors.NewForbidden(schema.GroupResource{Group: "kardinal.io", Resource: "pipelines"}, "", errors.New("no"))
			}
			return cl.List(ctx, list, opts...)
		},
	}).Build()
	// -A asks for every namespace: without the right to list them all it
	// fails and says to use -n, as status -A does.
	_, _, err := inboxRunE(t, limited, "", nil)
	require.EqualError(t, err, "you may not list Pipelines in every namespace: run kardinal inbox -n <namespace>")
	run("kardinal inbox (SelfSubjectReview fails)", c, "default", errors.New("selfsubjectreviews is forbidden"))
	path := filepath.Join("testdata", "inbox.golden")
	if *updateInbox {
		require.NoError(t, os.WriteFile(path, golden.Bytes(), 0o644))
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, string(want), golden.String())

	// -o json: no placeholders; needsReason says the command needs --reason.
	globalOutput = "json"
	t.Cleanup(func() { globalOutput = "" })
	out, _ := inboxRun(t, c, "default", nil)
	var got inboxOutput
	require.NoError(t, json.Unmarshal([]byte(out), &got), out)
	var gateNext string
	for _, it := range append(got.OnYou, got.Others...) {
		assert.NotContains(t, it.Next, "<", "no placeholder in %+v", it)
		if it.Kind == "gate" {
			gateNext = it.Next
			assert.True(t, it.NeedsReason)
		}
	}
	assert.Equal(t, "kardinal override shop eu --gate labels", gateNext)

	// Nothing waits.
	globalOutput = ""
	out, _ = inboxRun(t, policyClient(t, policyPipeline("quiet", "prod")), "default", nil)
	assert.Equal(t, "Nothing waits for a person.\n", out)
}
