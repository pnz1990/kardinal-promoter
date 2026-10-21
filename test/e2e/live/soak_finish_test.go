//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/invariants"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/scale"
)

// soakSettle bounds how long the estate may take to settle once the load
// stops: the last versions wait for their change requests, approvals,
// reviews and verifiers, a minute or two each.
const soakSettle = 20 * time.Minute

// soakAllow are the error logs the day's episodes are expected to cause,
// each with its reason.
var soakAllow = []*regexp.Regexp{
	// The leader kill and the upgrade's rolling restart end a replica's
	// reconciles mid-call.
	regexp.MustCompile(`context canceled`),
}

// finish ends the day: the load stops, the estate settles with the bots
// still answering, and the checker's last pass looks over the whole day;
// then the scale invariants run over each team, each team's auditor exports
// an evidence pack, the registry's referrers are counted against the
// receipts, and the report is written.
func (s *soakDay) finish() {
	t, e := s.t, s.e
	s.mu.Lock()
	cancel := s.loadCancel
	s.mu.Unlock()
	cancel()
	s.loadWG.Wait()
	loadEnd := time.Now()
	s.note("loadMinutes", int(loadEnd.Sub(s.start).Minutes()))
	for _, tn := range s.tenants() {
		tn.fleet.WaitSettled(t, soakSettle)
	}
	s.note("settleSeconds", int(time.Since(loadEnd).Seconds()))
	s.botsCancel()
	s.botsWG.Wait()

	s.final = true
	ctx, done := context.WithTimeout(context.Background(), 15*time.Minute)
	defer done()
	if v := s.check.Final(ctx); v != nil {
		t.Fatalf("the soak's last pass, over the whole day (diagnostics in %s): %v", invariants.Dir(t), v)
	}
	s.note("checkerPasses", s.check.Passes())

	for i, tn := range s.tenants() {
		o := invariants.Options{Namespace: tn.ns, Targets: tn.fleet.Targets(), Image: scale.ImageRepo, SeedTag: scale.SeedTag,
			Start: s.start, MaxReconcileErrorRatio: 0.01, WarmAt: s.warmAt, Allow: soakAllow}
		if i == 0 {
			// The controller's logs and metrics are the cluster's: checked
			// once, with team a.
			o.Logs, o.Metrics, o.Extra = s.logs, true, s.notes
		}
		invariants.Check(t, e, o)
	}
	s.evidencePacks(ctx)
	s.referrers(ctx)
	s.intruderRefused(ctx)
	s.mu.Lock()
	s.notes["ticketsRejected"] = s.rejected
	s.mu.Unlock()
	s.writeReport()
}

// clusterScopedPackFiles are the pack's files of cluster-scoped kinds, which
// every team's pack holds whole: the agent registry and the freezes (a
// namespace's freeze is named after it).
var clusterScopedPackFiles = []string{"agentidentities.ndjson:", "changewindows.ndjson:"}

// evidencePacks: each team's auditor (the chart's auditor roles, in the
// team's namespace) exports an evidence pack of the day: sha256sum -c
// passes, nothing is forbidden or missing, and no record of a namespaced
// kind names another team's namespace.
func (s *soakDay) evidencePacks(ctx context.Context) {
	t, e := s.t, s.e
	since := fmt.Sprintf("%dm", int(time.Since(s.start).Minutes())+30)
	c := e.CLI(t)
	for _, tn := range s.tenants() {
		auditor := "auditor@team-" + tn.name + ".example.com"
		pack := filepath.Join(t.TempDir(), "pack-"+tn.name+".tar.gz")
		r := c.Exec(framework.CLIOptions{Kubeconfig: auditorKubeconfig(t, e, tn.ns, auditor)},
			c.Args(tn.ns, "audit", "evidence-pack", "--since", since, "-o", pack)...)
		if !assert.Zero(t, r.Code, "team %s's evidence pack: %s", tn.name, r.Output()) {
			continue
		}
		dir, m := packOf(t, pack)
		sums := exec.Command("sha256sum", "-c", "--quiet", "SHA256SUMS")
		sums.Dir = dir
		out, err := sums.CombinedOutput()
		assert.NoError(t, err, "team %s's pack: sha256sum -c:\n%s", tn.name, out)
		coverage, _ := m["coverage"].(map[string]interface{})
		assert.Empty(t, coverage["forbidden"], "team %s's auditor reads everything its pack holds", tn.name)
		assert.Empty(t, coverage["missing"], "team %s's pack", tn.name)
		var others []string
		for _, o := range s.tenants() {
			if o != tn {
				others = append(others, o.ns)
			}
		}
		var leaks []string
		for _, hit := range packHolds(t, dir, others...) {
			if !slices.ContainsFunc(clusterScopedPackFiles, func(f string) bool { return strings.HasPrefix(hit, f) }) {
				leaks = append(leaks, hit)
			}
		}
		assert.Empty(t, leaks, "team %s's pack names another team's namespace", tn.name)
		files := packFiles(m)
		s.note("pack."+tn.name, files)
		assert.Positive(t, files["receipts.ndjson"], "team %s's pack holds its receipts", tn.name)
		assert.Positive(t, files["auditevents.ndjson"], "team %s's pack holds its AuditEvents", tn.name)
	}
}

