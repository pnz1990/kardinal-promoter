//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/gitserver"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/invariants"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/scale"
)

// guideChartV0120 is the chart reference of the "Upgrading from v0.12.0"
// guide: the test replaces it with the checkout's chart.
const guideChartV0120 = "oci://ghcr.io/pnz1990/charts/kardinal-promoter --version <version>"

// u0120AgentRunDelay is how long the verify gate's AgentRun works before it
// writes its Verdict: long enough to be running when the upgrade starts.
const u0120AgentRunDelay = 4 * time.Minute

// u0120Estate is the estate v0.12.0 runs before the upgrade, one namespace
// per concern:
//
//   - flow: versions mid-promotion (a one-minute bake on uat and prod), prod
//     PRs open for review, a rollback in flight that v0.12.0 created (it goes
//     through test and uat), and a Pipeline with two Verified versions to
//     roll back after the upgrade;
//   - approve: prod behind a two-approver gate, one approval given (alice,
//     with the v0.12.0 CLI) or none;
//   - held: prod behind a gate whose rule is false, edited after the upgrade;
//   - verify: prod behind verify gates, one waiting for an AgentRun still
//     running, one for two people's Verdicts with one recorded;
//   - frozen: an agent's version held at test by kardinal freeze --agents.
type u0120Estate struct {
	e                                   *framework.Env
	flow, approve, held, verify, frozen *scale.Fleet
	repos                               map[string]gitserver.Repo // namespace/pipeline → repo
}

func (s *u0120Estate) fleets() []*scale.Fleet {
	return []*scale.Fleet{s.flow, s.approve, s.held, s.verify, s.frozen}
}

func (s *u0120Estate) pipeline(t *testing.T, f *scale.Fleet, name string, envs []v1alpha1.EnvironmentSpec, edits ...func(*v1alpha1.Pipeline)) string {
	t.Helper()
	var dirs []string
	for _, env := range envs {
		dirs = append(dirs, env.Name)
	}
	repo := f.Repo(t, f.NS+"-"+name, map[string][]string{name: dirs})
	p := f.PipelineSpec(name, repo, envs, edits...)
	f.Apply(t, p, repo)
	s.repos[f.NS+"/"+name] = repo
	return name
}

// envFile is the kustomization of pipeline's env in its repo, as git has it.
func (s *u0120Estate) envFile(t *testing.T, f *scale.Fleet, pipeline, env string) string {
	t.Helper()
	repo := s.repos[f.NS+"/"+pipeline]
	b, err := s.e.Git.ReadFile(context.Background(), repo, repo.Branch, scale.EnvPath(pipeline, env)+"/kustomization.yaml")
	require.NoError(t, err)
	return string(b)
}

// snapshot is what the estate holds now, in u0110Snapshot's shape.
func (s *u0120Estate) snapshot(t *testing.T) u0110Snapshot {
	t.Helper()
	old := &u0110Estate{e: s.e}
	old.auto, old.review, old.approve, old.fleet, old.ops = s.flow, s.approve, s.held, s.verify, s.frozen
	return old.snapshot(t)
}

func u0120Envs(names ...string) []v1alpha1.EnvironmentSpec {
	var envs []v1alpha1.EnvironmentSpec
	for _, n := range names {
		envs = append(envs, v1alpha1.EnvironmentSpec{Name: n})
	}
	return envs
}

// u0120ReceiptKey creates the release namespace's Secret receipt-key with a
// new ECDSA P-256 key (PKCS #8, as docs/receipts.md asks), and returns the
// file of its public key.
func u0120ReceiptKey(t *testing.T, e *framework.Env) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	der, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err)
	_, err = e.Kube.CoreV1().Secrets(framework.ControllerNamespace).Create(context.Background(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "receipt-key", Namespace: framework.ControllerNamespace},
		Data:       map[string][]byte{"cosign.key": pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	pub, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	require.NoError(t, err)
	f := filepath.Join(t.TempDir(), "receipt.pub")
	require.NoError(t, os.WriteFile(f, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pub}), 0o600))
	return f
}

