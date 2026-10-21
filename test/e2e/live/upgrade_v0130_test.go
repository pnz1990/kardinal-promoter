//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	authnv1 "k8s.io/api/authentication/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/actor"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/receipt"
	pipelinerec "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/pipeline"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/invariants"
)

// guideChartV0130 is the chart reference of the "Upgrading from v0.13.0"
// guide: the test replaces it with the checkout's chart.
const guideChartV0130 = "oci://ghcr.io/pnz1990/charts/kardinal-promoter --version <version>"

// u0130Override is the reason of the org gate's override, given with the
// v0.13.0 CLI.
const u0130Override = "INC-0130 hotfix"

// u0130Approver approves the evidence version's change, naming its ticket.
const u0130Approver = "alice@example.com"

// u0130Estate is the estate v0.13.0 runs before the upgrade, one namespace
// per concern, each with the Pipeline podinfo:
//
//   - ev (the agent-evidence journey's app, newEvidenceApp): agent A's
//     version by digest, test Verified with its receipt, through prod's
//     attestations gate and agent V's verdict, and held by the cab gate: its
//     ServiceNow change request is opened and pending, and two approvals
//     name its ticket (alice's, counted, and A's own, recorded and not);
//   - done: agent S's version by digest, Verified in test and prod (its PR
//     merged), both receipts signed and pushed;
//   - org: a version past an org gate (platform-policies) a person overrode
//     with the v0.13.0 CLI, its PR in the org gate's environment open;
//   - shared: prod held by two gates read through spec.policyNamespaces,
//     one from a namespace that agreed to share its gates (ok) and one from
//     a namespace that has not (no): PolicyNamespacesShared=False.
type u0130Estate struct {
	e    *framework.Env
	from u012From

	ev                    *evidenceApp
	done, org, shared     *app
	orgEnv, orgGate       string
	okNS, noNS            string
	shipperName, shipper  string // S: the AgentIdentity, its ServiceAccount's username
	verifierUser, alice   string // V's username; alice's kubeconfig
	pub                   string // the receipt key's public half
	evB, doneB1, orgB, sb string // the versions
}

func (s *u0130Estate) namespaces() []string {
	return []string{s.ev.ns, s.done.ns, s.org.ns, s.shared.ns}
}

// cli runs the v0.13.0 CLI in ns, with kubeconfig when set, and returns its
// output; it fails the test when the CLI does.
func (s *u0130Estate) cli(t *testing.T, ns, kubeconfig string, args ...string) string {
	t.Helper()
	if kubeconfig != "" {
		args = append([]string{"--kubeconfig", kubeconfig}, args...)
	}
	out, err := s.from.cli(s.e, t, ns, args...)
	require.NoError(t, err, out)
	return out
}

// created is the Bundle a released CLI's create bundle reports.
func (s *u0130Estate) created(t *testing.T, out string) string {
	t.Helper()
	m := v081BundleCreated.FindStringSubmatch(out)
	require.Len(t, m, 2, "kardinal create bundle names the Bundle it created:\n%s", out)
	return m[1]
}

// shipperKubeconfig is a kubeconfig with a new token of S's ServiceAccount
// (policyAgent's lasts an hour, less than the test).
func (s *u0130Estate) shipperKubeconfig(t *testing.T) string {
	t.Helper()
	tr, err := s.e.Kube.CoreV1().ServiceAccounts(s.done.ns).CreateToken(context.Background(), "shipper", &authnv1.TokenRequest{
		Spec: authnv1.TokenRequestSpec{ExpirationSeconds: ptr.To[int64](3600)}}, metav1.CreateOptions{})
	require.NoError(t, err)
	return tokenKubeconfig(t, s.e, tr.Status.Token)
}

// objects are the uids of the estate's gate instances, Approvals, Verdicts,
// AgentRuns, ChangeRequests (templates and instances), ImageVerifications
// and PromotionReceipts, and of the cluster's AgentIdentities, StepTemplates
// and AgentIndexes, by "Kind namespace/name".
func (s *u0130Estate) objects(t *testing.T) map[string]types.UID {
	t.Helper()
	ctx := context.Background()
	out := map[string]types.UID{}
	put := func(kind string, o client.Object) { out[kind+" "+o.GetNamespace()+"/"+o.GetName()] = o.GetUID() }
	for _, ns := range s.namespaces() {
		var gl v1alpha1.PolicyGateList
		require.NoError(t, s.e.Client.List(ctx, &gl, client.InNamespace(ns), client.HasLabels{"kardinal.io/bundle"}))
		for i := range gl.Items {
			put("PolicyGate", &gl.Items[i])
		}
		var al v1alpha1.ApprovalList
		require.NoError(t, s.e.Client.List(ctx, &al, client.InNamespace(ns)))
		for i := range al.Items {
			put("Approval", &al.Items[i])
		}
		var vl v1alpha1.VerdictList
		require.NoError(t, s.e.Client.List(ctx, &vl, client.InNamespace(ns)))
		for i := range vl.Items {
			put("Verdict", &vl.Items[i])
		}
		var rl v1alpha1.AgentRunList
		require.NoError(t, s.e.Client.List(ctx, &rl, client.InNamespace(ns)))
		for i := range rl.Items {
			put("AgentRun", &rl.Items[i])
		}
		var cl v1alpha1.ChangeRequestList
		require.NoError(t, s.e.Client.List(ctx, &cl, client.InNamespace(ns)))
		for i := range cl.Items {
			put("ChangeRequest", &cl.Items[i])
		}
		var il v1alpha1.ImageVerificationList
		require.NoError(t, s.e.Client.List(ctx, &il, client.InNamespace(ns)))
		for i := range il.Items {
			put("ImageVerification", &il.Items[i])
		}
		var pl v1alpha1.PromotionReceiptList
		require.NoError(t, s.e.Client.List(ctx, &pl, client.InNamespace(ns)))
		for i := range pl.Items {
			put("PromotionReceipt", &pl.Items[i])
		}
	}
	var ids v1alpha1.AgentIdentityList
	require.NoError(t, s.e.Client.List(ctx, &ids))
	for i := range ids.Items {
		put("AgentIdentity", &ids.Items[i])
	}
	var tmpls v1alpha1.StepTemplateList
	require.NoError(t, s.e.Client.List(ctx, &tmpls))
	for i := range tmpls.Items {
		put("StepTemplate", &tmpls.Items[i])
	}
	var idx v1alpha1.AgentIndexList
	require.NoError(t, s.e.Client.List(ctx, &idx))
	for i := range idx.Items {
		put("AgentIndex", &idx.Items[i])
	}
	return out
}

// u0130Receipt is what a PromotionReceipt holds once signed: the statement
// and the signed envelope never change after it.
type u0130Receipt struct {
	uid                     types.UID
	digest, envelope, phase string
}

// receipts are the estate's PromotionReceipts by namespace/name.
func (s *u0130Estate) receipts(t *testing.T) map[string]u0130Receipt {
	t.Helper()
	out := map[string]u0130Receipt{}
	for _, ns := range s.namespaces() {
		var list v1alpha1.PromotionReceiptList
		require.NoError(t, s.e.Client.List(context.Background(), &list, client.InNamespace(ns)))
		for _, rc := range list.Items {
			out[ns+"/"+rc.Name] = u0130Receipt{uid: rc.UID, digest: rc.Spec.StatementDigest, envelope: rc.Status.Bundle, phase: rc.Status.Phase}
		}
	}
	return out
}

