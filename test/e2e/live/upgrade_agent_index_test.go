//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	authnv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/actor"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
)

// The upgrade-agents suite (hack/e2e/up.sh): the agent index changes
// format (#2071, docs/actors.md#the-index-format) while controllers of the
// earlier and of this release run side by side.

// Environment variables components/kardinal-prev.sh sets.
const (
	envPrevCLI   = "KARDINAL_E2E_PREV_CLI"
	envPrevImage = "KARDINAL_E2E_PREV_IMAGE"
	envPrevRef   = "KARDINAL_E2E_PREV_REF"
)

// indexWrite is a write the actor policy checks, made as who with stamp,
// and whether the policy must admit it. It is a server-side dry run:
// admission decides it, and nothing is stored.
type indexWrite struct {
	name  string
	who   client.Client
	obj   func() client.Object
	admit bool
}

// indexOutcome is one attempt of a write.
type indexOutcome struct {
	at       time.Time
	write    string
	admit    bool
	admitted bool
	// refusal is a policy's refusal; other any other error (the API
	// server's, the network's).
	refusal, other string
}

// indexSample is the agent index's format and the controller's Pods at one
// moment, the index read first: a Pod read after a compact index is no
// older than the Pods the controller saw when it wrote it.
type indexSample struct {
	at             time.Time
	compact, built bool
	// running is the image of each controller Pod not terminating.
	running []string
}

// indexRecorder makes every write and takes a sample in rounds until
// stopped.
type indexRecorder struct {
	mu       sync.Mutex
	outcomes []indexOutcome
	samples  []indexSample
}

func (r *indexRecorder) round(ctx context.Context, e *framework.Env, rel *framework.Release, writes []indexWrite) {
	for _, w := range writes {
		o := indexOutcome{at: time.Now(), write: w.name, admit: w.admit}
		err := w.who.Create(ctx, w.obj(), client.DryRunAll)
		if ctx.Err() != nil {
			return // stopped: the recorder's own cancellation, not an outcome
		}
		switch {
		case err == nil:
			o.admitted = true
		case strings.Contains(err.Error(), "ValidatingAdmissionPolicy"):
			o.refusal = err.Error()
		default:
			o.other = err.Error()
		}
		r.mu.Lock()
		r.outcomes = append(r.outcomes, o)
		r.mu.Unlock()
	}
	// A sample whose reads fail is not taken.
	s := indexSample{at: time.Now()}
	var idx v1alpha1.AgentIndex
	if err := e.Client.Get(ctx, types.NamespacedName{Name: agentIndexName}, &idx); err != nil {
		return
	}
	s.compact = actor.IsCompact(&idx.Status)
	_, s.built = actor.Readable(&idx.Status)
	var pods corev1.PodList
	if err := e.Client.List(ctx, &pods, client.InNamespace(rel.Namespace),
		client.MatchingLabelsSelector{Selector: rel.Selector()}); err != nil {
		return
	}
	for _, p := range pods.Items {
		if p.DeletionTimestamp != nil {
			continue
		}
		for _, c := range p.Spec.Containers {
			if c.Name == "controller" {
				s.running = append(s.running, c.Image)
			}
		}
	}
	r.mu.Lock()
	r.samples = append(r.samples, s)
	r.mu.Unlock()
}

// start runs rounds every 200ms until the returned stop is called.
func (r *indexRecorder) start(e *framework.Env, rel *framework.Release, writes []indexWrite) (stop func()) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ctx.Err() == nil {
			rctx, rcancel := context.WithTimeout(ctx, 10*time.Second)
			r.round(rctx, e, rel, writes)
			rcancel()
			select {
			case <-ctx.Done():
			case <-time.After(200 * time.Millisecond):
			}
		}
	}()
	return func() { cancel(); <-done }
}

// between returns the outcomes and samples recorded in [from, to).
func (r *indexRecorder) between(from, to time.Time) ([]indexOutcome, []indexSample) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var outs []indexOutcome
	for _, o := range r.outcomes {
		if !o.at.Before(from) && o.at.Before(to) {
			outs = append(outs, o)
		}
	}
	var ss []indexSample
	for _, s := range r.samples {
		if !s.at.Before(from) && s.at.Before(to) {
			ss = append(ss, s)
		}
	}
	return outs, ss
}