// referrers: in the registry, each team image's receipt referrers name its
// own team's namespace only, and there is exactly one per receipt that
// records it (none signed or pushed twice).
func (s *soakDay) referrers(ctx context.Context) {
	t := s.t
	recorded := map[string]map[string]int{} // subject -> resource -> receipts
	for _, tn := range s.tenants() {
		var rcs v1alpha1.PromotionReceiptList
		require.NoError(t, s.e.Client.List(ctx, &rcs, client.InNamespace(tn.ns)))
		for _, rc := range rcs.Items {
			for _, st := range rc.Status.Stored {
				if st.Sink != v1alpha1.ReceiptSinkOCI {
					continue
				}
				res := fmt.Sprintf("kardinal://%s/namespaces/%s/pipelines/%s/environments/%s", s.cluster, rc.Namespace,
					rc.Labels["kardinal.io/pipeline"], rc.Labels["kardinal.io/environment"])
				if recorded[st.Subject] == nil {
					recorded[st.Subject] = map[string]int{}
				}
				recorded[st.Subject][res]++
			}
		}
	}
	total := 0
	for _, tn := range []*soakTenant{s.a, s.b} {
		for _, d := range tn.digests {
			res, entries := referrerResources(t, tn.prefix+"app", d)
			got := map[string]int{}
			for _, r := range res {
				got[r]++
				assert.Contains(t, r, "/namespaces/"+tn.ns+"/", "a referrer of team %s's image %s names another namespace", tn.name, d)
			}
			assert.Equal(t, len(res), entries, "every referrer of %s@%s is a receipt", tn.image, d)
			assert.Equal(t, recorded[tn.image+"@"+d], nilIfEmpty(got), "one referrer per receipt of %s@%s, by resource", tn.image, d)
			total += len(res)
		}
	}
	s.note("receiptReferrers", total)
}

// nilIfEmpty is m, or nil when it is empty (a map no receipt filled).
func nilIfEmpty(m map[string]int) map[string]int {
	if len(m) == 0 {
		return nil
	}
	return m
}

// intruderRefused: the intruder's version, of team a's repo with team c's
// token, tried and never pushed.
func (s *soakDay) intruderRefused(ctx context.Context) {
	ps, ok, err := s.e.Step(ctx, s.intruder, "steal", s.intruderBundle, "test")
	require.NoError(s.t, err)
	if !assert.True(s.t, ok, "the intruder's version has a test step") {
		return
	}
	assert.NotEqual(s.t, "Verified", ps.Status.State)
	assert.Empty(s.t, ps.Status.Outputs["pushedSHA"], "nothing pushed with team c's token")
	assert.True(s.t, ps.Status.RetryCount+ps.Status.GitCredentialRetries > 0 || ps.Status.State == "Failed",
		"the intruder's step tried and was refused: %s %s", ps.Status.State, ps.Status.Message)
	s.note("intruder", ps.Status.State+": "+ps.Status.Message)
}
