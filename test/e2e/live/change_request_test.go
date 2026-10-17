//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/changerequest"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
)

// snowTemplate is a per-promotion ChangeRequest template against the fake
// ServiceNow (hack/e2e/ticketapi), its credential in Secret snow of ns.
func snowTemplate(t *testing.T, e *framework.Env, ns string, mut ...func(*v1alpha1.ChangeRequestSpec)) *v1alpha1.ChangeRequest {
	t.Helper()
	e.CreateSecretData(t, ns, "snow", true, map[string]string{"token": framework.SnowCredentials(t)})
	cr := &v1alpha1.ChangeRequest{
		ObjectMeta: metav1.ObjectMeta{Name: "prod-change", Namespace: ns},
		Spec: v1alpha1.ChangeRequestSpec{PerPromotion: true, AppliesTo: []string{"prod"}, Provider: "servicenow",
			PollInterval: "30s",
			ServiceNow: &v1alpha1.ServiceNowChangeRequest{InstanceURL: framework.TicketAPIURL(t),
				ChangeRequestAuth: v1alpha1.ChangeRequestAuth{CredentialsSecretRef: v1alpha1.SecretKeyRef{Name: "snow", Key: "token"}},
				Template:          map[string]string{"short_description": "Promote {{ pipeline.name }} {{ bundle.version }} to {{ environment.name }}"}}},
	}
	for _, m := range mut {
		m(&cr.Spec)
	}
	require.NoError(t, e.Client.Create(context.Background(), cr))
	return cr
}

// waitChangeRequest waits until the instance name in ns satisfies ok.
func waitChangeRequest(t *testing.T, e *framework.Env, ns, name, what string, ok func(*v1alpha1.ChangeRequest) (bool, string)) *v1alpha1.ChangeRequest {
	t.Helper()
	var cr v1alpha1.ChangeRequest
	framework.Eventually(t, gateTimeout, what, func(ctx context.Context) (bool, string) {
		if err := e.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &cr); err != nil {
			return false, err.Error()
		}
		return ok(&cr)
	})
	return &cr
}