// TestUpgradeV0120_Estate builds an estate under kardinal-promoter v0.12.0 as
// released (its chart, CLI and kro) and follows "Upgrading from v0.12.0" in
// docs/upgrade.md, running the guide's own commands, while versions are in
// flight.
//
// After the upgrade:
//   - kardinal doctor passes, every Pipeline is Ready, and the controller
//     removes the health ref nodes and reader RoleBindings v0.12.0 made
//     (#1283);
//   - each in-flight version keeps its Graph (same uid), PromotionSteps and
//     PR, and finishes: the bakes run out, the reviewer merges the open PRs,
//     the rollback v0.12.0 created goes through test and uat as it was built
//     to;
//   - gates that held a version keep holding it: the two-approver gate
//     until bob's approval completes alice's (2 of 2); the false gate until
//     its template is edited, which now reaches the version in flight
//     (#1360: the instance gets the template's generation, and a GateChanged
//     record is written); the verify gates until the AgentRun that was
//     running finishes and its Verdict counts, and until a second person's
//     Verdict joins the one recorded before;
//   - the freeze holds the agent's version (and the new controller records
//     status.frozenBy) until kardinal unfreeze; the version keeps its actor;
//   - the PromotionReceipt and ChangeRequest CRDs are installed and
//     established, nothing creates either, the receipts admission policy
//     refuses a forged receipt, and the release's auditor ClusterRole exists;
//   - no Bundle has two Graphs, no (Bundle, environment) two steps, no PR
//     head branch two PRs; each namespace passes the invariants; every
//     stored object reads with this release's types;
//   - a rollback of prod after the upgrade restores the earlier version in
//     prod alone (#1311) and is Verified;
//   - with receipts enabled by a second helm upgrade while versions are in
//     flight, those finish and a new version's promotions get signed
//     receipts that kardinal verify-receipt accepts.
//
// Covers UPG-V0120-01, UPG-V0120-02, UPG-V0120-03, UPG-V0120-04, UPG-V0120-05.
func TestUpgradeV0120_Estate(t *testing.T) {
	e := framework.New(t)
	ctx := context.Background()
	t.Logf("Kubernetes 1.%d", serverMinor(t, e))
	seed, err := scale.Seed()
	require.NoError(t, err)
	s := &u0120Estate{e: e, repos: map[string]gitserver.Repo{}}
	newFleet := func(name string) *scale.Fleet { return scale.NewFleet(t, e, scale.NewRNG(seed, name)) }
	s.flow, s.approve, s.held, s.verify, s.frozen = newFleet("flow"), newFleet("approve"), newFleet("held"), newFleet("verify"), newFleet("frozen")
	bundle := func(f *scale.Fleet, p string, i int) string { return f.MustCreateBundle(t, p, scale.Tag(p, i)).Name }
	three := u0120Envs("test", "uat", "prod")

	// ── The estate, under v0.12.0 ───────────────────────────────────────
	var bakes, reviews []string
	for i := range 3 {
		bakes = append(bakes, s.pipeline(t, s.flow, u0110Name("bake", i), three, u0110Bake("uat", "prod")))
	}
	for i := range 2 {
		reviews = append(reviews, s.pipeline(t, s.flow, u0110Name("review", i), u0110Envs("pr-review")))
	}
	steady := s.pipeline(t, s.flow, "steady", three)
	rbPre := s.pipeline(t, s.flow, "rollback-pre", three, u0110Bake("test", "uat"))
	fresh := s.pipeline(t, s.flow, "fresh", u0120Envs("test", "prod"))

	gate := framework.Gate(s.approve.NS, "two-approvers", "prod", "true", recheck)
	gate.Spec.Approval = &v1alpha1.GateApprovalPolicy{Required: 2, AllowedGroups: []string{"release-managers"}}
	e.CreateGate(t, gate)
	alice := userKubeconfig(t, e, s.approve.NS, "alice@example.com", "release-managers")
	bob := userKubeconfig(t, e, s.approve.NS, "bob@example.com", "release-managers")
	var approves []string
	for i := range 2 {
		approves = append(approves, s.pipeline(t, s.approve, u0110Name("approve", i), three))
	}

	e.CreateGate(t, framework.Gate(s.held.NS, "release-window", "prod", "false", recheck))
	var helds []string
	for i := range 2 {
		helds = append(helds, s.pipeline(t, s.held, u0110Name("held", i), three))
	}

	agent := registerAgent(t, e, s.verify.NS, "qa", "bob@example.com",
		map[string]string{"PROBE_DELAY": u0120AgentRunDelay.String(), "PROBE_RESULTS": "pass"})
	byRunGate := framework.Gate(s.verify.NS, "agent-check", "prod", "true", recheck)
	byRunGate.Spec.Verify = &v1alpha1.GateVerifyPolicy{Check: "smoke", Required: 1, AllowedAgents: []string{agent},
		Run: &v1alpha1.GateVerifyRun{Template: agent, Count: 1, Namespace: s.verify.NS}}
	e.CreateGate(t, byRunGate)
	byPersonGate := framework.Gate(s.verify.NS, "people-check", "live", "true", recheck)
	byPersonGate.Spec.Verify = &v1alpha1.GateVerifyPolicy{Check: "explore", Required: 2}
	e.CreateGate(t, byPersonGate)
	byRun := s.pipeline(t, s.verify, "by-run", u0120Envs("uat", "prod"))
	byPerson := s.pipeline(t, s.verify, "by-person", u0120Envs("qa", "live"))
	verifier := func(user string) string {
		kc := userKubeconfig(t, e, s.verify.NS, user)
		name := "verifier-" + strings.NewReplacer("@", "-", ".", "-").Replace(user)
		_, err := e.Kube.RbacV1().RoleBindings(s.verify.NS).Create(ctx, &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: s.verify.NS},
			RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: verifierRole},
			Subjects:   []rbacv1.Subject{{Kind: rbacv1.UserKind, APIGroup: rbacv1.GroupName, Name: user}},
		}, metav1.CreateOptions{})
		require.NoError(t, err)
		waitBoundNamed(t, e, s.verify.NS, name)
		return kc
	}
	carol, dave := verifier("carol@example.com"), verifier("dave@example.com")

	shipped := s.pipeline(t, s.frozen, "shipped", u0120Envs("test", "prod"))
	fixer := policyAgent(t, e, s.frozen.NS, "fixer", "sponsor-"+s.frozen.NS+"@example.com", v1alpha1.AutonomyAuto)
	t.Logf("estate: %d Pipelines in %d namespaces", len(s.repos), len(s.fleets()))

	// Versions that finish under v0.12.0: two on steady (the rollback after
	// the upgrade returns prod to the first), and rollback-pre's.
	// steady's versions are pinned by digest, so a rollback changes prod
	// alone (#1311); a tag-only one goes through test and uat (#2175).
	pinned := func(i int) string {
		b := s.flow.BundleSpec(steady, scale.Tag(steady, i))
		b.Spec.Images[0].Digest = fmt.Sprintf("sha256:%064x", i)
		require.NoError(t, e.Client.Create(ctx, b))
		return b.Name
	}
	steady1 := pinned(1)
	e.WaitBundlePhase(t, s.flow.NS, steady1, "Verified", promoteTimeout)
	steady2 := pinned(2)
	rbPre1 := bundle(s.flow, rbPre, 1)
	e.WaitBundlePhase(t, s.flow.NS, steady2, "Verified", promoteTimeout)
	e.WaitBundlePhase(t, s.flow.NS, rbPre1, "Verified", promoteTimeout)
	rbPre2 := bundle(s.flow, rbPre, 2)
	e.WaitBundlePhase(t, s.flow.NS, rbPre2, "Verified", 2*promoteTimeout)

	// Versions in flight across the upgrade.
	inFlight := map[string]string{} // namespace/bundle → pipeline
	add := func(f *scale.Fleet, p string, i int) string {
		b := bundle(f, p, i)
		inFlight[f.NS+"/"+b] = p
		return b
	}
	for _, p := range reviews {
		add(s.flow, p, 1)
	}
	approveB := map[string]string{}
	for _, p := range approves {
		approveB[p] = add(s.approve, p, 1)
	}
	heldB := map[string]string{}
	for _, p := range helds {
		heldB[p] = add(s.held, p, 1)
	}
	byPersonB := add(s.verify, byPerson, 1)

	// The freeze, then the agent ships: its version waits at test.
	out, err := e.KardinalV0120(t, s.frozen.NS, "freeze", "--agents", "--reason", "INC-0120 agents paused", "--yes")
	require.NoError(t, err, out)
	freeze := "kardinal-freeze-agents-" + s.frozen.NS
	e.WaitChangeWindow(t, freeze, gateTimeout, "active", func(w *v1alpha1.ChangeWindow) bool { return w.Status.Active })
	out, err = e.KardinalV0120(t, s.frozen.NS, "--kubeconfig", fixer.Kubeconfig, "create", "bundle", shipped,
		"--image", scale.ImageRepo+":"+scale.Tag(shipped, 1))
	require.NoError(t, err, out)
	agentB := bundleCreatedName(t, out)
	inFlight[s.frozen.NS+"/"+agentB] = shipped
	e.WaitStepMessage(t, s.frozen.NS, shipped, agentB, "test", "Pending", "frozen by "+freeze, promoteTimeout)

	for p, b := range approveB {
		e.WaitGateReady(t, s.approve.NS, b, "prod", "two-approvers", false, "waiting for approvals: 0 of 2", gateTimeout)
		if p == approves[0] {
			out, err := e.KardinalV0120(t, s.approve.NS, "--kubeconfig", alice, "approve", b, "--env", "prod")
			require.NoError(t, err, out)
			e.WaitGateReady(t, s.approve.NS, b, "prod", "two-approvers", false, "waiting for approvals: 1 of 2 (alice@example.com)", gateTimeout)
		}
	}
	for _, b := range heldB {
		e.WaitGateReady(t, s.held.NS, b, "prod", "release-window", false, "false = false", gateTimeout)
	}
	e.WaitStepState(t, s.verify.NS, byPerson, byPersonB, "qa", "Verified", promoteTimeout)
	out, err = e.KardinalV0120(t, s.verify.NS, "--kubeconfig", carol, "verify", byPerson, "qa", "--check", "explore",
		"--pass", "--summary", "checked before the upgrade")
	require.NoError(t, err, out)
	e.WaitGateReady(t, s.verify.NS, byPersonB, "live", "people-check", false, "1 of 2", gateTimeout)
	for _, p := range reviews {
		for k, v := range inFlight {
			if v == p {
				_, b, _ := strings.Cut(k, "/")
				e.WaitStepState(t, s.flow.NS, p, b, "prod", "WaitingForMerge", promoteTimeout)
			}
		}
	}

	// Last, so they are mid-way at the upgrade: the rollback, the bakes and
	// the AgentRun. v0.12.0 rolls rollback-pre's prod back through test and
	// uat, with a bake on each.
	out, err = e.KardinalV0120(t, s.flow.NS, "rollback", rbPre, "--env", "prod")
	require.NoError(t, err, out)
	m := rbCreated.FindStringSubmatch(out)
	require.Len(t, m, 3, "kardinal rollback names the Bundle it created:\n%s", out)
	rbPreB := m[1]
	assert.Equal(t, rbPre1, m[2], "the rollback restores rollback-pre's first version")
	inFlight[s.flow.NS+"/"+rbPreB] = rbPre
	for _, p := range bakes {
		add(s.flow, p, 1)
	}
	byRunB := add(s.verify, byRun, 1)
	var runBefore v1alpha1.AgentRun
	framework.Eventually(t, promoteTimeout, "the AgentRun of "+byRunB+" running", func(ctx context.Context) (bool, string) {
		runs, err := agentRuns(ctx, e, s.verify.NS, byRunB)
		if err != nil {
			return false, err.Error()
		}
		if len(runs) != 1 {
			return false, fmt.Sprintf("%d AgentRuns", len(runs))
		}
		runBefore = runs[0]
		return runs[0].Status.Phase == v1alpha1.AgentRunRunning, "phase " + runs[0].Status.Phase
	})
	framework.Eventually(t, 3*time.Minute, "a step HealthChecking (baking) in "+s.flow.NS, func(ctx context.Context) (bool, string) {
		var sl v1alpha1.PromotionStepList
		if err := e.Client.List(ctx, &sl, client.InNamespace(s.flow.NS)); err != nil {
			return false, err.Error()
		}
		for _, st := range sl.Items {
			if st.Status.State == "HealthChecking" {
				return true, ""
			}
		}
		return false, fmt.Sprintf("%d steps, none HealthChecking", len(sl.Items))
	})

	before := s.snapshot(t)
	for k := range inFlight {
		b, ok := before.bundles[k]
		if assert.True(t, ok, "%s exists at the upgrade", k) {
			assert.NotContains(t, []string{"Verified", "Failed", "Superseded", "Rejected"}, b.Status.Phase,
				"%s is in flight at the upgrade", k)
		}
	}
	bp, sp := before.phases()
	t.Logf("before the upgrade: Bundles %v, steps %v, %d Graphs, %d PRs", bp, sp, len(before.graphs), len(before.prs))
	for _, state := range []string{"Verified", "WaitingForMerge", "HealthChecking"} {
		assert.NotZero(t, sp[state], "the estate has a %s step at the upgrade: %v", state, sp)
	}
	var stored v1alpha1.Bundle
	require.NoError(t, e.Client.Get(ctx, types.NamespacedName{Namespace: s.frozen.NS, Name: agentB}, &stored))
	require.NotNil(t, stored.Spec.Actor, "v0.12.0 stamped the agent's version")
	agentActor := *stored.Spec.Actor
	assert.Equal(t, v1alpha1.ActorAgent, agentActor.Kind)
	readerRole := framework.ChartFullname(upgradeRelease) + "-graph-reader"
	for _, f := range s.fleets() {
		_, err := e.Kube.RbacV1().RoleBindings(f.NS).Get(ctx, readerRole+"-"+f.NS, metav1.GetOptions{})
		require.NoError(t, err, "v0.12 has a reader RoleBinding in %s", f.NS)
	}

	// Every in-flight version has its Graph at the snapshot, and each
	// review version its prod PR: the checks after the upgrade compare
	// these, so none may be missing.
	for k := range inFlight {
		_, ok := before.graphs[k]
		require.True(t, ok, "%s has a Graph at the upgrade", k)
	}
	for k, p := range inFlight {
		if !slices.Contains(reviews, p) {
			continue
		}
		ns, b, _ := strings.Cut(k, "/")
		_, ok := before.prs[ns+"/"+b+"/prod"]
		require.True(t, ok, "%s has its prod PR at the upgrade", k)
	}
	upgradeStart := time.Now()

	// ── 2. Apply the new CRDs ───────────────────────────────────────────
	e.MustShell(t, framework.Fill(t, framework.GuideBlockFrom(t, "0.12.0", "helm show crds"), guideChartV0120, `"$KARDINAL_E2E_CHART"`))
	for _, crd := range []string{"promotionreceipts.kardinal.io", "changerequests.kardinal.io"} {
		framework.Eventually(t, time.Minute, "CRD "+crd+" established", func(ctx context.Context) (bool, string) {
			u, err := e.Dynamic.Resource(crdGVR).Get(ctx, crd, metav1.GetOptions{})
			if err != nil {
				return false, err.Error()
			}
			return crdCondition(toCRD(t, u.Object), apiextensionsv1.Established), "not Established"
		})
	}

	// ── 3. Upgrade the chart ────────────────────────────────────────────
	rel := e.ExistingRelease(t, upgradeRelease, framework.ControllerNamespace)
	upgradeCmd := strings.TrimRight(framework.Fill(t, framework.GuideBlockFrom(t, "0.12.0", "helm upgrade"), guideChartV0120, `"$KARDINAL_E2E_CHART"`), "\n") + ` \
  --set image.repository="${KARDINAL_E2E_IMAGE%:*}" --set image.tag="${KARDINAL_E2E_IMAGE##*:}" --set image.pullPolicy=Never
`
	up := e.Shell(t, upgradeCmd)
	require.Zero(t, up.Code, up.Output)
	rel.WaitRolledOut(t, 3*time.Minute)
	for _, c := range rel.Deployment(t).Spec.Template.Spec.Containers {
		if c.Name == "controller" {
			assert.Equal(t, os.Getenv(framework.EnvImage), c.Image, "the controller runs this checkout's image after the upgrade")
		}
	}

	// ── 4. Check the result ─────────────────────────────────────────────
	check := e.MustShell(t, framework.GuideBlockFrom(t, "0.12.0", "kardinal doctor"))
	assert.Regexp(t, `(?m)^\d+ check\(s\) passed$`, check, "kardinal doctor passes every check")
	for _, f := range s.fleets() {
		ns := f.NS
		framework.Eventually(t, 2*time.Minute, "every Pipeline of "+ns+" Ready", func(ctx context.Context) (bool, string) {
			var pl v1alpha1.PipelineList
			if err := e.Client.List(ctx, &pl, client.InNamespace(ns)); err != nil {
				return false, err.Error()
			}
			var not []string
			for _, p := range pl.Items {
				if c := meta.FindStatusCondition(p.Status.Conditions, "Ready"); c == nil || c.Status != metav1.ConditionTrue {
					not = append(not, fmt.Sprintf("%s: %v", p.Name, c))
				}
			}
			return len(not) == 0, strings.Join(not, "; ")
		})
	}
	// #1283: the reader RoleBindings and the health ref nodes are removed.
	for _, f := range s.fleets() {
		ns := f.NS
		framework.Eventually(t, 2*time.Minute, "the reader RoleBinding of "+ns+" deleted", func(ctx context.Context) (bool, string) {
			_, err := e.Kube.RbacV1().RoleBindings(ns).Get(ctx, readerRole+"-"+ns, metav1.GetOptions{})
			return apierrors.IsNotFound(err), fmt.Sprintf("reader RoleBinding: %v", err)
		})
		framework.Eventually(t, 2*time.Minute, "the Graphs of "+ns+" without health ref nodes", func(ctx context.Context) (bool, string) {
			list, err := e.Dynamic.Resource(framework.GraphGVR).Namespace(ns).List(ctx, metav1.ListOptions{})
			if err != nil {
				return false, err.Error()
			}
			var left []string
			for _, g := range list.Items {
				nodes, _, _ := unstructured.NestedSlice(g.Object, "spec", "nodes")
				for _, n := range nodes {
					if id, _ := n.(map[string]interface{})["id"].(string); strings.HasPrefix(id, "health") && len(id) > 6 && id[6] >= 'A' && id[6] <= 'Z' {
						left = append(left, g.GetName()+"/"+id)
					}
				}
			}
			return len(left) == 0, strings.Join(left, ", ")
		})
	}

	// Right after the upgrade: every in-flight version keeps its Graph, its
	// steps and its PRs.
	after := s.snapshot(t)
	for k := range inFlight {
		assert.Equal(t, before.graphs[k], after.graphs[k], "%s keeps its Graph (same uid)", k)
		assert.Equal(t, before.bundles[k].UID, after.bundles[k].UID, "%s is the same Bundle", k)
	}
	for k, st := range before.steps {
		ns, _, _ := strings.Cut(k, "/")
		if _, ok := inFlight[ns+"/"+st.Spec.BundleName]; !ok {
			continue
		}
		now, ok := after.steps[k]
		if assert.True(t, ok, "step %s survives the upgrade", k) {
			assert.Equal(t, st.UID, now.UID, "step %s is the same object", k)
		}
	}
	for k, n := range before.prs {
		ns, rest, _ := strings.Cut(k, "/")
		b, _, _ := strings.Cut(rest, "/")
		if _, ok := inFlight[ns+"/"+b]; ok {
			assert.Equal(t, n, after.prs[k], "%s keeps PR #%d", k, n)
		}
	}

	// The new kinds are installed and inert: no receipt, no change request,
	// and only kardinal may write a receipt.
	var receipts v1alpha1.PromotionReceiptList
	require.NoError(t, e.Client.List(ctx, &receipts))
	assert.Empty(t, receipts.Items, "receipts are off by default: none is created")
	var crs v1alpha1.ChangeRequestList
	require.NoError(t, e.Client.List(ctx, &crs))
	assert.Empty(t, crs.Items, "no gate reads a change request: none is created")
	sum := sha256.Sum256([]byte("{}"))
	err = e.Client.Create(ctx, &v1alpha1.PromotionReceipt{
		ObjectMeta: metav1.ObjectMeta{Name: "forged", Namespace: s.flow.NS},
		Spec:       v1alpha1.PromotionReceiptSpec{Statement: "{}", StatementDigest: "sha256:" + hex.EncodeToString(sum[:])},
	})
	require.Error(t, err, "a forged receipt is refused")
	assert.Contains(t, err.Error(), "only kardinal writes PromotionReceipts")
	_, err = e.Kube.RbacV1().ClusterRoles().Get(ctx, framework.ChartFullname(upgradeRelease)+"-auditor", metav1.GetOptions{})
	assert.NoError(t, err, "the chart's auditor ClusterRole")

	// Gates that held keep holding.
	framework.Consistently(t, 30*time.Second, "the held gates still hold, and nothing reaches their environments", func(ctx context.Context) (bool, string) {
		for p, b := range approveB {
			if st, ok, err := e.Step(ctx, s.approve.NS, p, b, "prod"); err != nil || ok {
				return false, fmt.Sprintf("%s prod: %v %v", b, st, err)
			}
		}
		for p, b := range heldB {
			if st, ok, err := e.Step(ctx, s.held.NS, p, b, "prod"); err != nil || ok {
				return false, fmt.Sprintf("%s prod: %v %v", b, st, err)
			}
			g, ok, err := e.GateInstance(ctx, s.held.NS, b, "prod", "release-window")
			if err != nil || !ok || g.Status.Ready {
				return false, fmt.Sprintf("%s gate: %v %v", b, ok, err)
			}
		}
		if st, ok, err := e.Step(ctx, s.verify.NS, byPerson, byPersonB, "live"); err != nil || ok {
			return false, fmt.Sprintf("%s live: %v %v", byPersonB, st, err)
		}
		return true, ""
	})
	e.StepHeld(t, s.frozen.NS, shipped, agentB, "test", "frozen by "+freeze, 10*time.Second)
	// The new controller records the freeze on the step v0.12.0 created (#1974).
	e.WaitStep(t, s.frozen.NS, shipped, agentB, "test", gateTimeout, "status.frozenBy names the freeze",
		func(ps *v1alpha1.PromotionStep) (bool, string) {
			if ps.Status.FrozenBy == nil {
				return false, "frozenBy unset"
			}
			return ps.Status.FrozenBy.Name == freeze, fmt.Sprintf("%+v", *ps.Status.FrozenBy)
		})

	// ── Everything in flight finishes ───────────────────────────────────
	var reviewRepos []gitserver.Repo
	for _, p := range reviews {
		reviewRepos = append(reviewRepos, s.repos[s.flow.NS+"/"+p])
	}
	scale.Review(t, e.Git, nil, reviewRepos...)
	c := e.CLI(t)
	for _, p := range approves {
		b := approveB[p]
		r := c.Exec(framework.CLIOptions{Kubeconfig: bob}, c.Args(s.approve.NS, "approve", b, "--env", "prod")...)
		require.Equal(t, 0, r.Code, r.Output())
		if p == approves[0] {
			e.WaitGateReady(t, s.approve.NS, b, "prod", "two-approvers", true, "(2 of 2)", gateTimeout)
			continue
		}
		e.WaitGateReady(t, s.approve.NS, b, "prod", "two-approvers", false, "waiting for approvals: 1 of 2 (bob@example.com)", gateTimeout)
		r = c.Exec(framework.CLIOptions{Kubeconfig: alice}, c.Args(s.approve.NS, "approve", b, "--env", "prod")...)
		require.Equal(t, 0, r.Code, r.Output())
	}
	// #1360: the edit reaches the versions v0.12.0 started.
	for _, b := range heldB {
		g, ok, err := e.GateInstance(ctx, s.held.NS, b, "prod", "release-window")
		require.NoError(t, err)
		require.True(t, ok)
		assert.NotContains(t, g.Annotations, "kardinal.io/gate-template-generation", "v0.12.0 copied the gate without the template's revision")
	}
	gen := editGate(t, e, s.held.NS, "release-window", "true")
	for _, b := range heldB {
		g := e.WaitGateReady(t, s.held.NS, b, "prod", "release-window", true, "true = true", gateTimeout)
		assert.Equal(t, fmt.Sprint(gen), g.Annotations["kardinal.io/gate-template-generation"], "the instance names the edited revision")
	}
	framework.Eventually(t, time.Minute, "a GateChanged record for each held version", func(ctx context.Context) (bool, string) {
		var evs v1alpha1.AuditEventList
		if err := e.Client.List(ctx, &evs, client.InNamespace(s.held.NS), client.MatchingLabels{"kardinal.io/action": "GateChanged"}); err != nil {
			return false, err.Error()
		}
		seen := map[string]bool{}
		for _, ev := range evs.Items {
			seen[ev.Spec.BundleName] = true
		}
		for _, b := range heldB {
			if !seen[b] {
				return false, fmt.Sprintf("none for %s in %d records", b, len(evs.Items))
			}
		}
		return true, ""
	})
	// The verify gates: the AgentRun that was running finishes and counts;
	// dave's Verdict joins carol's.
	framework.Eventually(t, u0120AgentRunDelay+3*time.Minute, "the AgentRun of "+byRunB+" Succeeded", func(ctx context.Context) (bool, string) {
		runs, err := agentRuns(ctx, e, s.verify.NS, byRunB)
		if err != nil {
			return false, err.Error()
		}
		if len(runs) != 1 {
			return false, fmt.Sprintf("%d AgentRuns", len(runs))
		}
		if runs[0].UID != runBefore.UID {
			t.Fatalf("AgentRun %s (uid %s) is not the run that was running at the upgrade (%s, uid %s)",
				runs[0].Name, runs[0].UID, runBefore.Name, runBefore.UID)
		}
		return runs[0].Status.Phase == v1alpha1.AgentRunSucceeded, fmt.Sprintf("phase %s: %s", runs[0].Status.Phase, runs[0].Status.Message)
	})
	assert.True(t, runBefore.CreationTimestamp.Time.Before(upgradeStart), "the AgentRun was created before the upgrade began")
	e.WaitGateReady(t, s.verify.NS, byRunB, "prod", "agent-check", true, "verified: 1 of 1", gateTimeout)
	r := c.Exec(framework.CLIOptions{Kubeconfig: dave}, c.Args(s.verify.NS, "verify", byPerson, "qa", "--check", "explore",
		"--pass", "--summary", "checked after the upgrade")...)
	require.Equal(t, 0, r.Code, r.Output())
	e.WaitGateReady(t, s.verify.NS, byPersonB, "live", "people-check", true, "2 of 2", gateTimeout)
	// The freeze lifted: the agent's version goes on, with its actor.
	out = c.Must(s.frozen.NS, "unfreeze", freeze)
	assert.Contains(t, out, "Freeze "+freeze+" lifted")

	for k := range inFlight {
		ns, b, _ := strings.Cut(k, "/")
		e.WaitBundlePhase(t, ns, b, "Verified", 2*promoteTimeout)
	}
	require.NoError(t, e.Client.Get(ctx, types.NamespacedName{Namespace: s.frozen.NS, Name: agentB}, &stored))
	assert.Equal(t, &agentActor, stored.Spec.Actor, "the agent's version keeps its actor")
	// The rollback v0.12.0 created went through test and uat.
	for _, env := range []string{"test", "uat", "prod"} {
		e.WaitStepState(t, s.flow.NS, rbPre, rbPreB, env, "Verified", time.Minute)
	}
	assert.Contains(t, s.envFile(t, s.flow, rbPre, "prod"), scale.Tag(rbPre, 1), "rollback-pre's prod runs its first version again")

	// ── Nothing orphaned or duplicated ──────────────────────────────────
	end := s.snapshot(t)
	stepsPer := map[string][]string{}
	for k, st := range end.steps {
		ns, _, _ := strings.Cut(k, "/")
		key := ns + "/" + st.Spec.BundleName + "/" + st.Spec.Environment
		stepsPer[key] = append(stepsPer[key], st.Name)
	}
	for key, names := range stepsPer {
		assert.Len(t, names, 1, "one PromotionStep for %s: %v", key, names)
	}
	for _, f := range s.fleets() {
		graphs, err := e.Dynamic.Resource(framework.GraphGVR).Namespace(f.NS).List(ctx, metav1.ListOptions{})
		require.NoError(t, err)
		per := map[string]int{}
		for _, g := range graphs.Items {
			per[g.GetLabels()["kardinal.io/bundle"]]++
		}
		for b, n := range per {
			assert.Equal(t, 1, n, "Bundle %s/%s has one Graph", f.NS, b)
		}
	}
	for k, repo := range s.repos {
		prs, err := e.Git.PullRequests(ctx, repo)
		require.NoError(t, err)
		heads := map[string][]int{}
		for _, pr := range prs {
			heads[pr.Head] = append(heads[pr.Head], pr.Number)
		}
		for h, nums := range heads {
			assert.Len(t, nums, 1, "%s: one PR for head %s, got %v", k, h, nums)
		}
	}
	for _, f := range s.fleets() {
		o := invariants.Options{Namespace: f.NS, Targets: f.Targets(), Image: scale.ImageRepo, SeedTag: scale.SeedTag,
			Start: time.Now().Add(-time.Hour), AllowEmpty: false}
		if f == s.flow {
			o.Skip = map[string]string{"env-content": "rollback-pre's rollback restores an older version on purpose; its content is checked above"}
		}
		invariants.Check(t, e, o)
	}
	u0120AllKinds(t, e, s)

	// ── A rollback after the upgrade changes prod alone (#1311) ─────────
	out, err = e.Kardinal(t, s.flow.NS, "rollback", steady, "--env", "prod")
	require.NoError(t, err, out)
	m = rbCreated.FindStringSubmatch(out)
	require.Len(t, m, 3, "kardinal rollback names the Bundle it created:\n%s", out)
	rb := m[1]
	assert.Equal(t, steady1, m[2], "the rollback restores the first version")
	e.WaitStepState(t, s.flow.NS, steady, rb, "prod", "Verified", promoteTimeout)
	e.WaitBundlePhase(t, s.flow.NS, rb, "Verified", time.Minute)
	e.NoStep(t, s.flow.NS, steady, rb, "test", 5*time.Second)
	e.NoStep(t, s.flow.NS, steady, rb, "uat", 5*time.Second)
	assert.Contains(t, s.envFile(t, s.flow, steady, "prod"), fmt.Sprintf("sha256:%064x", 1), "prod runs the first version again")
	for _, env := range []string{"test", "uat"} {
		assert.Contains(t, s.envFile(t, s.flow, steady, env), fmt.Sprintf("sha256:%064x", 2), "%s keeps the second version", env)
	}

	// ── Receipts, enabled while versions are in flight ──────────────────
	pub := u0120ReceiptKey(t, e)
	var during []string
	for _, p := range bakes {
		during = append(during, bundle(s.flow, p, 2))
	}
	e.WaitStepState(t, s.flow.NS, bakes[0], during[0], "test", "Verified", promoteTimeout)
	enable := e.Shell(t, `helm upgrade kardinal-promoter "$KARDINAL_E2E_CHART" --namespace kardinal-system --reuse-values \
  --set receipts.enabled=true --set receipts.signing.mode=key --set receipts.signing.key.secretRef.name=receipt-key \
  --wait --timeout 3m
`)
	require.Zero(t, enable.Code, enable.Output)
	rel.WaitRolledOut(t, 3*time.Minute)
	for _, b := range during {
		e.WaitBundlePhase(t, s.flow.NS, b, "Verified", 2*promoteTimeout)
	}
	signed := bundle(s.flow, fresh, 1)
	e.WaitBundlePhase(t, s.flow.NS, signed, "Verified", promoteTimeout)
	for _, env := range []string{"test", "prod"} {
		rc := promotionReceipt(t, e, s.flow.NS, signed, env, v1alpha1.ReceiptStored)
		assert.Equal(t, v1alpha1.ReceiptSigningKey, rc.Status.SignedBy.Mode)
	}
	assert.Contains(t, e.MustKardinal(t, s.flow.NS, "verify-receipt", fresh, "prod", "--key", pub), "VERIFIED receipt")

	// The CLI and the UI API read the upgraded estate.
	res := c.Run("", "get", "pipelines", "-A")
	require.Equal(t, 0, res.Code, res.Output())
	for _, f := range s.fleets() {
		assert.Contains(t, res.Stdout, f.NS, "kardinal get pipelines -A lists %s", f.NS)
	}
	spec := framework.LoadOpenAPI(t)
	ui := mainUI(t, e)
	got := ui.Get(t, uiAPI+"/pipelines")
	require.Equal(t, 200, got.Status, got.String())
	assert.Empty(t, spec.Validate(spec.ResponseSchema(t, "GET", "/api/v1/ui/pipelines", 200), []byte(got.Body)))
	got = ui.Get(t, uiAPI+"/gates")
	require.Equal(t, 200, got.Status, got.String())
	assert.Empty(t, spec.Validate(spec.ResponseSchema(t, "GET", "/api/v1/ui/gates", 200), []byte(got.Body)))
}