// sameReceipts checks that every receipt of before is the same object,
// with the same statement and signed envelope (signed once: no controller
// signs it again), still Stored.
func (s *u0130Estate) sameReceipts(t *testing.T, before map[string]u0130Receipt, when string) {
	t.Helper()
	now := s.receipts(t)
	for k, rc := range before {
		got, ok := now[k]
		if !assert.True(t, ok, "receipt %s survives %s", k, when) {
			continue
		}
		assert.Equal(t, rc.uid, got.uid, "receipt %s is the same object after %s", k, when)
		assert.Equal(t, rc.digest, got.digest, "receipt %s has the same statement after %s", k, when)
		assert.Equal(t, rc.envelope, got.envelope, "receipt %s keeps the envelope it was signed with after %s: signed once", k, when)
		assert.Equal(t, v1alpha1.ReceiptStored, got.phase, "receipt %s is still Stored after %s", k, when)
	}
}

// u0130Graph is what the render-label checks compare of a Graph.
type u0130Graph struct {
	uid        types.UID
	label      string
	generation int64
}

// graphs are the estate's Graphs by namespace/bundle.
func (s *u0130Estate) graphs(ctx context.Context) (map[string]u0130Graph, error) {
	out := map[string]u0130Graph{}
	for _, ns := range s.namespaces() {
		list, err := s.e.Dynamic.Resource(framework.GraphGVR).Namespace(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, err
		}
		for _, g := range list.Items {
			if g.GetDeletionTimestamp() == nil {
				out[ns+"/"+g.GetLabels()["kardinal.io/bundle"]] = u0130Graph{uid: g.GetUID(),
					label: g.GetLabels()[graph.LabelRenderVersion], generation: g.GetGeneration()}
			}
		}
	}
	return out, nil
}

// rendered checks the estate's Graphs across a change of controller from
// one whose render label is prev to the running one's, want (#2346, #2359).
// The running controller renders a promoting version's Graph again once,
// in place, when its label differs from want, and leaves every other Graph
// alone: each Graph of before whose Bundle is still Promoting is the same
// object and comes to carry want; when prev is want it is not written
// (the same generation), and otherwise it is rendered once (one more
// generation at most, for 20s). A Graph of a version no longer promoting
// keeps its label. With prev equal to want no gate rule changes, so the
// estate records no GateChanged from since on.
func (s *u0130Estate) rendered(t *testing.T, before map[string]u0130Graph, prev, want string, since time.Time, when string) {
	t.Helper()
	ctx := context.Background()
	phase := func(ctx context.Context, key string) (string, error) {
		ns, name, _ := strings.Cut(key, "/")
		var b v1alpha1.Bundle
		if err := s.e.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &b); err != nil {
			return "", err
		}
		return b.Status.Phase, nil
	}
	check := func(ctx context.Context) (bool, string) {
		now, err := s.graphs(ctx)
		if err != nil {
			return false, err.Error()
		}
		var bad []string
		promoting := 0
		for k, g := range before {
			cur, ok := now[k]
			if !ok {
				continue // retired: a version that finished
			}
			p, err := phase(ctx, k)
			if err != nil {
				return false, err.Error()
			}
			switch {
			case cur.uid != g.uid:
				bad = append(bad, fmt.Sprintf("%s: a new Graph (uid %s, was %s)", k, cur.uid, g.uid))
			case p != "Promoting" && cur.label != g.label:
				bad = append(bad, fmt.Sprintf("%s (%s): label %q, was %q", k, p, cur.label, g.label))
			case p != "Promoting":
			case cur.label != want:
				bad = append(bad, fmt.Sprintf("%s: label %q, want %q", k, cur.label, want))
			case prev == want && cur.generation != g.generation:
				bad = append(bad, fmt.Sprintf("%s: written again (generation %d, was %d) though its label was the current one", k, cur.generation, g.generation))
			case cur.generation > g.generation+1:
				bad = append(bad, fmt.Sprintf("%s: rendered more than once (generation %d, was %d)", k, cur.generation, g.generation))
			default:
				promoting++
			}
		}
		if len(bad) > 0 {
			return false, strings.Join(bad, "; ")
		}
		return promoting > 0, fmt.Sprintf("%d promoting versions' Graphs checked", promoting)
	}
	framework.Eventually(t, 2*time.Minute, "the Graphs after "+when+" carry render label "+want, check)
	framework.Consistently(t, 20*time.Second, "the Graphs after "+when+" are rendered once at most", check)
	var evs []string
	for _, ns := range s.namespaces() {
		var list v1alpha1.AuditEventList
		require.NoError(t, s.e.Client.List(ctx, &list, client.InNamespace(ns), client.MatchingLabels{"kardinal.io/action": "GateChanged"}))
		for _, ev := range list.Items {
			if !ev.CreationTimestamp.Time.Before(since.Truncate(time.Second)) {
				evs = append(evs, fmt.Sprintf("%s/%s: %s", ns, ev.Spec.BundleName, ev.Spec.Message))
			}
		}
	}
	if prev == want {
		assert.Empty(t, evs, "render label %s before and after %s: no gate rule changed, and no GateChanged is recorded", want, when)
		return
	}
	t.Logf("render label %s → %s at %s; GateChanged records: %v", prev, want, when, evs)
}

// indexed checks that the release's AgentIndex, in whichever format is
// current (actor.Readable), maps each user to its agent, unblocked, and
// returns its uid.
func (s *u0130Estate) indexed(t *testing.T, users map[string]string, when string) types.UID {
	t.Helper()
	var uid types.UID
	framework.Eventually(t, 2*time.Minute, "the AgentIndex maps the estate's agents "+when, func(ctx context.Context) (bool, string) {
		var idx v1alpha1.AgentIndex
		if err := s.e.Client.Get(ctx, types.NamespacedName{Name: agentIndexName}, &idx); err != nil {
			return false, err.Error()
		}
		uid = idx.UID
		st, ok := actor.Readable(&idx.Status)
		if !ok {
			return false, fmt.Sprintf("not built: %v", idx.Status.Conditions)
		}
		for user, agent := range users {
			if en, found := st.Users[user]; !found || en.Agent != agent || en.Blocked != "" {
				return false, fmt.Sprintf("%s: %+v (found %v), want agent %s", user, en, found, agent)
			}
		}
		return true, ""
	})
	return uid
}

// notShared is shared's PolicyNamespacesShared condition as doctor reads it
// (pipelinerec.NotSharedPolicyNamespaces), and whether it is Ready.
func (s *u0130Estate) notShared(ctx context.Context) (bool, []string, bool, error) {
	var p v1alpha1.Pipeline
	if err := s.e.Client.Get(ctx, types.NamespacedName{Namespace: s.shared.ns, Name: pipelineName}, &p); err != nil {
		return false, nil, false, err
	}
	flagged, names := pipelinerec.NotSharedPolicyNamespaces(&p)
	return flagged, names, meta.IsStatusConditionTrue(p.Status.Conditions, "Ready"), nil
}

// orgInstance is the org gate's instance of the org version.
func (s *u0130Estate) orgInstance(t *testing.T) *v1alpha1.PolicyGate {
	t.Helper()
	var g v1alpha1.PolicyGate
	require.NoError(t, s.e.Client.Get(context.Background(), types.NamespacedName{Namespace: s.org.ns,
		Name: framework.GateInstanceName(framework.PolicyNamespace, s.orgGate, s.orgEnv, s.orgB)}, &g))
	return &g
}

