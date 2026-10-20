//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/receipt"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/changerequest"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
)

// The evidence-tenancy suite (hack/e2e/up.sh evidence-tenancy, #2062): team
// A in a namespace of the main release (shard default), team B in one of
// shard b (components/shard.sh). Each shard signs receipts with its own key
// and pushes referrers only under its team's prefix of the suite's registry
// (tenancyPrefix). Shards split the load, not trust (docs/sharding.md): each
// shard's controller may read every Secret, so these tests check what the
// API server, the admission policies and the controller keep apart, not
// RBAC between the shards.

// tenancyPrefix is the registry path prefix each shard pushes receipt
// referrers under (up.sh evidence-tenancy).
var tenancyPrefix = map[string]string{"default": "e2e/team-a", "b": "e2e/team-b"}

// namespacedPackKinds are the evidence pack's namespaced kinds: a namespace
// auditor asked for a namespace it is not bound in, or for every namespace,
// may read none of them.
var namespacedPackKinds = []interface{}{"AuditEvent", "Approval", "Bundle", "ChangeRequest", "ChangeRequestTemplate",
	"Pipeline", "PolicyGate", "PolicyGateTemplate", "PromotionReceipt", "Verdict"}

// team is one team's namespace and the version it promotes to prod through
// a ServiceNow change (its own credential, user = its namespace) and an
// approval naming the change.
type team struct {
	*app
	name, shard string
	// change is the ChangeRequest template's name.
	change string
	// repo and digest are the version's image in the suite's registry.
	repo, digest string
	bundle       string
	uid          types.UID
	ticket       string
	correlation  string
	receipts     map[string]*v1alpha1.PromotionReceipt
}

// image is the version's image as the Bundle records it.
func (tm *team) image(t *testing.T) string {
	return framework.NewRegistry(t).Repository(tm.repo) + "@" + tm.digest
}

// changeTemplate creates a per-promotion ServiceNow ChangeRequest template
// for prod whose credential is key token of Secret secret.
func changeTemplate(t *testing.T, e *framework.Env, ns, name, secret, url string, mut ...func(*v1alpha1.ChangeRequestSpec)) {
	t.Helper()
	cr := &v1alpha1.ChangeRequest{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: v1alpha1.ChangeRequestSpec{PerPromotion: true, AppliesTo: []string{"prod"}, Provider: "servicenow",
			PollInterval: "30s",
			ServiceNow: &v1alpha1.ServiceNowChangeRequest{InstanceURL: url,
				ChangeRequestAuth: v1alpha1.ChangeRequestAuth{CredentialsSecretRef: v1alpha1.SecretKeyRef{Name: secret, Key: "token"}},
				Template:          map[string]string{"short_description": "Promote {{ pipeline.name }} to {{ environment.name }}"}}},
	}
	for _, m := range mut {
		m(&cr.Spec)
	}
	require.NoError(t, e.Client.Create(context.Background(), cr))
}

// startTeam sets up team name on shard (default or b) and creates its
// version: seed tag tag copied into the team's registry repository. The
// image's repository is in none of the app's manifests, so the version
// changes only the kustomization's images and the workload keeps running
// podinfo (health notes the image as not verified); what matters here is
// the evidence: its change, approval, receipts and referrers.
func startTeam(t *testing.T, e *framework.Env, name, shard, tag string) *team {
	t.Helper()
	tm := &team{app: newArgoApp(t, e, "test", "prod"), name: name, shard: shard, change: name + "-change",
		repo: tenancyPrefix[shard] + "/podinfo", receipts: map[string]*v1alpha1.PromotionReceipt{}}
	if shard != "default" {
		labelShard(t, e, tm.ns, shard)
	}
	cred := e.AddFakeTicketUser(t, tm.ns, randomHex(t, 12))
	e.CreateSecretData(t, tm.ns, "snow", true, map[string]string{"token": cred})
	changeTemplate(t, e, tm.ns, tm.change, "snow", framework.TicketAPIURL(t))
	g := framework.Gate(tm.ns, "cab", "prod", fmt.Sprintf(`changeRequests[%q].state == "approved" && `+
		`approvals.fields.ticketID.exists(id, id == changeRequests[%q].ticket)`, tm.change, tm.change), recheck)
	g.Spec.Approval = &v1alpha1.GateApprovalPolicy{Required: 1, Fields: []v1alpha1.ApprovalField{
		{Name: "ticketID", Required: true, Description: "ServiceNow change number"}}}
	e.CreateGate(t, g)
	tm.apply(t, tm.pipeline(nil))
	waitShardHolder(t, e, tm.ns, shard, time.Minute)
	tm.digest = framework.NewRegistry(t).Copy(t, tag, tm.repo, tag)
	tm.bundle = e.CreateBundle(t, tm.ns, pipelineName, "--image", tm.image(t))
	return tm
}