// u0120AllKinds reads every stored object of every kardinal kind in the
// estate with this release's types (a new required field or a changed type
// fails the decode), and checks the admission policies admit a label on the
// objects users write: Pipelines, Bundles and gate templates.
func u0120AllKinds(t *testing.T, e *framework.Env, s *u0120Estate) {
	t.Helper()
	ctx := context.Background()
	old := &u0110Estate{e: e}
	old.auto, old.review, old.approve, old.fleet, old.ops = s.flow, s.approve, s.held, s.verify, s.frozen
	u0110AllKinds(t, e, old)
	lists := []client.ObjectList{
		&v1alpha1.AgentRunList{}, &v1alpha1.VerdictList{}, &v1alpha1.PromotionReceiptList{}, &v1alpha1.ChangeRequestList{},
	}
	for _, f := range s.fleets() {
		for _, l := range lists {
			require.NoError(t, e.Client.List(ctx, l, client.InNamespace(f.NS)), "list %T in %s with this release's types", l, f.NS)
		}
	}
	var ids v1alpha1.AgentIdentityList
	require.NoError(t, e.Client.List(ctx, &ids))
	var tmpls v1alpha1.StepTemplateList
	require.NoError(t, e.Client.List(ctx, &tmpls))
	var idx v1alpha1.AgentIndexList
	require.NoError(t, e.Client.List(ctx, &idx))
}