// overridden counts the GateOverridden records of the org version.
func (s *u0130Estate) overridden(ctx context.Context) (int, error) {
	var list v1alpha1.AuditEventList
	if err := s.e.Client.List(ctx, &list, client.InNamespace(s.org.ns), client.MatchingLabels{"kardinal.io/action": "GateOverridden"}); err != nil {
		return 0, err
	}
	n := 0
	for _, ev := range list.Items {
		if ev.Spec.BundleName == s.orgB {
			n++
		}
	}
	return n, nil
}

// kept checks, for a change of controller (when), that every in-flight
// version of before is the same Bundle with the same Graph, steps and PRs,
// every object of objs the same object, every receipt the same, the
// AgentIndex the same object mapping the same agents, the org gate's
// override still counted and recorded once, and shared's
// PolicyNamespacesShared condition what it was.
func (s *u0130Estate) kept(t *testing.T, before u0110Snapshot, inFlight []string, objs map[string]types.UID,
	rcs map[string]u0130Receipt, idx types.UID, override v1alpha1.PolicyGate, nsNames []string, when string) {
	t.Helper()
	after := snapshotOf(t, s.e, s.namespaces()...)
	for _, k := range inFlight {
		assert.Equal(t, before.bundles[k].UID, after.bundles[k].UID, "%s is the same Bundle after %s", k, when)
		assert.Equal(t, before.graphs[k], after.graphs[k], "%s keeps its Graph (same uid) after %s", k, when)
	}
	for k, st := range before.steps {
		ns, _, _ := strings.Cut(k, "/")
		if !slices.Contains(inFlight, ns+"/"+st.Spec.BundleName) {
			continue
		}
		now, ok := after.steps[k]
		if assert.True(t, ok, "step %s survives %s", k, when) {
			assert.Equal(t, st.UID, now.UID, "step %s is the same object after %s", k, when)
		}
	}
	for k, n := range before.prs {
		ns, rest, _ := strings.Cut(k, "/")
		b, _, _ := strings.Cut(rest, "/")
		if slices.Contains(inFlight, ns+"/"+b) {
			assert.Equal(t, n, after.prs[k], "%s keeps PR #%d after %s", k, n, when)
		}
	}
	now := s.objects(t)
	for k, uid := range objs {
		assert.Equal(t, uid, now[k], "%s is the same object after %s", k, when)
	}
	s.sameReceipts(t, rcs, when)
	users := map[string]string{s.ev.authorUser: s.ev.authorName, s.shipper: s.shipperName, s.verifierUser: s.ev.verifier}
	assert.Equal(t, idx, s.indexed(t, users, "after "+when), "the AgentIndex is the same object after %s", when)

	g := s.orgInstance(t)
	assert.Equal(t, override.UID, g.UID, "the overridden org gate instance is the same object after %s", when)
	assert.Equal(t, override.Spec.Overrides, g.Spec.Overrides, "the override is the one given under v0.13.0")
	assert.Equal(t, override.Status.Overrides, g.Status.Overrides, "the controller's record of the override (key, first seen, verified, audited) is the same after %s", when)
	assert.True(t, g.Status.Ready, "the override still counts after %s: %s", when, framework.DescribeGate(g))
	assert.Contains(t, g.Status.Reason, "OVERRIDDEN by", "after %s", when)
	framework.Consistently(t, 20*time.Second, "the org version's override is recorded once after "+when, func(ctx context.Context) (bool, string) {
		n, err := s.overridden(ctx)
		if err != nil {
			return false, err.Error()
		}
		return n == 1, fmt.Sprintf("%d GateOverridden records", n)
	})
	framework.Eventually(t, time.Minute, "shared's PolicyNamespacesShared condition after "+when, func(ctx context.Context) (bool, string) {
		flagged, names, ready, err := s.notShared(ctx)
		if err != nil {
			return false, err.Error()
		}
		// #2286: this release warns, as v0.13.0 does; a release that refuses
		// the namespace says here what happens to the version it holds.
		return flagged && slices.Equal(names, nsNames) && ready, fmt.Sprintf("not shared %v %v, Ready %v", flagged, names, ready)
	})
}

// u0130KroVersions are the kro version v0.13.0 targets (versions.env
// V0130_KRO_VERSION) and the checkout's (hack/install-kro.sh).
func u0130KroVersions(t *testing.T) (from, checkout string) {
	t.Helper()
	root := framework.RepoRoot(t)
	env, err := os.ReadFile(filepath.Join(root, "hack", "e2e", "versions.env"))
	require.NoError(t, err)
	m := regexp.MustCompile(`(?m)^V0130_KRO_VERSION=(\S+)$`).FindSubmatch(env)
	require.NotNil(t, m, "hack/e2e/versions.env V0130_KRO_VERSION")
	script, err := os.ReadFile(filepath.Join(root, "hack", "install-kro.sh"))
	require.NoError(t, err)
	c := regexp.MustCompile(`KRO_VERSION:-([0-9][0-9a-z.-]*)\}`).FindSubmatch(script)
	require.NotNil(t, c, "hack/install-kro.sh KRO_VERSION")
	return string(m[1]), string(c[1])
}

// u0130CheckoutCRDs are the CRDs the checkout's chart ships.
func u0130CheckoutCRDs(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(framework.RepoRoot(t), "chart", "kardinal-promoter", "crds", "kardinal.io_*.yaml"))
	require.NoError(t, err)
	require.NotEmpty(t, files)
	var out []string
	for _, f := range files {
		out = append(out, strings.TrimSuffix(strings.TrimPrefix(filepath.Base(f), "kardinal.io_"), ".yaml")+".kardinal.io")
	}
	return out
}

// u0130Logs checks what the Collector read: the controllers' logs (each
// release's Pods from their start), with no data race, no panic and no
// error line but the benign ones.
func u0130Logs(t *testing.T, logs *invariants.Collector) {
	t.Helper()
	sum := logs.Summary()
	controllers := 0
	for _, p := range sum.Pods {
		if !strings.HasPrefix(p, "kro-") {
			controllers++
		}
	}
	assert.NotZero(t, controllers, "the controllers' logs were read")
	assert.Empty(t, sum.StreamErrors, "every log stream was read whole")
	assert.Empty(t, sum.Races, "no data race")
	assert.Empty(t, sum.Panics, "no controller panic")
	assert.Empty(t, sum.KroPanics, "no kro panic")
	for _, g := range sum.Unexpected {
		t.Errorf("%dx error log %q (e.g. %s: %s)", g.Count, g.Key, g.Example.Pod, g.Example.Error)
	}
	t.Logf("controller logs: %d lines from %d containers, %d errors (%d benign), %d warnings",
		sum.Lines, len(sum.Pods), sum.Errors, sum.Benign, sum.Warnings)
}