// checkPhase holds the outcomes of [from, to) to the actor policy: a write
// it must refuse is never admitted, whatever the format or the release; a
// write it must admit is refused only when refusable says so (a rollback's
// fail-closed window). Each write was tried at least five times, and at
// most two attempts failed for another reason than a policy's.
func checkPhase(t *testing.T, phase string, outs []indexOutcome, writes []indexWrite, refusable func(indexOutcome) bool) {
	t.Helper()
	tried := map[string]int{}
	others := 0
	for _, o := range outs {
		tried[o.write]++
		switch {
		case o.other != "":
			others++
			t.Logf("%s: %s at %s: not a policy's error: %s", phase, o.write, o.at.Format(time.RFC3339Nano), o.other)
		case !o.admit && o.admitted:
			t.Errorf("%s: %s was admitted at %s; the actor policy must refuse it", phase, o.write, o.at.Format(time.RFC3339Nano))
		case o.admit && !o.admitted && (refusable == nil || !refusable(o)):
			t.Errorf("%s: %s was refused at %s: %s", phase, o.write, o.at.Format(time.RFC3339Nano), o.refusal)
		}
	}
	for _, w := range writes {
		assert.GreaterOrEqual(t, tried[w.name], 5, "%s: %s tried throughout", phase, w.name)
	}
	assert.LessOrEqual(t, others, 2, "%s: attempts that failed for another reason than a policy's", phase)
	t.Logf("%s: %d writes", phase, len(outs))
}

// waitIndexFormat waits until the release's index is built in the compact
// format (compact) or the legacy one, and returns when it saw it.
func waitIndexFormat(t *testing.T, e *framework.Env, compact bool, timeout time.Duration) time.Time {
	t.Helper()
	var at time.Time
	framework.Eventually(t, timeout, fmt.Sprintf("the agent index built, compact %v", compact), func(ctx context.Context) (bool, string) {
		var idx v1alpha1.AgentIndex
		if err := e.Client.Get(ctx, types.NamespacedName{Name: agentIndexName}, &idx); err != nil {
			return false, err.Error()
		}
		_, built := actor.Readable(&idx.Status)
		at = time.Now()
		return built && actor.IsCompact(&idx.Status) == compact, fmt.Sprint(idx.Status.Conditions)
	})
	return at
}

// controllerImage is the image of the Deployment's controller container.
func controllerImage(t *testing.T, rel *framework.Release) string {
	t.Helper()
	for _, c := range rel.Deployment(t).Spec.Template.Spec.Containers {
		if c.Name == "controller" {
			return c.Image
		}
	}
	t.Fatal("the controller Deployment has no controller container")
	return ""
}