// finish promotes the team's version to prod: its change is approved in the
// fake ServiceNow, an approver approves naming it, and both receipts are
// stored.
func (tm *team) finish(t *testing.T) {
	t.Helper()
	e := tm.e
	e.WaitStepState(t, tm.ns, pipelineName, tm.bundle, "test", "Verified", promoteTimeout)
	inst := waitChangeRequest(t, e, tm.ns, tm.change+"-prod--"+tm.bundle, "the instance opened its ticket",
		func(cr *v1alpha1.ChangeRequest) (bool, string) {
			return cr.Status.Ticket != nil && cr.Status.State == "pending", fmt.Sprintf("state %q conditions %v", cr.Status.State, cr.Status.Conditions)
		})
	tm.ticket, tm.correlation = inst.Status.Ticket.Key, inst.Status.CorrelationID
	e.SetFakeSnowTicket(t, tm.ticket, map[string]interface{}{"state": "-2", "approval": "approved",
		"approvers": []map[string]string{{"name": "Jane Smith", "state": "approved"}}})
	c := e.CLI(t)
	kc := userKubeconfig(t, e, tm.ns, "approver@"+tm.name+".example.com")
	r := c.Exec(framework.CLIOptions{Kubeconfig: kc}, c.Args(tm.ns, "approve", pipelineName, "prod", "--field", "ticketID="+tm.ticket)...)
	require.Zero(t, r.Code, r.Output())
	e.WaitStepState(t, tm.ns, pipelineName, tm.bundle, "prod", "Verified", promoteTimeout)
	for _, env := range tm.envs {
		tm.receipts[env] = promotionReceipt(t, e, tm.ns, tm.bundle, env, v1alpha1.ReceiptStored)
	}
	var b v1alpha1.Bundle
	require.NoError(t, e.Client.Get(context.Background(), types.NamespacedName{Namespace: tm.ns, Name: tm.bundle}, &b))
	tm.uid = b.UID
}

// shardB is shard b's release namespace and name (components/shard.sh).
func shardB(t *testing.T) (ns, release string) {
	t.Helper()
	ns, release = os.Getenv("KARDINAL_E2E_SHARD_B_NS"), os.Getenv("KARDINAL_E2E_SHARD_B_RELEASE")
	require.NotEmpty(t, ns, "KARDINAL_E2E_SHARD_B_NS is not set; the suite runs components/shard.sh")
	require.NotEmpty(t, release, "KARDINAL_E2E_SHARD_B_RELEASE is not set; the suite runs components/shard.sh")
	return ns, release
}

// shardBDeployment is shard b's controller Deployment.
func shardBDeployment(t *testing.T, e *framework.Env) *appsv1.Deployment {
	t.Helper()
	ns, release := shardB(t)
	var list appsv1.DeploymentList
	require.NoError(t, e.Client.List(context.Background(), &list, client.InNamespace(ns),
		client.MatchingLabels{"app.kubernetes.io/instance": release}))
	require.Len(t, list.Items, 1, "shard b's controller Deployment")
	return &list.Items[0]
}

// asServiceAccount is a client acting as ServiceAccount ns/name, with the
// groups the API server gives it.
func asServiceAccount(t *testing.T, e *framework.Env, ns, name string) client.Client {
	t.Helper()
	return impersonated(t, e, "system:serviceaccount:"+ns+":"+name,
		"system:serviceaccounts", "system:serviceaccounts:"+ns, "system:authenticated")
}

// referrerResources are the resourceUris of the receipt referrers of
// repo@digest in the suite's registry, and how many referrers the image has
// in all (a referrer without the resource annotation is counted there only).
func referrerResources(t *testing.T, repo, digest string) ([]string, int) {
	t.Helper()
	var out []string
	reg := framework.NewRegistry(t)
	found, err := (&receipt.Registry{Insecure: true}).Referrers(context.Background(), reg.HostRepository(repo), digest,
		func(res string) bool {
			out = append(out, res)
			return false // only the annotation: the receipts themselves are not fetched
		})
	require.NoError(t, err)
	require.False(t, found.Truncated)
	return out, found.Entries
}

// packHolds lists the files of an extracted pack that contain one of
// tokens, as "file: token".
func packHolds(t *testing.T, dir string, tokens ...string) []string {
	t.Helper()
	var hits []string
	require.NoError(t, filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for _, tok := range tokens {
			if tok != "" && bytes.Contains(b, []byte(tok)) {
				rel, _ := filepath.Rel(dir, p)
				hits = append(hits, rel+": "+tok)
			}
		}
		return nil
	}))
	return hits
}