// TestGate_ChangeRequestServiceNow (spec 23 §4–§7): a prod gate waits for a
// ServiceNow change request. Once test is Verified the Graph creates the
// instance from the per-promotion template (named <template>-<env>--<bundle>,
// labelled with the Bundle UID, its ticket template rendered), and the
// controller opens one change_request over HTTPS with the suite's CA,
// carrying the promotion's correlation key. The gate blocks naming the
// ticket while it is pending. A 429 with Retry-After writes ProviderError and
// retryAt, and nothing is sent before it. Approved in ServiceNow, the gate
// opens and prod promotes, with one ticket in all, ChangeRequestOpened and
// ChangeRequestApproved records, and the instance suspended once the Bundle
// is Verified. Nobody but kardinal may write an instance.
//
// Covers CHANGE-REQUEST-02, CHANGE-REQUEST-03, CHANGE-REQUEST-04.
func TestGate_ChangeRequestServiceNow(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	a := newArgoApp(t, e, "test", "prod")
	tmpl := snowTemplate(t, e, a.ns)
	e.CreateGate(t, framework.Gate(a.ns, "cab", "prod",
		`changeRequests["prod-change"].state == "approved" && !changeRequests["prod-change"].stale`, recheck))
	a.apply(t, a.pipeline(nil))
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Verified", promoteTimeout)

	name := "prod-change-prod--" + bundle
	inst := waitChangeRequest(t, e, a.ns, name, "the instance opened its ticket", func(cr *v1alpha1.ChangeRequest) (bool, string) {
		return cr.Status.Ticket != nil && cr.Status.State == "pending", fmt.Sprintf("state %q conditions %v", cr.Status.State, cr.Status.Conditions)
	})
	b := getBundle(t, e, a.ns, bundle)
	assert.Equal(t, map[string]string{"kardinal.io/pipeline": pipelineName, "kardinal.io/bundle": bundle,
		"kardinal.io/environment": "prod", graph.LabelBundleUID: string(b.UID), graph.LabelChangeRequestTemplate: "prod-change"},
		changeRequestLabels(inst.Labels))
	corr := fmt.Sprintf("kardinal:%s/%s/prod/%s", a.ns, bundle, b.UID)
	assert.Equal(t, corr, inst.Status.CorrelationID)
	key := changerequest.CorrelationKey(corr)
	ticketOf := func() []framework.FakeSnowTicket {
		all, err := e.FakeSnowTickets(ctx)
		require.NoError(t, err)
		var out []framework.FakeSnowTicket
		for _, tk := range all {
			if tk.CorrelationID == key {
				out = append(out, tk)
			}
		}
		return out
	}
	tickets := ticketOf()
	require.Len(t, tickets, 1, "one change_request with the promotion's correlation key")
	tk := tickets[0]
	assert.Equal(t, tk.Number, inst.Status.Ticket.Key)
	assert.Equal(t, tk.SysID, inst.Status.Ticket.ID)
	assert.Equal(t, fmt.Sprintf("Promote %s %s to prod", pipelineName, fixtures.V2), tk.Fields["short_description"])
	assert.Equal(t, "kardinal", tk.Fields["correlation_display"])
	e.WaitGateReady(t, a.ns, bundle, "prod", "cab", false,
		fmt.Sprintf("change request prod-change: ticket %s is pending (New / Not Yet Requested)", tk.Number), gateTimeout)

	// A 429 with Retry-After: ProviderError, retryAt, nothing sent before it.
	e.FailFakeTicketAPI(t, tk.SysID, 429, 1, "90")
	inst = waitChangeRequest(t, e, a.ns, name, "the 429 is recorded", func(cr *v1alpha1.ChangeRequest) (bool, string) {
		c := meta.FindStatusCondition(cr.Status.Conditions, "ProviderError")
		return c != nil && c.Status == metav1.ConditionTrue && strings.Contains(c.Message, "HTTP 429") && cr.Status.RetryAt != nil,
			fmt.Sprintf("conditions %v", cr.Status.Conditions)
	})
	assert.GreaterOrEqual(t, inst.Status.RetryAt.Sub(meta.FindStatusCondition(inst.Status.Conditions, "ProviderError").LastTransitionTime.Time),
		85*time.Second, "retryAt honours Retry-After: 90")
	reqs, err := e.FakeTicketRequests(ctx, tk.SysID)
	require.NoError(t, err)
	failedAt := len(reqs)
	time.Sleep(time.Until(inst.Status.RetryAt.Add(-10 * time.Second)))
	reqs, err = e.FakeTicketRequests(ctx, tk.SysID)
	require.NoError(t, err)
	assert.Len(t, reqs, failedAt, "no request before retryAt")

	// Only kardinal writes an instance.
	mallory := impersonating(t, e, a.ns, "mallory@example.com", nil,
		[]string{"changerequests", "changerequests/status"}, "get", "list", "create", "update", "patch")
	refused := func(err error, what string) {
		t.Helper()
		require.Error(t, err, what)
		assert.True(t, apierrors.IsForbidden(err), "%s: %v", what, err)
		assert.Contains(t, err.Error(), "only kardinal (the promotion Graph or the controller) creates or changes this object", what)
	}
	approve := client.RawPatch(types.MergePatchType, []byte(`{"status":{"state":"approved","validUntil":"2099-01-01T00:00:00Z"}}`))
	refused(mallory.Status().Patch(ctx, inst.DeepCopy(), approve), "a user marks the change request approved")
	refused(e.Client.Status().Patch(ctx, inst.DeepCopy(), approve), "the cluster admin marks it approved")
	refused(mallory.Create(ctx, &v1alpha1.ChangeRequest{ObjectMeta: metav1.ObjectMeta{Name: "forged", Namespace: a.ns,
		Labels: map[string]string{"kardinal.io/bundle": bundle, "kardinal.io/environment": "prod", graph.LabelChangeRequestTemplate: "prod-change"}},
		Spec: v1alpha1.ChangeRequestSpec{Provider: "servicenow", ServiceNow: tmpl.Spec.ServiceNow.DeepCopy()}}), "a user forges an instance")
	mine := tmpl.DeepCopy()
	mine.ObjectMeta = metav1.ObjectMeta{Name: "mallory-template", Namespace: a.ns}
	mine.Spec.AppliesTo = []string{"nowhere"}
	require.NoError(t, mallory.Create(ctx, mine), "a template (no kardinal.io/bundle) is the user's")
	e.WaitGateReady(t, a.ns, bundle, "prod", "cab", false, "", gateTimeout)

	// Approved in ServiceNow: the gate opens and prod promotes.
	e.SetFakeSnowTicket(t, tk.Number, map[string]interface{}{"state": "-2", "approval": "approved",
		"approvers": []map[string]string{{"name": "Jane Smith", "state": "approved"}}})
	e.WaitGateReady(t, a.ns, bundle, "prod", "cab", true, "", promoteTimeout)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)
	inst = waitChangeRequest(t, e, a.ns, name, "approved with its approvers, then suspended", func(cr *v1alpha1.ChangeRequest) (bool, string) {
		return cr.Status.State == "approved" && len(cr.Status.Approvers) == 1 && cr.Spec.Suspend,
			fmt.Sprintf("state %q approvers %v suspend %v", cr.Status.State, cr.Status.Approvers, cr.Spec.Suspend)
	})
	assert.Equal(t, "Jane Smith", inst.Status.Approvers[0].Name)
	assert.Len(t, ticketOf(), 1, "one ticket in all")
	framework.Eventually(t, time.Minute, "ChangeRequestOpened and ChangeRequestApproved", func(ctx context.Context) (bool, string) {
		events, err := e.AuditEvents(ctx, a.ns, bundle)
		if err != nil {
			return false, err.Error()
		}
		var got []string
		for _, ev := range events {
			if strings.HasPrefix(ev.Spec.Action, "ChangeRequest") {
				got = append(got, ev.Spec.Action)
				if !strings.Contains(ev.Spec.Message, tk.Number) {
					return false, ev.Spec.Message
				}
			}
		}
		return len(got) == 2, fmt.Sprintf("%v", got)
	})
	all, err := e.FakeTicketRequests(ctx, tk.SysID)
	require.NoError(t, err)
	assert.Positive(t, len(all))
	for _, r := range all {
		assert.Equal(t, "ok", r.Auth, "every request carried the credential")
	}
}