// TestUpgradeV0130_Estate builds an estate of v0.13's evidence in flight
// under kardinal-promoter v0.13.0 as released (its chart, digest-pinned
// image, CLI and kro; components/kardinal-v0130.sh, receipts signed with a
// static key) and follows "Upgrading from v0.13.0" in docs/upgrade.md,
// running the guide's own commands, then its step 5: helm rollback to
// v0.13.0. Until v0.13.0 is released it skips, saying why.
//
// The estate (u0130Estate): signed receipts, an agent's version through an
// attestations gate and an agent verifier, held by a ServiceNow change
// request with approvals naming its ticket; an agent's version Verified;
// a version past an org gate a person overrode, its PR open; and a version
// held by gates read through spec.policyNamespaces from a namespace that
// agreed to share them and one that has not.
//
// The upgrade is checked to have happened (v0.13.0's image, chart and
// deployed revision before; the checkout's image, chart, one new revision
// and new Pods after, and the guide's apply owns every kardinal CRD), and
// kro is the same. After it:
//   - kardinal doctor passes, warning only of the namespace that has not
//     agreed to share its gates, and every Pipeline is Ready;
//   - every in-flight version is the same Bundle (uid) with the same Graph,
//     steps and PRs; every gate instance, Approval, Verdict, AgentRun,
//     ChangeRequest, ImageVerification, receipt, AgentIdentity,
//     StepTemplate and the AgentIndex is the same object; every receipt
//     keeps the statement and envelope v0.13.0 signed; the AgentIndex maps
//     the agents; the override is the same record, recorded once; the
//     PolicyNamespacesShared condition is what it was;
//   - render labels (#2346, #2359): each promoting version's Graph carries
//     the checkout's graph.RenderLabel, in place, and is not written when
//     v0.13.0's label is the same (no GateChanged then); else rendered once;
//   - the held gates hold for 30s; the approvals given with the v0.13.0 CLI
//     count once the ticket is approved, and the version reaches prod
//     through its PR, with a receipt the new controller signs that names
//     the agent, both approvals with their fields, V's verdict, the
//     verified ticket and SLSA level 3; it and the test receipt v0.13.0
//     signed verify offline with the checkout's CLI; the overridden
//     version's PR merges and its receipt is OVERRIDDEN; S's new version,
//     with the checkout's CLI, carries S's actor stamp;
//   - every stored object reads with this release's types and takes a label.
//
// Step 5 then rolls back, with S's version in flight (its prod PR open),
// started by the new controller. v0.13.0's image and chart run again (one
// more revision, new Pods), the CRDs stay as the guide applied them, kro is
// the same, and v0.13.0's doctor passes with the same warning. The same
// objects and receipts survive, the Graphs follow v0.13.0's render label,
// the receipts the new controller signed verify with the v0.13.0 CLI, S's
// version finishes under v0.13.0 with its PR and a receipt, the held
// version is released, and S ships again with the v0.13.0 CLI. No Bundle
// has two Graphs, no (Bundle, environment) two steps, no PR head two PRs;
// neither the controllers nor kro logged an unexpected error, and kro did
// not restart.
//
// Covers UPG-V0130-01, UPG-V0130-02, UPG-V0130-03, UPG-V0130-04, UPG-V0130-05, UPG-V0130-06.
func TestUpgradeV0130_Estate(t *testing.T) {
	if why := os.Getenv(framework.EnvV0130Unreleased); why != "" {
		t.Skip(why)
	}
	testStart := time.Now()
	e := framework.New(t)
	ctx := context.Background()
	s := &u0130Estate{e: e, from: u012From{pins: "V0130", cli: (*framework.Env).KardinalV0130, guideChart: guideChartV0130}}
	fromVersion, fromImage, fromChart := u012Pins(t, s.from)
	t.Logf("upgrading from v%s (%s, %s) by \"Upgrading from v%s\", then rolling back; Kubernetes 1.%d",
		fromVersion, fromImage, fromChart, fromVersion, serverMinor(t, e))
	kroFrom, kroCheckout := u0130KroVersions(t)
	require.Equal(t, kroFrom, kroCheckout, "the checkout targets kro %s and v%s kro %s: the guide must upgrade kro, and this test with it",
		kroCheckout, fromVersion, kroFrom)
	s.pub = receiptPublicKey(t, e)
	me := whoAmI(t, e)

	// ── The estate, under v0.13.0 ───────────────────────────────────────
	s.ev = newEvidenceApp(t, e)
	s.verifierUser = s.ev.verifier + "@agents.example.com"
	s.done = newArgoApp(t, e, "test", "prod")
	s.done.apply(t, s.done.pipeline(map[string]string{"prod": "pr-review"}))
	policyAgent(t, e, s.done.ns, "shipper", "sponsor-"+s.done.ns+"@example.com", v1alpha1.AutonomyAuto)
	s.shipperName, s.shipper = s.done.ns+"-shipper", "system:serviceaccount:"+s.done.ns+":shipper"

	s.orgEnv = orgEnv(t)
	s.orgGate = s.orgEnv + "-hold"
	e.EnsureNamespace(t, framework.PolicyNamespace)
	org := framework.Gate(framework.PolicyNamespace, s.orgGate, s.orgEnv, openExpr, recheck)
	delete(org.Labels, "kardinal.io/scope") // platform-policies makes it an org gate
	e.CreateGate(t, org)
	s.org = newArgoApp(t, e, "test", s.orgEnv)
	s.org.apply(t, s.org.pipeline(map[string]string{s.orgEnv: "pr-review"}))

	s.shared = newArgoApp(t, e, "prod")
	s.okNS, s.noNS = e.Namespace(t), e.Namespace(t)
	_, err := e.Kube.CoreV1().Namespaces().Patch(ctx, s.okNS, types.MergePatchType,
		[]byte(`{"metadata":{"labels":{"kardinal.io/policy-shareable":"true"}}}`), metav1.PatchOptions{})
	require.NoError(t, err)
	e.CreateGate(t, framework.Gate(s.okNS, "shared-hold", "prod", openExpr, recheck))
	e.CreateGate(t, framework.Gate(s.noNS, "unshared-hold", "prod", openExpr, recheck))
	sp := s.shared.pipeline(nil)
	sp.Spec.PolicyNamespaces = []string{s.okNS, s.noNS}
	s.shared.apply(t, sp)
	framework.Eventually(t, 2*time.Minute, "v"+fromVersion+" flags shared's namespace "+s.noNS, func(ctx context.Context) (bool, string) {
		flagged, names, ready, err := s.notShared(ctx)
		if err != nil {
			return false, err.Error()
		}
		return flagged && slices.Equal(names, []string{s.noNS}) && ready, fmt.Sprintf("not shared %v %v, Ready %v", flagged, names, ready)
	})
	t.Logf("estate: namespaces %v; org gate %s/%s on %s; shared reads %s (agreed) and %s (not)",
		s.namespaces(), framework.PolicyNamespace, s.orgGate, s.orgEnv, s.okNS, s.noNS)

	// The versions, with the v0.13.0 CLI: A's and S's by digest, with their
	// own tokens; the org and shared ones as the test's user.
	byDigest := fixtures.Image + "@" + fixtures.V2Digest
	s.evB = s.created(t, s.cli(t, s.ev.ns, s.ev.author.Kubeconfig, "create", "bundle", pipelineName, "--image", byDigest, "--commit", attestCommit))
	s.doneB1 = s.created(t, s.cli(t, s.done.ns, s.shipperKubeconfig(t), "create", "bundle", pipelineName, "--image", byDigest))
	s.orgB = s.created(t, s.cli(t, s.org.ns, "", "create", "bundle", pipelineName, "--image", fixtures.Image+":"+fixtures.V2))
	s.sb = s.created(t, s.cli(t, s.shared.ns, "", "create", "bundle", pipelineName, "--image", fixtures.Image+":"+fixtures.V2))
	agentStamp := func(ns, b, agent, principal string) v1alpha1.Actor {
		t.Helper()
		got := getBundle(t, e, ns, b)
		require.NotNil(t, got.Spec.Actor, "%s carries an actor stamp", b)
		assert.Equal(t, v1alpha1.ActorAgent, got.Spec.Actor.Kind, "%s is an agent's", b)
		assert.Equal(t, agent, got.Spec.Actor.Agent, "%s names its agent", b)
		assert.Equal(t, principal, got.Spec.Actor.Principal, "%s names the agent's principal", b)
		return *got.Spec.Actor
	}
	evActor := agentStamp(s.ev.ns, s.evB, s.ev.authorName, s.ev.authorUser)
	doneActor := agentStamp(s.done.ns, s.doneB1, s.shipperName, s.shipper)

	// done: through test and prod, both receipts signed.
	e.WaitStepState(t, s.done.ns, pipelineName, s.doneB1, "prod", "WaitingForMerge", promoteTimeout)
	s.done.merge(t, s.done.openPR(t, s.doneB1, "prod"))
	e.WaitBundlePhase(t, s.done.ns, s.doneB1, "Verified", promoteTimeout)
	for _, env := range []string{"test", "prod"} {
		promotionReceipt(t, e, s.done.ns, s.doneB1, env, v1alpha1.ReceiptStored)
	}
	s.cli(t, s.done.ns, "", "verify-receipt", pipelineName, "prod", "--bundle", s.doneB1, "--key", s.pub, "--require", "HEALTHY,!OVERRIDDEN")

	// ev: the attestations gate and V's verdict pass; the change request
	// opens its ticket; A's own approval and alice's name it; cab holds on
	// the pending ticket.
	e.WaitStepState(t, s.ev.ns, pipelineName, s.evB, "test", "Verified", promoteTimeout)
	waitImageVerification(t, e, s.ev.ns, s.evB, "Verified")
	e.WaitGateReady(t, s.ev.ns, s.evB, "prod", attestGate, true, "", promoteTimeout)
	evRuns := waitRuns(t, e, s.ev.ns, s.evB, 1)
	e.WaitGateReady(t, s.ev.ns, s.evB, "prod", journeyVerify, true, "verified: 1 of 1", gateTimeout)
	crName := "prod-change-prod--" + s.evB
	cr := waitChangeRequest(t, e, s.ev.ns, crName, "the prod change request opened its ticket", func(cr *v1alpha1.ChangeRequest) (bool, string) {
		return cr.Status.Ticket != nil && cr.Status.State == "pending", fmt.Sprintf("state %q conditions %v", cr.Status.State, cr.Status.Conditions)
	})
	ticket := cr.Status.Ticket.Key
	s.cli(t, s.ev.ns, s.ev.author.Kubeconfig, "approve", s.evB, "--env", "prod", "--field", "ticketID="+ticket)
	own := approvalRecord(t, e, s.ev.ns, s.evB, "prod", journeyCAB, s.ev.authorUser)
	assert.False(t, own.Counted, "the agent's approval of its own version does not count")
	s.alice = userKubeconfig(t, e, s.ev.ns, u0130Approver, "release-managers")
	s.cli(t, s.ev.ns, s.alice, "approve", s.evB, "--env", "prod", "--field", "ticketID="+ticket)
	require.True(t, approvalRecord(t, e, s.ev.ns, s.evB, "prod", journeyCAB, u0130Approver).Counted, "alice's approval counts")
	e.WaitGateReady(t, s.ev.ns, s.evB, "prod", journeyCAB, false, "change request prod-change: ticket "+ticket+" is pending", gateTimeout)
	promotionReceipt(t, e, s.ev.ns, s.evB, "test", v1alpha1.ReceiptStored)

	// org: the org gate holds the version until a person overrides it with
	// the v0.13.0 CLI; then its PR opens.
	orgName := framework.GateInstanceName(framework.PolicyNamespace, s.orgGate, s.orgEnv, s.orgB)
	e.WaitGateNamed(t, s.org.ns, orgName, promoteTimeout, "blocking", framework.Evaluated(false, "= false"))
	s.cli(t, s.org.ns, "", "override", pipelineName, "--stage", s.orgEnv, "--gate", s.orgGate, "--reason", u0130Override, "--expires-in", "4h")
	e.WaitGateNamed(t, s.org.ns, orgName, gateTimeout, "overridden", framework.Evaluated(true, "OVERRIDDEN by "+me+": "+u0130Override))
	e.WaitStepState(t, s.org.ns, pipelineName, s.orgB, s.orgEnv, "WaitingForMerge", promoteTimeout)
	orgPR := s.org.openPR(t, s.orgB, s.orgEnv)
	framework.Eventually(t, gateTimeout, "the override recorded and audited", func(ctx context.Context) (bool, string) {
		var g v1alpha1.PolicyGate
		if err := e.Client.Get(ctx, types.NamespacedName{Namespace: s.org.ns, Name: orgName}, &g); err != nil {
			return false, err.Error()
		}
		n, err := s.overridden(ctx)
		if err != nil {
			return false, err.Error()
		}
		return len(g.Status.Overrides) == 1 && g.Status.Overrides[0].Audited && n == 1,
			fmt.Sprintf("records %+v, %d GateOverridden", g.Status.Overrides, n)
	})
	promotionReceipt(t, e, s.org.ns, s.orgB, "test", v1alpha1.ReceiptStored)

	// shared: both gates hold prod.
	sharedOK := framework.GateInstanceName(s.okNS, "shared-hold", "prod", s.sb)
	sharedNo := framework.GateInstanceName(s.noNS, "unshared-hold", "prod", s.sb)
	for _, name := range []string{sharedOK, sharedNo} {
		e.WaitGateNamed(t, s.shared.ns, name, promoteTimeout, "blocking", framework.Evaluated(false, "= false"))
	}
	e.NoStep(t, s.shared.ns, pipelineName, s.sb, "prod", 10*time.Second)

	// ── The snapshot ────────────────────────────────────────────────────
	inFlight := []string{s.ev.ns + "/" + s.evB, s.org.ns + "/" + s.orgB, s.shared.ns + "/" + s.sb}
	before := snapshotOf(t, e, s.namespaces()...)
	objsBefore := s.objects(t)
	rcsBefore := s.receipts(t)
	graphsBefore, err := s.graphs(ctx)
	require.NoError(t, err)
	for _, k := range inFlight {
		require.Equal(t, "Promoting", before.bundles[k].Status.Phase, "%s is in flight at the upgrade", k)
		require.Contains(t, graphsBefore, k, "%s has a Graph at the upgrade", k)
	}
	require.Equal(t, orgPR.Number, before.prs[s.org.ns+"/"+s.orgB+"/"+s.orgEnv], "the org version's PR is open at the upgrade")
	// v0.13.0's render label, on every Graph it rendered.
	fromLabel := graphsBefore[inFlight[0]].label
	require.NotEmpty(t, fromLabel, "v%s labels its Graphs %s", fromVersion, graph.LabelRenderVersion)
	for k, g := range graphsBefore {
		require.Equal(t, fromLabel, g.label, "every Graph v%s rendered carries one render label: %s", fromVersion, k)
	}
	t.Logf("before the upgrade: %d Bundles, %d steps, %d Graphs (render label %q, the checkout's %q), %d receipts, %d other objects",
		len(before.bundles), len(before.steps), len(graphsBefore), fromLabel, graph.RenderLabel, len(rcsBefore), len(objsBefore))
	require.Len(t, rcsBefore, 4, "the receipts at the upgrade: done's test and prod, ev's and org's test")
	users := map[string]string{s.ev.authorUser: s.ev.authorName, s.shipper: s.shipperName, s.verifierUser: s.ev.verifier}
	idx := s.indexed(t, users, "under v"+fromVersion)
	override := *s.orgInstance(t)
	require.Len(t, override.Spec.Overrides, 1, "the override given with the v%s CLI", fromVersion)

	rel := e.ExistingRelease(t, upgradeRelease, framework.ControllerNamespace)
	checkoutImage, checkoutChart := os.Getenv(framework.EnvImage), u0120CheckoutChart(t)
	old := u0120Release(t, e, rel)
	require.Equal(t, fromImage, old.image, "the controller runs v%s before the upgrade", fromVersion)
	require.Equal(t, fromChart, old.chart, "the release is v%s's chart before the upgrade", fromVersion)
	require.NotEqual(t, old.image, checkoutImage, "the checkout's image is not v%s's", fromVersion)
	require.NotEmpty(t, old.pods, "v%s's controller Pods", fromVersion)
	require.Equal(t, "deployed", old.status, "helm revision %d of the release is deployed", old.revision)
	for crd, applied := range old.applied {
		require.False(t, applied, "%s: nothing server-side applied it before the guide's step 2", crd)
	}
	var newCRDs []string
	for _, crd := range u0130CheckoutCRDs(t) {
		if _, ok := old.applied[crd]; !ok {
			newCRDs = append(newCRDs, crd)
		}
	}
	t.Logf("CRDs this release adds: %v", newCRDs)
	kroBefore := kroPods(t, e)
	require.NotEmpty(t, kroBefore, "kro's Pods")
	upgradeStart := time.Now()
	guide := fromVersion

	// ── 2. Apply the new CRDs ───────────────────────────────────────────
	e.MustShell(t, framework.Fill(t, framework.GuideBlockFrom(t, guide, "helm show crds"), s.from.guideChart, `"$KARDINAL_E2E_CHART"`))
	for _, crd := range u0130CheckoutCRDs(t) {
		framework.Eventually(t, time.Minute, "CRD "+crd+" established", func(ctx context.Context) (bool, string) {
			u, err := e.Dynamic.Resource(crdGVR).Get(ctx, crd, metav1.GetOptions{})
			if err != nil {
				return false, err.Error()
			}
			return crdCondition(toCRD(t, u.Object), apiextensionsv1.Established), "not Established"
		})
	}

	// ── 3. Upgrade the chart ────────────────────────────────────────────
	upgradeCmd := strings.TrimRight(framework.Fill(t, framework.GuideBlockFrom(t, guide, "helm upgrade"), s.from.guideChart, `"$KARDINAL_E2E_CHART"`), "\n") + ` \
  --set image.repository="${KARDINAL_E2E_IMAGE%:*}" --set image.tag="${KARDINAL_E2E_IMAGE##*:}" --set image.pullPolicy=Never \
  --set render.image.repository="${KARDINAL_E2E_RENDER_IMAGE%:*}" --set render.image.tag="${KARDINAL_E2E_RENDER_IMAGE##*:}" --set render.image.pullPolicy=Never
`
	leaderBefore := rel.LeaderVersion(t)
	up := e.Shell(t, upgradeCmd)
	require.Zero(t, up.Code, up.Output)
	rel.WaitRolledOut(t, 3*time.Minute)
	// The guide waits until kardinal version shows the new controller.
	rel.WaitLeaderVersionChanged(t, leaderBefore, 2*time.Minute)
	upgraded := time.Now()
	// Every controller Pod from the upgrade on, the new ones from their
	// start, and v0.13.0's again after the rollback.
	logs := invariants.CollectSince(t, e, invariants.Dir(t), upgradeStart)

	now := u0120Release(t, e, rel)
	assert.Equal(t, checkoutImage, now.image, "the controller Deployment runs the checkout's image")
	assert.Equal(t, checkoutChart, now.chart, "the release is the checkout's chart")
	assert.Equal(t, old.revision+1, now.revision, "the guide's helm upgrade made one new revision")
	assert.Equal(t, "deployed", now.status, "helm revision %d of the release is deployed", now.revision)
	require.NotEmpty(t, now.pods, "the controller's Pods")
	for uid, image := range now.pods {
		assert.NotContains(t, old.pods, uid, "Pod %s is new", uid)
		assert.Equal(t, checkoutImage, image, "Pod %s runs the checkout's image", uid)
	}
	assert.Len(t, now.applied, len(old.applied)+len(newCRDs), "the kardinal CRDs: v%s's and this release's new kinds", fromVersion)
	for crd, applied := range now.applied {
		assert.True(t, applied, "%s: the guide's server-side apply owns it", crd)
	}
	assert.Equal(t, kroBefore, kroPods(t, e), "kro is not upgraded: the same Pods, no restart")

	// ── 4. Check the result ─────────────────────────────────────────────
	notSharedWarn := regexp.MustCompile(`(?m)Policy namespaces shared +.*` + regexp.QuoteMeta(s.shared.ns+"/"+pipelineName+" ("+s.noNS+")"))
	check := e.MustShell(t, framework.GuideBlockFrom(t, guide, "kardinal doctor"))
	assert.Regexp(t, `(?m)^\d+ check\(s\) passed, 1 warning\(s\)$`, check, "kardinal doctor passes, warning of one check")
	assert.Regexp(t, notSharedWarn, check, "the warning names the Pipeline whose namespace has not agreed, and the namespace")
	for _, ns := range s.namespaces() {
		framework.Eventually(t, 2*time.Minute, "every Pipeline of "+ns+" Ready", func(ctx context.Context) (bool, string) {
			var p v1alpha1.Pipeline
			if err := e.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: pipelineName}, &p); err != nil {
				return false, err.Error()
			}
			c := meta.FindStatusCondition(p.Status.Conditions, "Ready")
			return c != nil && c.Status == metav1.ConditionTrue, fmt.Sprintf("Ready: %v", c)
		})
	}

	// What v0.13.0 stored keeps working.
	s.kept(t, before, inFlight, objsBefore, rcsBefore, idx, override, []string{s.noNS}, "the upgrade")
	s.rendered(t, graphsBefore, fromLabel, graph.RenderLabel, upgradeStart, "the upgrade")
	framework.Consistently(t, 30*time.Second, "the held gates still hold, and nothing reaches prod", func(ctx context.Context) (bool, string) {
		for _, held := range []struct{ ns, bundle, tmpl, name string }{
			{s.ev.ns, s.evB, journeyCAB, ""},
			{s.shared.ns, s.sb, "", sharedOK},
			{s.shared.ns, s.sb, "", sharedNo},
		} {
			if st, ok, err := e.Step(ctx, held.ns, pipelineName, held.bundle, "prod"); err != nil || ok {
				return false, fmt.Sprintf("%s prod: %v %v", held.bundle, st, err)
			}
			var g v1alpha1.PolicyGate
			if held.name != "" {
				if err := e.Client.Get(ctx, types.NamespacedName{Namespace: held.ns, Name: held.name}, &g); err != nil {
					return false, err.Error()
				}
			} else {
				inst, ok, err := e.GateInstance(ctx, held.ns, held.bundle, "prod", held.tmpl)
				if err != nil || !ok {
					return false, fmt.Sprintf("%s %s gate: ok=%v err=%v", held.bundle, held.tmpl, ok, err)
				}
				g = *inst
			}
			if g.Status.Ready {
				return false, framework.DescribeGate(&g)
			}
		}
		return true, ""
	})

	// ── Under the new controller: the change is approved, the overridden
	// version merges, and S ships again ───────────────────────────────
	c := e.CLI(t)
	assert.Contains(t, c.Must(s.done.ns, "verify-receipt", pipelineName, "prod", "--bundle", s.doneB1, "--key", s.pub, "--require", "HEALTHY,!OVERRIDDEN"),
		": every check below that was made passed", "the checkout's CLI verifies the receipt v%s signed", fromVersion)
	e.SetFakeSnowTicket(t, ticket, map[string]interface{}{"state": "-2", "approval": "approved",
		"approvers": []map[string]string{{"name": "Jane Smith", "state": "approved"}}})
	e.WaitGateReady(t, s.ev.ns, s.evB, "prod", journeyCAB, true, "", gateTimeout)
	e.WaitStepState(t, s.ev.ns, pipelineName, s.evB, "prod", "WaitingForMerge", promoteTimeout)
	evPR := s.ev.openPR(t, s.evB, "prod")
	s.ev.merge(t, evPR)
	e.WaitBundlePhase(t, s.ev.ns, s.evB, "Verified", promoteTimeout)
	assert.Equal(t, &evActor, getBundle(t, e, s.ev.ns, s.evB).Spec.Actor, "the agent's version keeps the stamp v%s admitted", fromVersion)
	evProd := promotionReceipt(t, e, s.ev.ns, s.evB, "prod", v1alpha1.ReceiptStored)
	signed := signedPayload(t, evProd.Status.Bundle)
	require.Equal(t, evProd.Spec.Statement, string(signed), "the envelope signs the receipt's statement")
	st, err := receipt.Parse(signed)
	require.NoError(t, err)
	rec := st.Predicate.Kardinal
	require.NotNil(t, rec.Actor)
	assert.Equal(t, s.ev.authorName, rec.Actor.Agent, "prod's receipt names the agent")
	assert.Equal(t, s.ev.sponsor, rec.Actor.Sponsor, "and its sponsor")
	gates := map[string]receipt.Gate{}
	for _, g := range rec.Gates {
		gates[g.Template[strings.LastIndex(g.Template, "/")+1:]] = g
	}
	for _, name := range []string{attestGate, journeyVerify, journeyCAB} {
		require.Contains(t, gates, name, "prod's receipt records the gate %s", name)
		assert.Equal(t, "pass", gates[name].Result, name)
	}
	approvals := map[string]receipt.Approval{}
	for _, ap := range gates[journeyCAB].Approvals {
		approvals[ap.User] = ap
	}
	require.Contains(t, approvals, u0130Approver, "alice's approval, given with the v%s CLI", fromVersion)
	assert.True(t, approvals[u0130Approver].Counted)
	assert.Equal(t, map[string]string{"ticketID": ticket}, approvals[u0130Approver].Fields, "with the field she gave under v%s", fromVersion)
	require.Contains(t, approvals, s.ev.authorUser, "the agent's own approval is recorded")
	assert.False(t, approvals[s.ev.authorUser].Counted)
	require.NotEmpty(t, gates[journeyVerify].Verdicts)
	assert.True(t, gates[journeyVerify].Verdicts[0].Counted, "V's verdict from v%s's AgentRun %s counts", fromVersion, evRuns[0].Name)
	require.Len(t, rec.ChangeRequests, 1)
	assert.Equal(t, ticket, rec.ChangeRequests[0].Ticket)
	assert.Equal(t, "approved", rec.ChangeRequests[0].State)
	assert.True(t, rec.ChangeRequests[0].Verified, "the ticket v%s opened carries the promotion's correlation id", fromVersion)
	require.NotNil(t, rec.Checks.SLSA)
	assert.Equal(t, 3, rec.Checks.SLSA.BuildLevel)
	assert.ElementsMatch(t, []string{receipt.LevelGatesPassed, receipt.LevelApproved, receipt.LevelVerifiedByAgent,
		receipt.LevelHealthy, "SLSA_BUILD_LEVEL_3"}, st.Predicate.VerifiedLevels, "exactly these levels")
	// Both of ev's receipts, v0.13.0's (test) and the new controller's (prod),
	// verify offline with the checkout's CLI.
	cluster := clusterIdentity(t, e)
	offlineVerify := func(cli string, rc *v1alpha1.PromotionReceipt, env, levels string) {
		t.Helper()
		file := filepath.Join(t.TempDir(), env+".sigstore.json")
		assert.NoError(t, os.WriteFile(file, []byte(rc.Status.Bundle), 0o600))
		out, err := offline(t, cli, "verify-receipt", "--file", file, "--key", s.pub, "-n", s.ev.ns, "--pipeline", pipelineName,
			"--env", env, "--subject", fixtures.V2Digest, "--cluster", cluster, "--require", levels)
		assert.NoError(t, err, out)
		assert.Contains(t, out, "receipt "+rc.Name+": every check below that was made passed")
	}
	evTest := promotionReceipt(t, e, s.ev.ns, s.evB, "test", v1alpha1.ReceiptStored)
	const evRequire = "GATES_PASSED,APPROVED,VERIFIED_BY_AGENT,HEALTHY,SLSA_BUILD_LEVEL_3,!OVERRIDDEN,!FREEZE_EXEMPTED"
	offlineVerify(os.Getenv(framework.EnvCLI), evTest, "test", "HEALTHY,!OVERRIDDEN")
	offlineVerify(os.Getenv(framework.EnvCLI), evProd, "prod", evRequire)

	// The overridden version merges; its receipt says it was overridden.
	s.org.merge(t, orgPR)
	e.WaitBundlePhase(t, s.org.ns, s.orgB, "Verified", promoteTimeout)
	orgRC := promotionReceipt(t, e, s.org.ns, s.orgB, s.orgEnv, v1alpha1.ReceiptStored)
	orgSt, err := receipt.Parse(signedPayload(t, orgRC.Status.Bundle))
	require.NoError(t, err)
	assert.Contains(t, orgSt.Predicate.VerifiedLevels, receipt.LevelOverridden, "the receipt of the version past the override")
	n, err := s.overridden(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n, "the override v%s recorded is recorded once", fromVersion)

	// S ships again with the checkout's CLI; its version waits for its prod
	// PR across the rollback.
	r := c.Exec(framework.CLIOptions{Kubeconfig: s.shipperKubeconfig(t)}, c.Args(s.done.ns, "create", "bundle", pipelineName,
		"--image", fixtures.Image+":"+fixtures.V3)...)
	require.Zero(t, r.Code, r.Output())
	doneB2 := bundleCreatedName(t, r.Stdout)
	doneActor2 := agentStamp(s.done.ns, doneB2, s.shipperName, s.shipper)
	assert.Equal(t, doneActor.Sponsor, doneActor2.Sponsor, "the same sponsor")
	e.WaitStepState(t, s.done.ns, pipelineName, doneB2, "prod", "WaitingForMerge", promoteTimeout)
	donePR2 := s.done.openPR(t, doneB2, "prod")

	// ── 5. Roll back ────────────────────────────────────────────────────
	inFlight = []string{s.done.ns + "/" + doneB2, s.shared.ns + "/" + s.sb}
	mid := snapshotOf(t, e, s.namespaces()...)
	objsMid := s.objects(t)
	rcsMid := s.receipts(t)
	graphsMid, err := s.graphs(ctx)
	require.NoError(t, err)
	for _, k := range inFlight {
		require.Equal(t, "Promoting", mid.bundles[k].Status.Phase, "%s is in flight at the rollback", k)
	}
	require.Equal(t, donePR2.Number, mid.prs[s.done.ns+"/"+doneB2+"/prod"], "S's new version has its PR at the rollback")
	rollbackStart := time.Now()
	leaderBefore = rel.LeaderVersion(t)
	e.MustShell(t, framework.GuideBlockFrom(t, guide, "helm rollback"))
	rel.WaitRolledOut(t, 3*time.Minute)
	rel.WaitLeaderVersionChanged(t, leaderBefore, 2*time.Minute)

	back := u0120Release(t, e, rel)
	assert.Equal(t, fromImage, back.image, "the controller runs v%s again", fromVersion)
	assert.Equal(t, fromChart, back.chart, "the release is v%s's chart again", fromVersion)
	assert.Equal(t, now.revision+1, back.revision, "the rollback made one new revision")
	assert.Equal(t, "deployed", back.status, "helm revision %d of the release is deployed", back.revision)
	for uid, image := range back.pods {
		assert.NotContains(t, now.pods, uid, "Pod %s is new", uid)
		assert.Equal(t, fromImage, image, "Pod %s runs v%s's image", uid, fromVersion)
	}
	assert.Equal(t, now.applied, back.applied, "helm rollback leaves the CRDs as the guide applied them")
	assert.Equal(t, kroBefore, kroPods(t, e), "kro is the same: the same Pods, no restart")
	out, err := s.from.cli(e, t, "", "doctor")
	require.NoError(t, err, out)
	assert.Regexp(t, `(?m)^\d+ check\(s\) passed, 1 warning\(s\)$`, out, "v%s's doctor passes, warning of one check", fromVersion)
	assert.Regexp(t, notSharedWarn, out, "the same warning")

	s.kept(t, mid, inFlight, objsMid, rcsMid, idx, override, []string{s.noNS}, "the rollback")
	s.rendered(t, graphsMid, graph.RenderLabel, fromLabel, rollbackStart, "the rollback")
	// The receipts the new controller signed verify with v0.13.0's CLI.
	s.cli(t, s.ev.ns, "", "verify-receipt", pipelineName, "prod", "--bundle", s.evB, "--key", s.pub, "--require", evRequire)
	s.cli(t, s.org.ns, "", "verify-receipt", pipelineName, s.orgEnv, "--bundle", s.orgB, "--key", s.pub, "--require", "OVERRIDDEN")
	offlineVerify(os.Getenv(framework.EnvV0130CLI), evProd, "prod", evRequire)

	// v0.13.0 finishes the version the new controller started, and signs its
	// receipt; the held version is released; S ships with the v0.13.0 CLI.
	s.done.merge(t, donePR2)
	e.WaitBundlePhase(t, s.done.ns, doneB2, "Verified", promoteTimeout)
	promotionReceipt(t, e, s.done.ns, doneB2, "prod", v1alpha1.ReceiptStored)
	assert.Equal(t, &doneActor2, getBundle(t, e, s.done.ns, doneB2).Spec.Actor, "S's version keeps its stamp")
	s.cli(t, s.done.ns, "", "verify-receipt", pipelineName, "prod", "--bundle", doneB2, "--key", s.pub, "--require", "HEALTHY,!OVERRIDDEN")
	assert.Contains(t, c.Must(s.done.ns, "verify-receipt", pipelineName, "prod", "--bundle", doneB2, "--key", s.pub, "--require", "HEALTHY,!OVERRIDDEN"),
		": every check below that was made passed", "and with the checkout's CLI")
	e.SetBundleLabel(t, s.shared.ns, s.sb, openLabel, "true")
	e.WaitBundlePhase(t, s.shared.ns, s.sb, "Verified", promoteTimeout)
	doneB3 := s.created(t, s.cli(t, s.done.ns, s.shipperKubeconfig(t), "create", "bundle", pipelineName, "--image", fixtures.Image+":"+fixtures.V1))
	agentStamp(s.done.ns, doneB3, s.shipperName, s.shipper)
	e.WaitStepState(t, s.done.ns, pipelineName, doneB3, "prod", "WaitingForMerge", promoteTimeout)
	s.done.merge(t, s.done.openPR(t, doneB3, "prod"))
	e.WaitBundlePhase(t, s.done.ns, doneB3, "Verified", promoteTimeout)

	// ── Nothing orphaned or duplicated; the logs ────────────────────────
	end := snapshotOf(t, e, s.namespaces()...)
	stepsPer := map[string][]string{}
	for k, st := range end.steps {
		ns, _, _ := strings.Cut(k, "/")
		key := ns + "/" + st.Spec.BundleName + "/" + st.Spec.Environment
		stepsPer[key] = append(stepsPer[key], st.Name)
	}
	for key, names := range stepsPer {
		assert.Len(t, names, 1, "one PromotionStep for %s: %v", key, names)
	}
	for _, ns := range s.namespaces() {
		graphs, err := e.Dynamic.Resource(framework.GraphGVR).Namespace(ns).List(ctx, metav1.ListOptions{})
		require.NoError(t, err)
		per := map[string]int{}
		for _, g := range graphs.Items {
			per[g.GetLabels()["kardinal.io/bundle"]]++
		}
		for b, n := range per {
			assert.Equal(t, 1, n, "Bundle %s/%s has one Graph", ns, b)
		}
	}
	for _, a := range []*app{s.ev.app, s.done, s.org, s.shared} {
		prs, err := e.Git.PullRequests(ctx, a.repo)
		require.NoError(t, err)
		heads := map[string][]int{}
		for _, pr := range prs {
			heads[pr.Head] = append(heads[pr.Head], pr.Number)
		}
		for h, nums := range heads {
			assert.Len(t, nums, 1, "%s: one PR for head %s, got %v", a.ns, h, nums)
		}
	}
	allKindsIn(t, e, s.namespaces()...)
	logs.Stop()
	u0130Logs(t, logs)
	// The only kro errors the guide's steps may cause: a kardinal kind's
	// schema resolved while the CRDs change (u012SchemaRedirect), before
	// the new controller leads.
	var schema int
	leveled, _, kroErrs := kroErrors(t, e, upgradeStart, func(at time.Time, line string) bool {
		if at.Before(upgraded) && u012OnlySchemaRedirect(line) {
			schema++
			return true
		}
		return false
	})
	t.Logf("kro: %d log lines with a level, %d resolved a schema while the CRDs changed; the test ran %s",
		leveled, schema, time.Since(testStart).Round(time.Second))
	assert.NotZero(t, leveled, "kro's log lines have a level the check reads")
	assert.Empty(t, kroErrs, "kro logged no other error from the upgrade on")
	assert.Equal(t, kroBefore, kroPods(t, e), "kro's Pods did not restart")
}