// packFiles are the files the manifest lists with their record counts.
func packFiles(m map[string]interface{}) map[string]float64 {
	out := map[string]float64{}
	files, _ := m["files"].([]interface{})
	for _, f := range files {
		fm, _ := f.(map[string]interface{})
		name, _ := fm["name"].(string)
		n, _ := fm["records"].(float64)
		out[name] = n
	}
	return out
}

// TestTenancy_Evidence: two teams, each with a version promoted to prod
// through its own ServiceNow change and an approval, team A on the main
// release and team B on shard b.
//
//   - Each team's auditor (the chart's auditor role bound in the team's
//     namespace) exports a full pack of its own namespace in which no file
//     names the other team's namespace, Bundle UID or ticket. Asked for the
//     other namespace, it gets a pack (exit 0) with every namespaced kind in
//     coverage.forbidden and no record of it; asked for every namespace
//     (-A), the same, with the warning that names -n <namespace>.
//   - Each receipt is in its version's namespace and names it. One shard's
//     controller may not create a receipt or change one's status in the
//     other shard's namespace (the receipts admission policy of each release
//     exempts only its own controller). verify-receipt --file pins the
//     namespace: team A's receipt presented as team B's fails naming it, and
//     a receipt does not verify with the other shard's key.
//   - No AuditEvent outside a team's namespace names its version (its
//     Bundle and Pipeline, or its ticket key), and some in it do: the same
//     check pointed at the other team's namespace finds every one.
//   - A third namespace on shard b whose gate reads team A's change request
//     template by name gets no instance of it (templates are the
//     namespace's own): its prod gate is blocked naming the template. Its
//     version of team A's image keeps its receipt in the cluster, but gets
//     no referrer in team A's repository (Stored=False,
//     RepositoryNotAllowed): only team A's receipts are referrers there, and
//     no other referrer, annotated or not.
//
// Covers TENANCY-AUDITOR-01, TENANCY-RECORDS-01, TENANCY-RECORDS-02.
func TestTenancy_Evidence(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	c := e.CLI(t)
	a := startTeam(t, e, "team-a", "default", "6.14.0")
	b := startTeam(t, e, "team-b", "b", "6.15.0")
	a.finish(t)
	b.finish(t)

	t.Run("auditor", func(t *testing.T) {
		for _, p := range [][2]*team{{a, b}, {b, a}} {
			me, other := p[0], p[1]
			kc := auditorKubeconfig(t, e, me.ns, "auditor@"+me.name+".example.com")
			dir := t.TempDir()

			pack := filepath.Join(dir, "own.tar.gz")
			r := c.Exec(framework.CLIOptions{Kubeconfig: kc}, c.Args(me.ns, "audit", "evidence-pack", "--since", "1h", "-o", pack)...)
			require.Zero(t, r.Code, r.Output())
			pdir, m := packOf(t, pack)
			assert.Empty(t, m["coverage"].(map[string]interface{})["forbidden"], "%s's auditor reads its whole namespace", me.name)
			assert.Equal(t, []string{me.ns + "/" + me.bundle}, names(ndjson(t, pdir, "bundles.ndjson")))
			assert.Len(t, ndjson(t, pdir, "receipts.ndjson"), 2, "%s's test and prod receipts", me.name)
			assert.Len(t, ndjson(t, pdir, "approvals.ndjson"), 1, "%s's approval", me.name)
			assert.Len(t, ndjson(t, pdir, "changerequests.ndjson"), 1, "%s's change", me.name)
			assert.Empty(t, packHolds(t, pdir, other.ns, string(other.uid), other.ticket),
				"%s's pack names nothing of %s", me.name, other.name)

			pack = filepath.Join(dir, "other.tar.gz")
			r = c.Exec(framework.CLIOptions{Kubeconfig: kc}, c.Args(other.ns, "audit", "evidence-pack", "--since", "1h", "-o", pack)...)
			require.Zero(t, r.Code, "a pack of what the auditor may read, not an error:\n%s", r.Output())
			pdir, m = packOf(t, pack)
			assert.ElementsMatch(t, namespacedPackKinds, m["coverage"].(map[string]interface{})["forbidden"])
			for file, n := range packFiles(m) {
				if strings.HasSuffix(file, ".ndjson") && file != "changewindows.ndjson" && file != "agentidentities.ndjson" {
					assert.Zero(t, n, "%s's auditor exported %s of %s", me.name, file, other.name)
				}
			}
			assert.Empty(t, packHolds(t, pdir, string(other.uid), other.ticket),
				"%s's auditor exported nothing of %s", me.name, other.name)

			pack = filepath.Join(dir, "all.tar.gz")
			r = c.Exec(framework.CLIOptions{Kubeconfig: kc}, c.Args("", "audit", "evidence-pack", "-A", "--since", "1h", "-o", pack)...)
			require.Zero(t, r.Code, r.Output())
			pdir, m = packOf(t, pack)
			cov := m["coverage"].(map[string]interface{})
			assert.ElementsMatch(t, namespacedPackKinds, cov["forbidden"])
			assert.Contains(t, fmt.Sprint(cov["warnings"]), "-n <namespace>")
			assert.Contains(t, r.Stderr, "-n <namespace>")
			assert.Empty(t, packHolds(t, pdir, a.ns, b.ns, string(a.uid), string(b.uid), a.ticket, b.ticket),
				"-A exports no team's records")
		}
	})

	t.Run("receipts", func(t *testing.T) {
		shardNS, _ := shardB(t)
		for _, tm := range []*team{a, b} {
			var list v1alpha1.PromotionReceiptList
			require.NoError(t, e.Client.List(ctx, &list, client.InNamespace(tm.ns)))
			require.Len(t, list.Items, 2, "%s holds its own two receipts only", tm.name)
			for _, rc := range list.Items {
				st, err := receipt.Parse([]byte(rc.Spec.Statement))
				require.NoError(t, err)
				res, err := receipt.ParseResourceURI(st.Predicate.ResourceURI)
				require.NoError(t, err)
				assert.Equal(t, tm.ns, res.Namespace, "%s/%s names its own namespace", rc.Namespace, rc.Name)
				assert.Equal(t, string(tm.uid), st.Predicate.Kardinal.Version.BundleUID)
			}
		}

		// Each shard's controller is refused in the other shard's namespace.
		mainSA := asServiceAccount(t, e, framework.ControllerNamespace, framework.ControllerServiceAccount)
		shardSA := asServiceAccount(t, e, shardNS, shardBDeployment(t, e).Spec.Template.Spec.ServiceAccountName)
		for _, w := range []struct {
			who    string
			as     client.Client
			victim *team
		}{{"shard b's controller", shardSA, a}, {"the main controller", mainSA, b}} {
			src := w.victim.receipts["prod"]
			forged := &v1alpha1.PromotionReceipt{
				ObjectMeta: metav1.ObjectMeta{Name: "forged-" + src.Name[:min(len(src.Name), 40)], Namespace: src.Namespace, Labels: src.Labels},
				Spec:       src.Spec}
			err := w.as.Create(ctx, forged)
			require.Error(t, err, "%s creates a receipt in %s", w.who, w.victim.name)
			assert.Contains(t, err.Error(), "only kardinal writes PromotionReceipts", "%s: refused by admission, not RBAC", w.who)
			var rc v1alpha1.PromotionReceipt
			require.NoError(t, e.Client.Get(ctx, client.ObjectKeyFromObject(src), &rc))
			base := rc.DeepCopy()
			rc.Status.Attempts++
			err = w.as.Status().Patch(ctx, &rc, client.MergeFrom(base))
			require.Error(t, err, "%s changes a receipt's status in %s", w.who, w.victim.name)
			assert.Contains(t, err.Error(), "only kardinal writes PromotionReceipts")
		}

		// verify-receipt --file pins the namespace; each shard signs with its own key.
		cluster := clusterIdentity(t, e)
		keys := map[string]string{"default": receiptPublicKeyIn(t, e, framework.ControllerNamespace), "b": receiptPublicKeyIn(t, e, shardNS)}
		dir := t.TempDir()
		for _, p := range [][2]*team{{a, b}, {b, a}} {
			me, other := p[0], p[1]
			file := filepath.Join(dir, me.name+".sigstore.json")
			require.NoError(t, os.WriteFile(file, []byte(me.receipts["prod"].Status.Bundle), 0o600))
			verify := func(ns, key string) framework.CLIResult {
				return c.Exec(framework.CLIOptions{}, c.Args(ns, "verify-receipt", "--file", file, "--pipeline", pipelineName,
					"--env", "prod", "--subject", me.digest, "--cluster", cluster, "--key", key)...)
			}
			r := verify(me.ns, keys[me.shard])
			require.Zero(t, r.Code, r.Output())
			assert.Contains(t, r.Output(), "every check below that was made passed")
			r = verify(other.ns, keys[me.shard])
			assert.NotZero(t, r.Code, "%s's receipt presented as %s's:\n%s", me.name, other.name, r.Output())
			assert.Contains(t, r.Output(), "not namespace "+other.ns)
			r = verify(me.ns, keys[other.shard])
			assert.NotZero(t, r.Code, "%s's receipt with shard %s's key:\n%s", me.name, other.shard, r.Output())
			assert.NotContains(t, r.Output(), "every check below that was made passed")
		}
	})

	t.Run("audit-events", func(t *testing.T) {
		var all v1alpha1.AuditEventList
		require.NoError(t, e.Client.List(ctx, &all))
		// names reports whether ev records tm's version: its Bundle (an
		// AuditEvent names the Bundle and Pipeline, not the UID) or its
		// change's ticket key (the ChangeRequest records).
		names := func(ev *v1alpha1.AuditEvent, tm *team) (bundle, ticket bool) {
			bundle = ev.Spec.BundleName == tm.bundle && ev.Spec.PipelineName == pipelineName
			ticket = strings.Contains(ev.Spec.Message, tm.ticket)
			return bundle, ticket
		}
		// leaks are the AuditEvents that name a team's version outside the
		// namespace home gives it. matched counts the ones in it, and
		// tickets those naming its ticket, so the check cannot pass on an
		// empty match set.
		check := func(home func(*team) string) (leaks []string, matched, tickets map[string]int) {
			matched, tickets = map[string]int{}, map[string]int{}
			for i := range all.Items {
				ev := &all.Items[i]
				for _, tm := range []*team{a, b} {
					bundle, ticket := names(ev, tm)
					switch {
					case !bundle && !ticket:
					case ev.Namespace != home(tm):
						leaks = append(leaks, fmt.Sprintf("%s/%s (%s %s) names %s's version", ev.Namespace, ev.Name,
							ev.Spec.Action, ev.Spec.BundleName, tm.name))
					default:
						matched[tm.name]++
						if ticket {
							tickets[tm.name]++
						}
					}
				}
			}
			return leaks, matched, tickets
		}
		leaks, matched, tickets := check(func(tm *team) string { return tm.ns })
		assert.Empty(t, leaks, "no AuditEvent outside a team's namespace names its version")
		for _, tm := range []*team{a, b} {
			assert.NotZero(t, matched[tm.name], "%s's AuditEvents are in its namespace", tm.name)
			assert.NotZero(t, tickets[tm.name], "%s's change records name its ticket %s", tm.name, tm.ticket)
		}
		// The check reds: pointed at the other team's namespace, every one
		// of a team's records is a leak.
		swapped, _, _ := check(func(tm *team) string {
			if tm == a {
				return b.ns
			}
			return a.ns
		})
		assert.Len(t, swapped, matched[a.name]+matched[b.name], "the same check, pointed at the other team's namespace, fails")
		t.Logf("AuditEvents of team A's version in its namespace: %d (%d naming the ticket); team B's: %d (%d); swapped: %d leaks",
			matched[a.name], tickets[a.name], matched[b.name], tickets[b.name], len(swapped))
	})

	t.Run("change-requests-and-referrers", func(t *testing.T) {
		// Team B's second namespace: a gate reading team A's template by name,
		// and a version of team A's image.
		other := newArgoApp(t, e, "test", "prod")
		labelShard(t, e, other.ns, "b")
		e.CreateGate(t, framework.Gate(other.ns, "cab", "prod", fmt.Sprintf(`changeRequests[%q].state == "approved"`, a.change), recheck))
		other.apply(t, other.pipeline(nil))
		waitShardHolder(t, e, other.ns, "b", time.Minute)
		bundle := e.CreateBundle(t, other.ns, pipelineName, "--image", a.image(t))
		e.WaitStepState(t, other.ns, pipelineName, bundle, "test", "Verified", promoteTimeout)

		e.WaitGateReady(t, other.ns, bundle, "prod", "cab", false, a.change, 2*time.Minute)
		for _, ns := range []string{a.ns, other.ns} {
			var crs v1alpha1.ChangeRequestList
			require.NoError(t, e.Client.List(ctx, &crs, client.InNamespace(ns), client.MatchingLabels{"kardinal.io/bundle": bundle}))
			assert.Empty(t, crs.Items, "no instance of %s for the other namespace's version, in %s", a.change, ns)
		}

		rc := promotionReceipt(t, e, other.ns, bundle, "test", v1alpha1.ReceiptStored)
		stored := meta.FindStatusCondition(rc.Status.Conditions, "Stored")
		require.NotNil(t, stored, "conditions %+v", rc.Status.Conditions)
		assert.Equal(t, metav1.ConditionFalse, stored.Status, stored.Message)
		assert.Equal(t, "RepositoryNotAllowed", stored.Reason, stored.Message)
		for _, tm := range []*team{a, b} {
			for env, own := range tm.receipts {
				var got v1alpha1.PromotionReceipt
				require.NoError(t, e.Client.Get(ctx, client.ObjectKeyFromObject(own), &got))
				s := meta.FindStatusCondition(got.Status.Conditions, "Stored")
				require.NotNil(t, s, "%s %s: conditions %+v", tm.name, env, got.Status.Conditions)
				assert.Equal(t, metav1.ConditionTrue, s.Status, "%s's %s receipt is a referrer of its own image: %s", tm.name, env, s.Message)
			}
			res, entries := referrerResources(t, tm.repo, tm.digest)
			assert.Len(t, res, 2, "%s's image has its test and prod receipts: %v", tm.name, res)
			assert.Equal(t, len(res), entries, "%s's image has no other referrer, annotated or not", tm.name)
			for _, uri := range res {
				r, err := receipt.ParseResourceURI(uri)
				require.NoError(t, err)
				assert.Equal(t, tm.ns, r.Namespace, "a referrer of %s's image from another namespace: %s", tm.name, uri)
			}
		}
	})
}