// TestGate_ChangeRequestLinkServiceNow (spec 23 §7.4, §8): a prod gate
// needs an approval with the ServiceNow change number in ticketID, and that
// change approved and verified. The ChangeRequest links the ticket the
// counted approval names and never opens one. An unrelated approved change
// (another configuration item) is tracked but not verified, and the gate
// says so; so is an approved change of the right item opened before the
// version existed; the approver's new approval with a change opened for this
// version verifies it, and prod promotes.
//
// Covers CHANGE-REQUEST-05.
func TestGate_ChangeRequestLinkServiceNow(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	c := e.CLI(t)
	a := newArgoApp(t, e, "test", "prod")
	snowTemplate(t, e, a.ns, func(s *v1alpha1.ChangeRequestSpec) {
		s.Mode = "link"
		s.Link = &v1alpha1.ChangeRequestLink{From: "approvalField", Name: "ticketID",
			RequireField: &v1alpha1.ChangeRequestFieldMatch{Name: "cmdb_ci", Value: "checkout-" + a.ns}}
	})
	g := framework.Gate(a.ns, "cab", "prod",
		`changeRequests["prod-change"].state == "approved" && changeRequests["prod-change"].verified`, recheck)
	g.Spec.Approval = &v1alpha1.GateApprovalPolicy{Required: 1, Fields: []v1alpha1.ApprovalField{
		{Name: "ticketID", Required: true, Pattern: `^CHG[0-9]{7}$`, Description: "ServiceNow change number"}}}
	e.CreateGate(t, g)
	a.apply(t, a.pipeline(nil))
	// An approved change of this configuration item opened before the
	// version existed: an old ticket, not this promotion's.
	old := e.AddFakeSnowTicket(t, framework.FakeSnowTicket{State: "-2", Approval: "approved",
		Fields: map[string]string{"cmdb_ci": "checkout-" + a.ns}})
	time.Sleep(1100 * time.Millisecond) // sys_created_on has one-second resolution
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2)
	time.Sleep(1100 * time.Millisecond)
	right := e.AddFakeSnowTicket(t, framework.FakeSnowTicket{State: "-2", Approval: "approved",
		Fields: map[string]string{"cmdb_ci": "checkout-" + a.ns}})
	unrelated := e.AddFakeSnowTicket(t, framework.FakeSnowTicket{State: "-2", Approval: "approved",
		Fields: map[string]string{"cmdb_ci": "payroll"}})
	e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Verified", promoteTimeout)
	name := "prod-change-prod--" + bundle
	waitChangeRequest(t, e, a.ns, name, "waiting for the approval's ticket", func(cr *v1alpha1.ChangeRequest) (bool, string) {
		ready := meta.FindStatusCondition(cr.Status.Conditions, "Ready")
		return ready != nil && ready.Reason == "WaitingForKey", fmt.Sprintf("%v", cr.Status.Conditions)
	})

	alice := userKubeconfig(t, e, a.ns, "alice@example.com", "release-managers")
	as := func(args ...string) framework.CLIResult {
		return c.Exec(framework.CLIOptions{Kubeconfig: alice}, c.Args(a.ns, args...)...)
	}
	r := as("approve", pipelineName, "prod", "--field", "ticketID="+unrelated.Number)
	require.Equal(t, 0, r.Code, r.Output())
	inst := waitChangeRequest(t, e, a.ns, name, "the unrelated change is linked, not verified", func(cr *v1alpha1.ChangeRequest) (bool, string) {
		return cr.Status.Ticket != nil && cr.Status.Ticket.Key == unrelated.Number && cr.Status.Verified != nil && !*cr.Status.Verified,
			fmt.Sprintf("ticket %v verified %v", cr.Status.Ticket, cr.Status.Verified)
	})
	assert.Equal(t, "approved", inst.Status.State)
	e.WaitGateReady(t, a.ns, bundle, "prod", "cab", false, fmt.Sprintf("ticket %s is approved (Scheduled / Approved), not verified", unrelated.Number), gateTimeout)
	a.noStep(t, bundle, "prod", 10*time.Second)

	// The old change of the right configuration item: tracked, not verified.
	r = as("approve", pipelineName, "prod", "--field", "ticketID="+old.Number)
	require.Equal(t, 0, r.Code, r.Output())
	waitChangeRequest(t, e, a.ns, name, "the old change is linked, not verified", func(cr *v1alpha1.ChangeRequest) (bool, string) {
		return cr.Status.Ticket != nil && cr.Status.Ticket.Key == old.Number && cr.Status.Verified != nil && !*cr.Status.Verified &&
				strings.Contains(cr.Status.VerifiedReason, "an older ticket is not this promotion's"),
			fmt.Sprintf("ticket %v verified %v %q", cr.Status.Ticket, cr.Status.Verified, cr.Status.VerifiedReason)
	})
	a.noStep(t, bundle, "prod", 10*time.Second)

	r = as("approve", pipelineName, "prod", "--field", "ticketID="+right.Number)
	require.Equal(t, 0, r.Code, r.Output())
	waitChangeRequest(t, e, a.ns, name, "the right change is linked and verified", func(cr *v1alpha1.ChangeRequest) (bool, string) {
		return cr.Status.Ticket != nil && cr.Status.Ticket.Key == right.Number && cr.Status.Verified != nil && *cr.Status.Verified,
			fmt.Sprintf("ticket %v verified %v", cr.Status.Ticket, cr.Status.Verified)
	})
	e.WaitGateReady(t, a.ns, bundle, "prod", "cab", true, "", promoteTimeout)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)
	b := getBundle(t, e, a.ns, bundle)
	key := changerequest.CorrelationKey(fmt.Sprintf("kardinal:%s/%s/prod/%s", a.ns, bundle, b.UID))
	all, err := e.FakeSnowTickets(ctx)
	require.NoError(t, err)
	for _, tk := range all {
		assert.NotEqual(t, key, tk.CorrelationID, "link mode opened no ticket (%s)", tk.Number)
	}
}

// changeRequestLabels is labels without the ones kro adds to the objects it
// applies.
func changeRequestLabels(labels map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range labels {
		if k == "kardinal.io/pipeline" || k == "kardinal.io/bundle" || k == "kardinal.io/environment" ||
			k == graph.LabelBundleUID || k == graph.LabelChangeRequestTemplate {
			out[k] = v
		}
	}
	return out
}