// helmRevision is the release's current helm revision.
func helmRevision(t *testing.T, e *framework.Env) int {
	t.Helper()
	out, err := e.Helm(t, "history", upgradeRelease, "-n", framework.ControllerNamespace, "--max", "1", "-o", "json")
	require.NoError(t, err, out)
	var h []struct {
		Revision int `json:"revision"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &h), out)
	require.NotEmpty(t, h, out)
	return h[len(h)-1].Revision
}

// TestUpgradeAgentIndex_RollingUpgrade upgrades a release of an earlier
// commit (components/kardinal-prev.sh: two replicas, the index in the
// object format) to this checkout, rolls it back, upgrades it again and
// rolls back the Deployment alone,
// while agents and people write without pause: a ServiceAccount agent, a
// User agent, a suspended agent, an unregistered ServiceAccount and a
// person, each with the stamp it must write and with a false one
// (server-side dry runs, which the actor policy decides).
//
//   - The upgrades: no write is refused and no false stamp admitted. The
//     controller keeps the object format, which the new policies read too,
//     while a replica of the earlier release runs, then switches to the
//     compact one; `kubectl wait --for=condition=CompactReady` (the docs)
//     returns.
//   - The rollback: no false stamp is admitted. The earlier policies refuse
//     the writes they check ("not ready yet", fail closed) until an earlier
//     controller has rebuilt the index in the object format, then admit
//     them again.
//   - The Deployment alone rolled back (kubectl rollout undo), this
//     release's policies kept: no write is refused. The first earlier
//     replica rebuilds the index in the object format, and this release's
//     leader, whose rollout has not finished, does not write it compact
//     again while an earlier replica runs (a replica older than v0.13.0
//     could not read it: it would never become ready, and the rollout would
//     stall).
//   - The CLIs: the earlier release's CLI (v0.13 reads the compact format,
//     #2384) stamps the agent before the upgrade and against the compact
//     index after it (N-1 CLI skew); this release's CLI too.
//
// Covers ACTOR-INDEX-03.
func TestUpgradeAgentIndex_RollingUpgrade(t *testing.T) {
	e := framework.New(t)
	ctx := context.Background()
	prevCLI, prevImage := os.Getenv(envPrevCLI), os.Getenv(envPrevImage)
	if prevCLI == "" || prevImage == "" {
		t.Fatalf("%s and %s are not set; run hack/e2e/up.sh upgrade-agents", envPrevCLI, envPrevImage)
	}
	newImage := os.Getenv(framework.EnvImage)
	require.NotEmpty(t, newImage, framework.EnvImage)
	require.NotEqual(t, prevImage, newImage)
	t.Logf("upgrading from %s (%s) to %s", os.Getenv(envPrevRef), prevImage, newImage)

	rel := e.ExistingRelease(t, upgradeRelease, framework.ControllerNamespace)
	rel.WaitRolledOut(t, 3*time.Minute)
	require.EqualValues(t, 2, *rel.Deployment(t).Spec.Replicas, "two replicas, so a rollout runs both releases at once")
	require.Equal(t, prevImage, controllerImage(t, rel), "the earlier release runs")
	waitIndexFormat(t, e, false, 2*time.Minute)

	// The registry, in the test's namespace: a ServiceAccount agent (its
	// CLI has the chart's promoter role), a User agent whose sponsor holds
	// "|" and ",", and a suspended agent.
	ns := e.Namespace(t)
	require.NoError(t, e.Client.Create(ctx, &v1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Name: pipelineName, Namespace: ns},
		Spec: v1alpha1.PipelineSpec{Paused: true, Git: v1alpha1.PipelineGit{URL: "https://example.invalid/none.git"},
			Environments: []v1alpha1.EnvironmentSpec{{Name: "test"}}}}))
	saUser := "system:serviceaccount:" + ns + ":fixer"
	userAgent := "robot@" + ns + ".example.com"
	parkedUser := "system:serviceaccount:" + ns + ":parked"
	builder := "system:serviceaccount:" + ns + ":builder"
	register := func(name, sponsor string, subject v1alpha1.AgentSubject, suspended bool) {
		require.NoError(t, e.Client.Create(ctx, &v1alpha1.AgentIdentity{ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: v1alpha1.AgentIdentitySpec{Description: "e2e agent of " + t.Name(), Subjects: []v1alpha1.AgentSubject{subject},
				Sponsor: v1alpha1.AgentSponsor{User: sponsor}, Scope: v1alpha1.AgentScope{Namespaces: []string{ns}},
				Autonomy: v1alpha1.AutonomyAuto, Suspended: suspended}}))
		t.Cleanup(func() {
			var cur v1alpha1.AgentIdentity
			if err := e.Client.Get(context.Background(), types.NamespacedName{Name: name}, &cur); err != nil {
				return
			}
			cur.Spec.Suspended = true
			_ = e.Client.Update(context.Background(), &cur)
			_ = e.Client.Delete(context.Background(), &cur)
		})
	}
	fixer, robot, parked := ns+"-fixer", ns+"-robot", ns+"-parked"
	_, err := e.Kube.CoreV1().ServiceAccounts(ns).Create(ctx, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "fixer"}}, metav1.CreateOptions{})
	require.NoError(t, err)
	_, err = e.Kube.RbacV1().RoleBindings(ns).Create(ctx, &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "fixer-promoter", Namespace: ns},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: framework.ControllerDeployment + "-promoter"},
		Subjects:   []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: "fixer", Namespace: ns}},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	waitBoundNamed(t, e, ns, "fixer-promoter")
	register(fixer, "alice@example.com", v1alpha1.AgentSubject{Kind: "ServiceAccount", Namespace: ns, Name: "fixer"}, false)
	register(robot, "auth0|bob,eng", v1alpha1.AgentSubject{Kind: "User", Name: userAgent}, false)
	register(parked, "carol@example.com", v1alpha1.AgentSubject{Kind: "ServiceAccount", Namespace: ns, Name: "parked"}, true)
	waitIndexed(t, e, saUser, func(en v1alpha1.AgentIndexEntry) bool { return en.Agent == fixer && en.Blocked == "" })
	waitIndexed(t, e, userAgent, func(en v1alpha1.AgentIndexEntry) bool { return en.Agent == robot && en.Blocked == "" })
	waitIndexed(t, e, parkedUser, func(en v1alpha1.AgentIndexEntry) bool { return en.Agent == parked && en.Blocked != "" })

	writer := func(user string) client.Client {
		return impersonating(t, e, ns, user, nil, []string{"bundles", "approvals"}, "create")
	}
	sa, ua, pk, bot, alice := writer(saUser), writer(userAgent), writer(parkedUser), writer(builder), writer("alice")
	agentStamp := func(principal, agent, sponsor string) *v1alpha1.Actor {
		return &v1alpha1.Actor{Kind: v1alpha1.ActorAgent, Principal: principal, Agent: agent, Sponsor: sponsor}
	}
	human := func(p string) *v1alpha1.Actor { return &v1alpha1.Actor{Kind: v1alpha1.ActorHuman, Principal: p} }
	bundle := func(stamp *v1alpha1.Actor) func() client.Object {
		return func() client.Object { return actorBundle(ns, stamp) }
	}
	approval := func(user string, stamp *v1alpha1.Actor) func() client.Object {
		return func() client.Object {
			return &v1alpha1.Approval{ObjectMeta: metav1.ObjectMeta{GenerateName: "a-", Namespace: ns,
				Labels: map[string]string{"kardinal.io/bundle": "b", "kardinal.io/environment": "test"}},
				Spec: v1alpha1.ApprovalSpec{Bundle: "b", BundleUID: "00000000-0000-0000-0000-000000000000", Environment: "test",
					User: user, Decision: "approve", Actor: stamp}}
		}
	}
	writes := []indexWrite{
		{"a ServiceAccount agent's version, stamped agent", sa, bundle(agentStamp(saUser, fixer, "alice@example.com")), true},
		{"a ServiceAccount agent's version, stamped human", sa, bundle(human(saUser)), false},
		{"a User agent's version, stamped agent", ua, bundle(agentStamp(userAgent, robot, "auth0|bob,eng")), true},
		{"a User agent's version, stamped human", ua, bundle(human(userAgent)), false},
		{"a User agent's approval, stamped human", ua, approval(userAgent, human(userAgent)), false},
		{"a suspended agent's version, stamped agent", pk, bundle(agentStamp(parkedUser, parked, "carol@example.com")), false},
		{"an unregistered ServiceAccount's version, stamped bot", bot,
			bundle(&v1alpha1.Actor{Kind: v1alpha1.ActorBot, Principal: builder}), true},
		{"an unregistered ServiceAccount's version, stamped human", bot, bundle(human(builder)), false},
		{"a person's version, stamped human", alice, bundle(human("alice")), true},
		{"a person's approval, stamped human", alice, approval("alice", human("alice")), true},
		{"a person's version, stamped as the agent", alice, bundle(agentStamp("alice", fixer, "alice@example.com")), false},
	}

	// The CLIs as the ServiceAccount agent, with a token of it.
	tr, err := e.Kube.CoreV1().ServiceAccounts(ns).CreateToken(ctx, "fixer", &authnv1.TokenRequest{
		Spec: authnv1.TokenRequestSpec{ExpirationSeconds: ptr.To[int64](3600)}}, metav1.CreateOptions{})
	require.NoError(t, err)
	agentCLI := e.CLI(t)
	agentCLI.Kubeconfig = tokenKubeconfig(t, e, tr.Status.Token)
	shipped := 0
	ships := func(bin, what string) {
		t.Helper()
		shipped++
		res := agentCLI.Exec(framework.CLIOptions{Bin: bin},
			agentCLI.Args(ns, "create", "bundle", pipelineName, "--image", fmt.Sprintf("ghcr.io/e2e/app:1.0.%d", shipped))...)
		require.Zero(t, res.Code, "%s: %s", what, res.Output())
		var b v1alpha1.Bundle
		require.NoError(t, e.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: bundleCreatedName(t, res.Stdout)}, &b))
		require.NotNil(t, b.Spec.Actor, what)
		assert.Equal(t, v1alpha1.ActorAgent, b.Spec.Actor.Kind, "%s: stamped agent", what)
		assert.Equal(t, fixer, b.Spec.Actor.Agent, what)
	}
	ships(prevCLI, "the earlier CLI before the upgrade")
	ships("", "this CLI before the upgrade")

	// The phases, each from its start to the next one's (the last to the
	// end of the recording). They are checked when the test ends, however
	// it ends: a phase that stalls (a rollout that never finishes) still
	// has its writes and samples judged.
	type phase struct {
		name string
		from time.Time
		// compact is the format the index must end the phase in;
		// upgrade: it must also start in the object format.
		compact, upgrade bool
		// refusable judges refusals of writes the policy must admit
		// (nil: none may be).
		refusable func(indexOutcome) bool
	}
	var phases []phase
	// legacyAt is when the rollback's index is in the object format again;
	// windowed counts the writes refused until then.
	var legacyAt time.Time
	windowed := 0
	begin := func(p phase) time.Time {
		p.from = time.Now()
		phases = append(phases, p)
		return p.from
	}
	rec := &indexRecorder{}
	stop := rec.start(e, rel, writes)
	defer func() {
		stop()
		end := time.Now()
		for i, p := range phases {
			to := end
			if i+1 < len(phases) {
				to = phases[i+1].from
			}
			outs, samples := rec.between(p.from, to)
			checkPhase(t, p.name, outs, writes, p.refusable)
			if p.name == "the rollback" {
				t.Logf("the rollback: %d writes refused until the earlier controllers rebuilt the index", windowed)
			}
			if p.name == "before the upgrade" {
				continue
			}
			checkSamples(t, p.name, samples, prevImage, newImage, p.compact, p.upgrade)
		}
	}()
	begin(phase{name: "before the upgrade"})
	time.Sleep(5 * time.Second)

	// ── The upgrade, as docs/upgrade.md says: the CRDs, then the chart ──
	upgrade := `helm show crds "$KARDINAL_E2E_CHART" | kubectl apply --server-side --force-conflicts -f -
helm upgrade kardinal-promoter "$KARDINAL_E2E_CHART" --namespace kardinal-system --reset-then-reuse-values \
  --set image.repository="${KARDINAL_E2E_IMAGE%:*}" --set image.tag="${KARDINAL_E2E_IMAGE##*:}" --set image.pullPolicy=Never \
  --set render.image.repository="${KARDINAL_E2E_RENDER_IMAGE%:*}" --set render.image.tag="${KARDINAL_E2E_RENDER_IMAGE##*:}" \
  --set render.image.pullPolicy=Never
`
	fromRevision := helmRevision(t, e)
	upgrading := begin(phase{name: "the upgrade", compact: true, upgrade: true})
	e.MustShell(t, upgrade)
	rel.WaitRolledOut(t, 5*time.Minute)
	require.Equal(t, newImage, controllerImage(t, rel), "the upgrade runs this checkout")
	compactAt := waitIndexFormat(t, e, true, 2*time.Minute)
	e.MustShell(t, `kubectl wait agentindex kardinal-promoter --for=condition=CompactReady --timeout=60s`)
	t.Logf("the upgrade: the index compact %s after the helm upgrade began", compactAt.Sub(upgrading).Round(time.Second))
	ships("", "this CLI after the upgrade")
	ships(prevCLI, "the earlier CLI after the upgrade, against the compact index")
	time.Sleep(10 * time.Second)

	// ── The rollback: it fails closed. A write the earlier policies must
	// admit is refused only as "not ready yet", and only until the index is
	// in the object format again and the API server's policies have read it.
	rolling := begin(phase{name: "the rollback", refusable: func(o indexOutcome) bool {
		ok := !legacyAt.IsZero() && o.at.Before(legacyAt.Add(5*time.Second)) &&
			(strings.Contains(o.refusal, "agent index is not ready yet") ||
				strings.Contains(o.refusal, "compact format of a newer release"))
		if ok {
			windowed++
		}
		return ok
	}})
	e.MustShell(t, fmt.Sprintf("helm rollback kardinal-promoter %d --namespace kardinal-system --wait --timeout 5m", fromRevision))
	rel.WaitRolledOut(t, 5*time.Minute)
	require.Equal(t, prevImage, controllerImage(t, rel), "the rollback runs the earlier release")
	legacyAt = waitIndexFormat(t, e, false, 2*time.Minute)
	t.Logf("the rollback: the index in the object format %s after the helm rollback began", legacyAt.Sub(rolling).Round(time.Second))
	time.Sleep(10 * time.Second)

	// ── The second upgrade ──────────────────────────────────────────────
	again := begin(phase{name: "the second upgrade", compact: true, upgrade: true})
	e.MustShell(t, upgrade)
	rel.WaitRolledOut(t, 5*time.Minute)
	require.Equal(t, newImage, controllerImage(t, rel))
	recompactAt := waitIndexFormat(t, e, true, 2*time.Minute)
	t.Logf("the second upgrade: the index compact %s after the helm upgrade began", recompactAt.Sub(again).Round(time.Second))
	time.Sleep(10 * time.Second)

	// ── The Deployment alone rolled back ────────────────────────────────
	// kubectl rollout undo: the chart's policies stay this release's, and
	// the Deployment rolls back to its previous Pod template (the earlier
	// release's, from the helm rollback) one replica at a time. The first
	// earlier replica rebuilds the index in the object format at its start,
	// and this release's leader must not write it compact again while that
	// replica runs (one older than v0.13.0 would read the index as not
	// ready and never become ready, and the rollout would stall).
	imageBack := begin(phase{name: "the Deployment rollback"})
	e.MustShell(t, `kubectl -n kardinal-system rollout undo deploy/kardinal-promoter`)
	rel.WaitRolledOut(t, 5*time.Minute)
	require.Equal(t, prevImage, controllerImage(t, rel))
	imageLegacyAt := waitIndexFormat(t, e, false, 2*time.Minute)
	t.Logf("the Deployment rollback: the index in the object format %s after it began", imageLegacyAt.Sub(imageBack).Round(time.Second))
	time.Sleep(10 * time.Second)
}

// checkSamples holds a phase's samples to the index's format: always
// built, never written compact (from the object format) while a replica of
// the earlier release (prevImage) runs, in the object format at the start
// of an upgrade and in the compact one (compact) or not at the end, and
// both releases' replicas seen side by side.
func checkSamples(t *testing.T, phase string, samples []indexSample, prevImage, newImage string, compact, upgrade bool) {
	t.Helper()
	if !assert.NotEmpty(t, samples, phase) {
		return
	}
	if upgrade {
		assert.False(t, samples[0].compact, "%s: the index starts in the object format", phase)
	}
	assert.Equal(t, compact, samples[len(samples)-1].compact, "%s: the index's format at the end", phase)
	both, wasCompact := 0, samples[0].compact
	for _, s := range samples {
		if !s.built {
			t.Errorf("%s: the index was not built at %s", phase, s.at.Format(time.RFC3339Nano))
			continue
		}
		prev, cur := false, false
		for _, img := range s.running {
			prev, cur = prev || img == prevImage, cur || img == newImage
		}
		if prev && cur {
			both++
		}
		if !wasCompact && s.compact && prev {
			t.Errorf("%s: the index was written compact by %s while a replica of the earlier release ran (%v)", phase,
				s.at.Format(time.RFC3339Nano), s.running)
		}
		wasCompact = s.compact
	}
	assert.Positive(t, both, "%s: both releases ran side by side", phase)
	t.Logf("%s: %d samples, %d with replicas of both releases running", phase, len(samples), both)
}