// webhookStatus posts the change request webhook of template ns/name, with
// token as its last path segment, to the webhook port of the controller
// Service svcNS/svc through the API server's service proxy, and returns the
// HTTP status.
func webhookStatus(t *testing.T, e *framework.Env, svcNS, svc, ns, name, token string) int {
	t.Helper()
	var code int
	res := e.Kube.CoreV1().RESTClient().Post().
		AbsPath("/api/v1/namespaces", svcNS, "services", "http:"+svc+":webhook", "proxy", "webhook", "changerequests", ns, name, token).
		Do(context.Background())
	res.StatusCode(&code)
	if code == 0 {
		require.NoError(t, res.Error(), "POST the webhook of %s/%s", ns, name)
	}
	return code
}

// TestTenancy_Credentials: a team's provider credentials work only in its
// namespace (#2048's tenancy item), team A on the main release and team B
// on shard b, each with its own fake ServiceNow user.
//
//   - Team B's template names Secret snow, which only team A has: its
//     instance fails with ProviderError naming the Secret not found, and the
//     fake ticketing API gets no request for team B's change. Team B's own
//     snow Secret, without kardinal.io/referenceable, is
//     SecretNotReferenceable, still with no request; labelled, the change
//     is opened with team B's user. No request with team A's user is for
//     team B's change, and each change is created by its own team's user.
//   - Team B's templates for a loopback and a link-local (not a metadata)
//     instance URL are refused by the egress guard (Refused, EgressRefused).
//   - Each template's webhook takes only its own token: team A's on team
//     B's route (through shard b's controller) gets 401, and team B's on
//     team A's route too; each its own is accepted.
//   - Team B cannot use team A's SCM credentials: a ScmProvider naming a
//     Secret in team A's namespace is refused at admission, a Pipeline whose
//     providerRef names team A's ScmProvider waits naming it not found in
//     team B's namespace, and one whose git.secretRef names team A's
//     namespace is not valid.
//
// Covers TENANCY-CREDS-01, TENANCY-CREDS-02.
func TestTenancy_Credentials(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	shardNS, release := shardB(t)
	a := newArgoApp(t, e, "test", "prod")
	b := newArgoApp(t, e, "test", "prod")
	labelShard(t, e, b.ns, "b")

	aCred := e.AddFakeTicketUser(t, a.ns, randomHex(t, 12))
	bCred := e.AddFakeTicketUser(t, b.ns, randomHex(t, 12))
	e.CreateSecretData(t, a.ns, "snow", true, map[string]string{"token": aCred})
	e.CreateSecretData(t, b.ns, "decoy", true, map[string]string{"token": bCred})
	tokens := map[string]string{a.ns: randomHex(t, 16), b.ns: randomHex(t, 16)}
	hook := func(s *v1alpha1.ChangeRequestSpec) {
		s.Webhook = &v1alpha1.ChangeRequestWebhook{SecretRef: v1alpha1.SubscriptionSecretRef{Name: "hook"}}
	}
	for ns, tok := range tokens {
		e.CreateSecretData(t, ns, "hook", true, map[string]string{"token": tok})
		changeTemplate(t, e, ns, "change", "snow", framework.TicketAPIURL(t), hook)
	}
	changeTemplate(t, e, b.ns, "loopback", "decoy", "https://127.0.0.1:8443")
	changeTemplate(t, e, b.ns, "linklocal", "decoy", "https://169.254.1.1")
	e.CreateGate(t, framework.Gate(a.ns, "cab", "prod", `changeRequests["change"].state == "approved"`, recheck))
	e.CreateGate(t, framework.Gate(b.ns, "cab", "prod", `changeRequests["change"].state == "approved" && `+
		`changeRequests["loopback"].state == "approved" && changeRequests["linklocal"].state == "approved"`, recheck))
	a.apply(t, a.pipeline(nil))
	b.apply(t, b.pipeline(nil))
	waitShardHolder(t, e, b.ns, "b", time.Minute)
	aBundle := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2)
	bBundle := e.CreateBundle(t, b.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2)
	// prod's instances are made once test is Verified.
	e.WaitStepState(t, a.ns, pipelineName, aBundle, "test", "Verified", promoteTimeout)
	e.WaitStepState(t, b.ns, pipelineName, bBundle, "test", "Verified", promoteTimeout)
	// A failed call is retried after the poll interval (30s): a Secret
	// change is seen within it.
	const retried = 2 * time.Minute

	t.Run("change-requests", func(t *testing.T) {
		aInst := waitChangeRequest(t, e, a.ns, "change-prod--"+aBundle, "team A's change opened with its own credential",
			func(cr *v1alpha1.ChangeRequest) (bool, string) {
				return cr.Status.Ticket != nil, fmt.Sprintf("conditions %v", cr.Status.Conditions)
			})
		failing := func(what, reason, msg string) func(*v1alpha1.ChangeRequest) (bool, string) {
			return func(cr *v1alpha1.ChangeRequest) (bool, string) {
				c := meta.FindStatusCondition(cr.Status.Conditions, what)
				return c != nil && c.Status == metav1.ConditionTrue && c.Reason == reason && strings.Contains(c.Message, msg) &&
					cr.Status.Ticket == nil, fmt.Sprintf("conditions %v", cr.Status.Conditions)
			}
		}
		bName := "change-prod--" + bBundle
		bInst := waitChangeRequestFor(t, e, b.ns, bName, "team B's change without a Secret of its own", retried,
			failing("ProviderError", "ProviderError", `secret "snow" not found`))
		bKey := changerequest.CorrelationKey(bInst.Status.CorrelationID)
		noRequests := func(why string) {
			t.Helper()
			got, err := e.FakeTicketRequests(ctx, "")
			require.NoError(t, err)
			for _, r := range got {
				assert.NotContains(t, r.Path+"?"+r.Query+r.Body, bKey, "%s: %s %s as %q", why, r.Method, r.Path, r.User)
			}
		}
		noRequests("no Secret in team B's namespace")

		// Team B's own Secret of that name, not yet referenceable.
		require.NoError(t, e.Client.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: b.ns, Name: "snow"},
			StringData: map[string]string{"token": bCred}}))
		waitChangeRequestFor(t, e, b.ns, bName, "team B's Secret is not referenceable", retried,
			failing("ProviderError", "ProviderError", "SecretNotReferenceable"))
		noRequests("an unlabelled Secret")
		var s corev1.Secret
		require.NoError(t, e.Client.Get(ctx, types.NamespacedName{Namespace: b.ns, Name: "snow"}, &s))
		s.Labels = referenceable
		require.NoError(t, e.Client.Update(ctx, &s))
		bInst = waitChangeRequestFor(t, e, b.ns, bName, "team B's change opened with its own credential", retried,
			func(cr *v1alpha1.ChangeRequest) (bool, string) {
				return cr.Status.Ticket != nil, fmt.Sprintf("conditions %v", cr.Status.Conditions)
			})

		tickets, err := e.FakeSnowTickets(ctx)
		require.NoError(t, err)
		created, sysID := map[string]string{}, map[string]string{}
		for _, tk := range tickets {
			created[tk.Number], sysID[tk.Number] = tk.CreatedBy, tk.SysID
		}
		assert.Equal(t, a.ns, created[aInst.Status.Ticket.Key], "team A's change is its user's")
		assert.Equal(t, b.ns, created[bInst.Status.Ticket.Key], "team B's change is its user's")
		reqs, err := e.FakeTicketRequests(ctx, "")
		require.NoError(t, err)
		n := 0
		for _, r := range reqs {
			text := r.Path + "?" + r.Query + r.Body
			if strings.Contains(text, bKey) || strings.Contains(text, bInst.Status.Ticket.Key) ||
				strings.Contains(text, sysID[bInst.Status.Ticket.Key]) {
				n++
				assert.Equal(t, b.ns, r.User, "a request for team B's change as another user: %s %s", r.Method, r.Path)
			}
			if r.User == a.ns {
				assert.NotContains(t, text, bKey, "team A's credential sent for team B's change")
				assert.NotContains(t, text, sysID[bInst.Status.Ticket.Key], "team A's credential sent for team B's change")
			}
		}
		assert.NotZero(t, n, "team B's change was requested")
	})

	t.Run("egress", func(t *testing.T) {
		for name, why := range map[string]string{"loopback": "127.0.0.1 is loopback", "linklocal": "169.254.1.1 is link-local"} {
			waitChangeRequestFor(t, e, b.ns, name+"-prod--"+bBundle, "the egress guard refuses the "+name+" instance URL", retried,
				func(cr *v1alpha1.ChangeRequest) (bool, string) {
					c := meta.FindStatusCondition(cr.Status.Conditions, "Refused")
					return c != nil && c.Status == metav1.ConditionTrue && c.Reason == "EgressRefused" && strings.Contains(c.Message, why),
						fmt.Sprintf("conditions %v", cr.Status.Conditions)
				})
		}
	})

	t.Run("webhooks", func(t *testing.T) {
		svc := map[string][2]string{a.ns: {framework.ControllerNamespace, framework.ControllerDeployment}}
		var services corev1.ServiceList
		require.NoError(t, e.Client.List(ctx, &services, client.InNamespace(shardNS), client.MatchingLabels{"app.kubernetes.io/instance": release}))
		for _, s := range services.Items {
			for _, p := range s.Spec.Ports {
				if p.Name == "webhook" {
					svc[b.ns] = [2]string{shardNS, s.Name}
				}
			}
		}
		require.Contains(t, svc, b.ns, "shard b's controller Service with a webhook port")
		for _, p := range [][2]string{{a.ns, b.ns}, {b.ns, a.ns}} {
			route, foreign := p[0], p[1]
			s := svc[route]
			assert.Equal(t, http.StatusUnauthorized, webhookStatus(t, e, s[0], s[1], route, "change", tokens[foreign]),
				"%s's token on %s's route", foreign, route)
			code := webhookStatus(t, e, s[0], s[1], route, "change", tokens[route])
			assert.True(t, code >= 200 && code < 300, "%s's own token: HTTP %d", route, code)
		}
	})

	t.Run("scm", func(t *testing.T) {
		require.NoError(t, e.Client.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Namespace: a.ns, Name: "team-scm", Labels: referenceable},
			StringData: map[string]string{"token": "team-a-" + randomHex(t, 8)}}))
		provider := func(ns string, ref v1alpha1.ScmSecretKeyRef) *v1alpha1.ScmProvider {
			return &v1alpha1.ScmProvider{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "team-scm"},
				Spec: v1alpha1.ScmProviderSpec{Type: "forgejo", APIURL: os.Getenv(framework.EnvSCMAPI), SecretRef: ref}}
		}
		require.NoError(t, e.Client.Create(ctx, provider(a.ns, v1alpha1.ScmSecretKeyRef{Name: "team-scm"})))
		err := e.Client.Create(ctx, provider(b.ns, v1alpha1.ScmSecretKeyRef{Name: "team-scm", Namespace: a.ns}))
		require.Error(t, err, "a ScmProvider in team B's namespace naming team A's Secret")
		assert.True(t, apierrors.IsInvalid(err), "refused by the CRD: %v", err)
		assert.Contains(t, err.Error(), "a ScmProvider's Secrets are in its own namespace")

		borrow := b.pipeline(nil)
		borrow.Name = "borrow-provider"
		borrow.Spec.Git.ProviderRef = &v1alpha1.ScmProviderRef{Name: "team-scm"}
		b.apply(t, borrow)
		bundle := e.CreateBundle(t, b.ns, borrow.Name, "--image", fixtures.Image+":"+fixtures.V2)
		waitBundleCondition(t, e, b.ns, bundle, "the ScmProvider "+b.ns+"/team-scm is not found")

		git := b.pipeline(nil)
		git.Name = "borrow-git"
		git.Spec.Git.SecretRef.Namespace = a.ns
		b.apply(t, git)
		want := fmt.Sprintf("git.secretRef.namespace %q is not allowed: the Secret must be in the Pipeline's namespace %q", a.ns, b.ns)
		waitPipeline(t, e, b.ns, git.Name, time.Minute, "Ready=False ValidationFailed", func(p *v1alpha1.Pipeline) (bool, string) {
			ok, seen := framework.CondIs(p.Status.Conditions, "Ready", metav1.ConditionFalse, "ValidationFailed")
			return ok && findCond(p.Status.Conditions, "Ready").Message == want, seen
		})
	})
}
