//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	authzv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/invariants"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/scale"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/soak"
)

// The day's invariants (section 3 of #2403's design). Each looks at what
// changed since the pass before (since); the steps and receipts checks look
// soakLag further back, so a promotion is judged once its receipt and
// AuditEvents had time to be written. The last pass, after the estate
// settled, looks over the whole day.

// soakLag is how long after a step turned Verified its receipt must be
// Stored and its AuditEvents written: a leader kill or a rollout delays
// both by about a minute.
const soakLag = 4 * time.Minute

// signedReceipt is a receipt's signature (a digest of status.bundle) and
// its OCI referrers when first seen Stored.
type signedReceipt struct{ sig, refs string }

// heapSample is one reading of a controller Pod's memory.
type heapSample struct {
	Pod    string    `json:"pod"`
	At     time.Time `json:"at"`
	Leader bool      `json:"leader"`
	// HeapMiB is the Go heap in use just after a collection (the leader
	// only: scale.PodHeap takes up to three minutes).
	HeapMiB float64 `json:"heapMiB,omitempty"`
	RSSMiB  float64 `json:"rssMiB"`
}

// soakObjectErr is a violation with the object that broke it, which the
// dump writes out.
type soakObjectErr struct {
	msg string
	obj interface{}
}

func (e *soakObjectErr) Error() string { return e.msg }

// objectErr is a violation naming obj.
func objectErr(obj interface{}, format string, args ...interface{}) error {
	return &soakObjectErr{msg: fmt.Sprintf(format, args...), obj: obj}
}

// addChecks registers the invariants with the checker.
func (s *soakDay) addChecks() {
	c := s.check
	c.Add("tenant-isolation", s.checkIsolation)
	c.Add("receipts", s.checkReceipts)
	c.Add("verdicts", s.checkVerdicts)
	c.Add("audit-trail", s.checkAudit)
	c.Add("reconcile-errors", s.checkReconcileErrors)
	c.Add("controller-health", s.checkController)
	c.Add("progress", s.checkProgress)
	c.Add("load", s.checkLoad)
	c.Add("memory", s.checkMemory)
	c.Add("in-flight-finished", s.checkExpectations)
}

// bounds is the window of Verified steps a pass judges: since and now, both
// soakLag back, but up to now on the last pass.
func (s *soakDay) bounds(since time.Time) (lo, hi time.Time) {
	if s.final {
		return since.Add(-soakLag), time.Now()
	}
	return since.Add(-soakLag), time.Now().Add(-soakLag)
}

// checkIsolation: nothing of one team shows in another's namespace or
// registry prefix, no gate is copied from a namespace that did not consent,
// a team's identities may not write in another team's namespace, and team
// c's token never pushed to team a's repo.
func (s *soakDay) checkIsolation(ctx context.Context, _ time.Time) error {
	for _, tn := range s.tenants() {
		own := s.reg.Repository(tn.prefix)
		var rcs v1alpha1.PromotionReceiptList
		if err := s.e.Client.List(ctx, &rcs, client.InNamespace(tn.ns)); err != nil {
			return err
		}
		for i := range rcs.Items {
			rc := &rcs.Items[i]
			if p := rc.Labels["kardinal.io/pipeline"]; p != "" && !tn.owns(p) {
				return objectErr(rc, "receipt %s/%s is of Pipeline %s, not team %s's", rc.Namespace, rc.Name, p, tn.name)
			}
			for _, st := range rc.Status.Stored {
				if st.Sink == v1alpha1.ReceiptSinkOCI && (!strings.HasPrefix(st.Subject, own) || !strings.HasPrefix(st.Ref, own)) {
					return objectErr(rc, "receipt %s/%s of team %s has a referrer outside the team's prefix %s: %s for %s",
						rc.Namespace, rc.Name, tn.name, own, st.Ref, st.Subject)
				}
			}
		}
		var evs v1alpha1.AuditEventList
		if err := s.e.Client.List(ctx, &evs, client.InNamespace(tn.ns)); err != nil {
			return err
		}
		for i := range evs.Items {
			ev := &evs.Items[i]
			if p := ev.Spec.PipelineName; p != "" && !tn.owns(p) {
				return objectErr(ev, "AuditEvent %s/%s names Pipeline %s, not team %s's", ev.Namespace, ev.Name, p, tn.name)
			}
		}
		var vs v1alpha1.VerdictList
		if err := s.e.Client.List(ctx, &vs, client.InNamespace(tn.ns)); err != nil {
			return err
		}
		for i := range vs.Items {
			if v := &vs.Items[i]; !tn.owns(v.Spec.Pipeline) {
				return objectErr(v, "Verdict %s/%s is for Pipeline %s, not team %s's", v.Namespace, v.Name, v.Spec.Pipeline, tn.name)
			}
		}
		var crs v1alpha1.ChangeRequestList
		if err := s.e.Client.List(ctx, &crs, client.InNamespace(tn.ns)); err != nil {
			return err
		}
		for i := range crs.Items {
			cr := &crs.Items[i]
			if p := cr.Labels["kardinal.io/pipeline"]; p != "" && !tn.owns(p) {
				return objectErr(cr, "change request %s/%s is of Pipeline %s, not team %s's", cr.Namespace, cr.Name, p, tn.name)
			}
		}
	}

	// No gate of the namespace that had not consented, before it did.
	s.mu.Lock()
	consentAt := s.consentAt
	s.mu.Unlock()
	var gates v1alpha1.PolicyGateList
	if err := s.e.Client.List(ctx, &gates, client.InNamespace(s.b.ns)); err != nil {
		return err
	}
	for i := range gates.Items {
		g := &gates.Items[i]
		if strings.Contains(g.Name, "-"+s.private+"-prod--") && (consentAt.IsZero() || g.CreationTimestamp.Time.Before(consentAt)) {
			return objectErr(g, "gate %s/%s was copied from %s, which had not consented", g.Namespace, g.Name, s.private)
		}
	}

	// A team's identities may not write in another team's namespace.
	shipper := "system:serviceaccount:" + s.a.ns + ":shipper"
	saGroups := []string{"system:serviceaccounts", "system:serviceaccounts:" + s.a.ns, "system:authenticated"}
	for _, p := range []struct {
		user     string
		groups   []string
		ns, verb string
		resource string
	}{
		{shipper, saGroups, s.b.ns, "create", "bundles"},
		{shipper, saGroups, s.c.ns, "create", "bundles"},
		{soakApprover, nil, s.b.ns, "create", "approvals"},
		{soakMallory, nil, s.b.ns, "create", "verdicts"},
		{soakMallory, nil, s.c.ns, "create", "verdicts"},
	} {
		r, err := s.e.Kube.AuthorizationV1().SubjectAccessReviews().Create(ctx, &authzv1.SubjectAccessReview{Spec: authzv1.SubjectAccessReviewSpec{
			User: p.user, Groups: p.groups, ResourceAttributes: &authzv1.ResourceAttributes{
				Namespace: p.ns, Verb: p.verb, Group: v1alpha1.GroupVersion.Group, Resource: p.resource}}}, metav1.CreateOptions{})
		if err != nil {
			return err
		}
		if r.Status.Allowed {
			return objectErr(r, "%s may %s %s in %s, another team's namespace", p.user, p.verb, p.resource, p.ns)
		}
	}

	// Team c's token never pushed to team a's repo.
	ps, ok, err := s.e.Step(ctx, s.intruder, "steal", s.intruderBundle, "test")
	if err != nil {
		return err
	}
	if ok && (ps.Status.State == "Verified" || ps.Status.Outputs["pushedSHA"] != "" || ps.Status.Outputs["commitSHA"] != "") {
		return objectErr(ps, "the intruder's step %s pushed to team a's repo with team c's token: %s %v", ps.Name, ps.Status.State, ps.Status.Outputs)
	}
	return nil
}

// checkReceipts: every Verified step in the window names its receipt, and
// that receipt is Stored, with a referrer under its team's prefix when it is
// of the team's registry image; no receipt's signature or referrers change
// once Stored (signed once, #2313); a sample verifies with kardinal
// verify-receipt from the cluster and from the registry.
func (s *soakDay) checkReceipts(ctx context.Context, since time.Time) error {
	lo, hi := s.bounds(since)
	var sample []func(ctx context.Context) error
	for _, tn := range s.tenants() {
		var rcs v1alpha1.PromotionReceiptList
		if err := s.e.Client.List(ctx, &rcs, client.InNamespace(tn.ns)); err != nil {
			return err
		}
		byName := map[string]*v1alpha1.PromotionReceipt{}
		for i := range rcs.Items {
			rc := &rcs.Items[i]
			byName[rc.Name] = rc
			if rc.Status.Phase != v1alpha1.ReceiptStored || rc.Status.Bundle == "" {
				continue
			}
			sum := sha256.Sum256([]byte(rc.Status.Bundle))
			var refs []string
			for _, st := range rc.Status.Stored {
				if st.Sink == v1alpha1.ReceiptSinkOCI {
					refs = append(refs, st.Ref)
				}
			}
			sort.Strings(refs)
			now := signedReceipt{sig: hex.EncodeToString(sum[:]), refs: strings.Join(refs, ",")}
			key := rc.Namespace + "/" + rc.Name
			s.mu.Lock()
			was, seen := s.signed[key]
			if !seen {
				s.signed[key] = now
			}
			s.mu.Unlock()
			switch {
			case seen && was.sig != now.sig:
				return objectErr(rc, "receipt %s was signed again: its signed bundle changed once Stored", key)
			case seen && was.refs != now.refs:
				return objectErr(rc, "receipt %s's referrers changed once Stored: %s, then %s", key, was.refs, now.refs)
			}
		}
		var bundles v1alpha1.BundleList
		if err := s.e.Client.List(ctx, &bundles, client.InNamespace(tn.ns)); err != nil {
			return err
		}
		live := map[string]bool{}
		for _, b := range bundles.Items {
			live[b.Name] = true
		}
		var steps v1alpha1.PromotionStepList
		if err := s.e.Client.List(ctx, &steps, client.InNamespace(tn.ns)); err != nil {
			return err
		}
		for i := range steps.Items {
			ps := &steps.Items[i]
			at := stepVerifiedAt(ps)
			if at.IsZero() || at.Before(lo) || !at.Before(hi) {
				continue
			}
			if ps.Status.Receipt == "" {
				return objectErr(ps, "step %s/%s Verified at %s names no receipt", ps.Namespace, ps.Name, at.UTC().Format(time.RFC3339))
			}
			rc := byName[ps.Status.Receipt]
			switch {
			case rc == nil:
				return objectErr(ps, "step %s/%s names receipt %s, which does not exist", ps.Namespace, ps.Name, ps.Status.Receipt)
			case rc.Status.Phase != v1alpha1.ReceiptStored:
				return objectErr(rc, "receipt %s/%s of step %s is %s %s after the step was Verified", rc.Namespace, rc.Name, ps.Name,
					rc.Status.Phase, time.Since(at).Round(time.Second))
			}
			oci := ""
			for _, st := range rc.Status.Stored {
				if st.Sink == v1alpha1.ReceiptSinkOCI {
					oci = st.Subject
				}
			}
			if ps.Spec.PipelineName == tn.evidence && !s.isProbe(ps.Spec.BundleName) && oci == "" {
				return objectErr(rc, "receipt %s/%s of team %s's own registry image has no referrer: %v", rc.Namespace, rc.Name, tn.name, rc.Status.Conditions)
			}
			if !live[ps.Spec.BundleName] || s.rng.Intn(20) != 0 {
				continue
			}
			tn, ps, rc, oci := tn, ps, rc, oci
			sample = append(sample, func(ctx context.Context) error {
				return s.verifyReceipt(ctx, tn, ps, rc, oci)
			})
		}
	}
	for i, v := range sample {
		if i >= 3 {
			break
		}
		if err := v(ctx); err != nil {
			return err
		}
	}
	return nil
}

// verifyReceipt verifies rc with kardinal verify-receipt from the cluster
// and, when it has a referrer, from the registry.
func (s *soakDay) verifyReceipt(ctx context.Context, tn *soakTenant, ps *v1alpha1.PromotionStep, rc *v1alpha1.PromotionReceipt, subject string) error {
	const passed = "every check below that was made passed"
	out, err := s.cliRun(ctx, "", tn.ns, "verify-receipt", ps.Spec.PipelineName, ps.Spec.Environment,
		"--bundle", ps.Spec.BundleName, "--key", s.pub)
	if err != nil || !strings.Contains(out, passed) {
		return objectErr(rc, "kardinal verify-receipt %s %s --bundle %s: %v\n%s", ps.Spec.PipelineName, ps.Spec.Environment, ps.Spec.BundleName, err, out)
	}
	s.count("receiptsVerified", 1)
	if subject == "" {
		return nil
	}
	_, digest, _ := strings.Cut(subject, "@")
	out, err = s.cliRun(ctx, "", tn.ns, "verify-receipt", "--image", tn.image+"@"+digest, "--pipeline", ps.Spec.PipelineName,
		"--env", ps.Spec.Environment, "--cluster", s.cluster, "--key", s.pub,
		"--repository", s.reg.HostRepository(tn.prefix+"app"), "--insecure-registry")
	if err != nil || !strings.Contains(out, passed) {
		return objectErr(rc, "kardinal verify-receipt --image %s@%s --pipeline %s --env %s: %v\n%s", tn.image, digest, ps.Spec.PipelineName, ps.Spec.Environment, err, out)
	}
	s.count("receiptsVerifiedFromRegistry", 1)
	return nil
}

// checkVerdicts: team a's verify gates count only the verifier agent's
// verdicts, and every agent run's Verdict is the run's own (named after it,
// written by its ServiceAccount); the only Verdict without a run is
// mallory's, which no gate counts.
func (s *soakDay) checkVerdicts(ctx context.Context, _ time.Time) error {
	a := s.a
	allowed := "agent " + a.verifier + " for " + soakVerifierSponsor
	var gates v1alpha1.PolicyGateList
	if err := s.e.Client.List(ctx, &gates, client.InNamespace(a.ns), client.MatchingLabels{"kardinal.io/gate-template": "explore-qa"}); err != nil {
		return err
	}
	counted := 0
	for i := range gates.Items {
		g := &gates.Items[i]
		for _, v := range g.Status.Verdicts {
			if v.Counted && !strings.HasPrefix(v.Verifier, allowed) {
				return objectErr(g, "gate %s/%s counts verdict %s of %s, not of the allowed agent %s", g.Namespace, g.Name, v.Name, v.Verifier, a.verifier)
			}
			if v.Counted {
				counted++
			}
		}
	}
	var vs v1alpha1.VerdictList
	if err := s.e.Client.List(ctx, &vs, client.InNamespace(a.ns)); err != nil {
		return err
	}
	for i := range vs.Items {
		v := &vs.Items[i]
		switch {
		case v.Spec.Run != nil && (v.Name != v.Spec.Run.Name || v.Spec.By != "system:serviceaccount:"+a.ns+":kr-"+v.Spec.Run.Name):
			return objectErr(v, "Verdict %s/%s of run %s was written by %s, not the run's ServiceAccount", v.Namespace, v.Name, v.Spec.Run.Name, v.Spec.By)
		case v.Spec.Run == nil && v.Spec.By != soakMallory:
			return objectErr(v, "Verdict %s/%s has no run and is by %s", v.Namespace, v.Name, v.Spec.By)
		}
	}
	s.note("verdictsCounted", counted)
	s.note("verdicts", len(vs.Items))
	return nil
}

// checkAudit: every step Verified in the window has its PromotionSucceeded
// AuditEvent, naming its actor.
func (s *soakDay) checkAudit(ctx context.Context, since time.Time) error {
	lo, hi := s.bounds(since)
	for _, tn := range s.tenants() {
		var evs v1alpha1.AuditEventList
		if err := s.e.Client.List(ctx, &evs, client.InNamespace(tn.ns)); err != nil {
			return err
		}
		succeeded := map[string]*v1alpha1.AuditEvent{}
		for i := range evs.Items {
			ev := &evs.Items[i]
			if ev.Spec.Action == "PromotionSucceeded" {
				succeeded[ev.Spec.BundleName+"/"+ev.Spec.Environment] = ev
			}
		}
		var steps v1alpha1.PromotionStepList
		if err := s.e.Client.List(ctx, &steps, client.InNamespace(tn.ns)); err != nil {
			return err
		}
		for i := range steps.Items {
			ps := &steps.Items[i]
			at := stepVerifiedAt(ps)
			if at.IsZero() || at.Before(lo) || !at.Before(hi) {
				continue
			}
			ev := succeeded[ps.Spec.BundleName+"/"+ps.Spec.Environment]
			switch {
			case ev == nil:
				return objectErr(ps, "step %s/%s Verified at %s has no PromotionSucceeded AuditEvent", ps.Namespace, ps.Name, at.UTC().Format(time.RFC3339))
			case ev.Spec.Actor == nil:
				return objectErr(ev, "AuditEvent %s/%s names no actor", ev.Namespace, ev.Name)
			}
		}
		s.note("auditEvents."+tn.name, len(evs.Items))
	}
	return nil
}

// soakErrorWindow is the reconcile-error window a pass judges.
const soakErrorWindow = 10 * time.Minute

// checkReconcileErrors: under 1% of the reconciles of the last ten minutes
// failed (Prometheus), unless the window holds an upgrade or a leader kill,
// whose errors the end of the day reports over the whole run instead.
func (s *soakDay) checkReconcileErrors(ctx context.Context, _ time.Time) error {
	if s.final {
		return nil // invariants.Check's metrics-reconcile-errors, over the day
	}
	end := time.Now()
	from := end.Add(-soakErrorWindow)
	if from.Before(s.start) {
		from = s.start
	}
	s.mu.Lock()
	for _, w := range s.windows {
		if (w.Name == "upgrade" || w.Name == "leader-killed") && w.From.Before(end) && w.To.After(from) {
			s.mu.Unlock()
			return nil
		}
	}
	s.mu.Unlock()
	sel := `namespace="` + framework.ControllerNamespace + `",job="` + framework.ControllerName + `"`
	w := fmt.Sprintf("%ds", int(end.Sub(from).Seconds()))
	sum := func(q string) (map[string]float64, error) {
		samples, err := s.e.PromQuery(ctx, q)
		if err != nil {
			return nil, err
		}
		out := map[string]float64{}
		for _, x := range samples {
			if v, err := strconv.ParseFloat(x.Value, 64); err == nil && !math.IsNaN(v) {
				out[x.Metric["controller"]] = v
			}
		}
		return out, nil
	}
	all, err := sum(`sum by (controller) (increase(controller_runtime_reconcile_total{` + sel + `}[` + w + `]))`)
	if err != nil {
		return nil // Prometheus unreachable: the day's check at the end still runs
	}
	bad, err := sum(`sum by (controller) (increase(controller_runtime_reconcile_errors_total{` + sel + `}[` + w + `]))`)
	if err != nil {
		return nil
	}
	var tot, errs float64
	var worst []string
	for k, v := range all {
		tot += v
		errs += bad[k]
		if bad[k] > 0 {
			worst = append(worst, fmt.Sprintf("%s %.0f/%.0f", k, bad[k], v))
		}
	}
	if tot < 200 {
		return nil
	}
	ratio := errs / tot
	s.mu.Lock()
	series, _ := s.notes["reconcileErrorRatios"].([]string)
	s.notes["reconcileErrorRatios"] = append(series, fmt.Sprintf("T+%s %.2f%% of %.0f", end.Sub(s.start).Round(time.Minute), 100*ratio, tot))
	s.mu.Unlock()
	if ratio > 0.01 {
		sort.Strings(worst)
		return fmt.Errorf("%.2f%% of %.0f reconciles failed from T+%s to T+%s (limit 1%%): %s", 100*ratio, tot,
			from.Sub(s.start).Round(time.Second), end.Sub(s.start).Round(time.Second), strings.Join(worst, ", "))
	}
	return nil
}

// checkController: no panic or data race in the controller's or kro's logs,
// and no controller container restarted.
func (s *soakDay) checkController(ctx context.Context, _ time.Time) error {
	sum := s.logs.Summary()
	if blocks := append(append(append([]invariants.Block{}, sum.Panics...), sum.Races...), sum.KroPanics...); len(blocks) > 0 {
		b := blocks[0]
		return objectErr(b, "%s in %s's log: %s", b.Kind, b.Pod, strings.Join(b.Lines, "\n"))
	}
	var pods corev1.PodList
	if err := s.e.Client.List(ctx, &pods, client.InNamespace(framework.ControllerNamespace),
		client.MatchingLabels{"app.kubernetes.io/name": framework.ControllerName}); err != nil {
		return err
	}
	for i := range pods.Items {
		for _, cs := range pods.Items[i].Status.ContainerStatuses {
			if cs.RestartCount > 0 {
				return objectErr(&pods.Items[i], "controller Pod %s's container %s restarted %d times (last: %+v)",
					pods.Items[i].Name, cs.Name, cs.RestartCount, cs.LastTerminationState)
			}
		}
	}
	return nil
}

// checkProgress: while the load runs, some step turned Verified since the
// pass before.
func (s *soakDay) checkProgress(ctx context.Context, since time.Time) error {
	if s.final || s.loadCtx().Err() != nil || s.check.Passes() == 0 {
		return nil // the first pass: the warm-up's versions are still at test
	}
	n := 0
	for _, tn := range s.tenants() {
		c, err := s.verifiedSince(ctx, tn.ns, since, func(string) bool { return true })
		if err != nil {
			return err
		}
		n += c
	}
	if n == 0 {
		return fmt.Errorf("no promotion finished since %s while the load ran", since.UTC().Format(time.RFC3339))
	}
	return nil
}

// checkLoad: every Bundle create succeeded (one retry each).
func (s *soakDay) checkLoad(context.Context, time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.loadErrors) > 0 {
		return fmt.Errorf("%d Bundle creates failed twice; first: %s", len(s.loadErrors), s.loadErrors[0])
	}
	return nil
}

// checkMemory: the leader's heap after its collections stays under twice
// its baseline (an early alarm); the end of the day judges it flat.
func (s *soakDay) checkMemory(context.Context, time.Time) error {
	if s.final {
		return s.memoryFlat()
	}
	for _, p := range s.leaderSeries() {
		if p.baseline > 0 && p.last > 2*p.baseline {
			return fmt.Errorf("controller leader %s's heap %.1f MiB is over twice its baseline %.1f MiB", p.pod, p.last, p.baseline)
		}
	}
	return nil
}

// checkExpectations runs the checks episodes left whose deadline passed.
func (s *soakDay) checkExpectations(ctx context.Context, _ time.Time) error {
	s.mu.Lock()
	var due, later []soakExpectation
	for _, x := range s.expect {
		if s.final || time.Now().After(x.deadline) {
			due = append(due, x)
		} else {
			later = append(later, x)
		}
	}
	s.expect = later
	s.mu.Unlock()
	for _, x := range due {
		if err := x.check(ctx); err != nil {
			return fmt.Errorf("%s: %w", x.name, err)
		}
	}
	return nil
}

// heapSampler reads every controller Pod's RSS and the leader's heap after
// a collection every twentieth of the day (at least every minute).
func (s *soakDay) heapSampler(ctx context.Context) {
	sample := func(ctx context.Context) {
		var pods corev1.PodList
		if err := s.e.Client.List(ctx, &pods, client.InNamespace(framework.ControllerNamespace),
			client.MatchingLabels{"app.kubernetes.io/name": framework.ControllerName}); err != nil {
			return
		}
		leader, _ := scale.Leader(ctx, s.e)
		for _, p := range pods.Items {
			if p.DeletionTimestamp != nil || p.Status.Phase != corev1.PodRunning {
				continue
			}
			m, err := scale.PodMetrics(ctx, s.e, p.Name)
			if err != nil {
				continue
			}
			h := heapSample{Pod: p.Name, At: time.Now(), Leader: p.Name == leader, RSSMiB: m.Sum("process_resident_memory_bytes", nil) / (1 << 20)}
			if h.Leader {
				if v, err := scale.PodHeap(ctx, s.e, p.Name); err == nil {
					h.HeapMiB = v
				}
			}
			s.mu.Lock()
			s.heaps = append(s.heaps, h)
			s.mu.Unlock()
		}
	}
	sample(ctx)
	s.every(ctx, s.span(1.0/20, time.Minute), sample)
}

// leaderTerm is one Pod's time as the leader, as the sampler saw it.
type leaderTerm struct {
	pod                  string
	baseline, last       float64
	baselineAt, lastAt   time.Time
	rssBaseline, rssLast float64
	samples              []heapSample
}

// leaderSeries splits the heap samples by leader Pod. Each Pod's baseline
// is its first sample from the warm-up's end and from a twelfth of the day
// after it became the leader (it builds its caches first).
func (s *soakDay) leaderSeries() []*leaderTerm {
	s.mu.Lock()
	defer s.mu.Unlock()
	warm := s.warmAt
	byPod := map[string]*leaderTerm{}
	var order []string
	first := map[string]time.Time{}
	for _, h := range s.heaps {
		if !h.Leader || h.HeapMiB == 0 {
			continue
		}
		if _, ok := first[h.Pod]; !ok {
			first[h.Pod] = h.At
		}
		if warm.IsZero() || h.At.Before(warm) || h.At.Before(first[h.Pod].Add(s.span(1.0/12, 2*time.Minute))) {
			continue
		}
		p := byPod[h.Pod]
		if p == nil {
			p = &leaderTerm{pod: h.Pod, baseline: h.HeapMiB, baselineAt: h.At, rssBaseline: h.RSSMiB}
			byPod[h.Pod] = p
			order = append(order, h.Pod)
		}
		p.last, p.lastAt, p.rssLast = h.HeapMiB, h.At, h.RSSMiB
		p.samples = append(p.samples, h)
	}
	out := make([]*leaderTerm, 0, len(order))
	for _, pod := range order {
		out = append(out, byPod[pod])
	}
	return out
}

// memoryFlat judges each leader term of at least a quarter of the day
// (and three samples): its heap ends at most 1.2 times its baseline (plus 8
// MiB, for a small heap) and its least-squares slope is under 1 MiB per ten
// minutes; RSS ends at most 1.2 times its baseline plus 64 MiB (the runtime
// returns memory to the OS lazily).
func (s *soakDay) memoryFlat() error {
	var judged []string
	for _, p := range s.leaderSeries() {
		span := p.lastAt.Sub(p.baselineAt)
		if len(p.samples) < 3 || span < s.d/4 {
			s.note("memory."+p.pod, fmt.Sprintf("%d samples over %s: too short to judge", len(p.samples), span.Round(time.Second)))
			continue
		}
		slope := heapSlope(p.samples) * 10 // MiB per ten minutes
		s.note("memory."+p.pod, map[string]interface{}{"baselineMiB": p.baseline, "endMiB": p.last, "slopeMiBPer10m": math.Round(slope*100) / 100,
			"rssBaselineMiB": p.rssBaseline, "rssEndMiB": p.rssLast, "minutes": int(span.Minutes())})
		judged = append(judged, p.pod)
		switch {
		case p.last > 1.2*p.baseline+8:
			return fmt.Errorf("controller leader %s's heap grew from %.1f to %.1f MiB over %s (limit 1.2x + 8 MiB)", p.pod, p.baseline, p.last, span.Round(time.Minute))
		case slope >= 1:
			return fmt.Errorf("controller leader %s's heap grows %.2f MiB every ten minutes over %s (limit 1)", p.pod, slope, span.Round(time.Minute))
		case p.rssLast > 1.2*p.rssBaseline+64:
			return fmt.Errorf("controller leader %s's RSS grew from %.0f to %.0f MiB over %s (limit 1.2x + 64 MiB)", p.pod, p.rssBaseline, p.rssLast, span.Round(time.Minute))
		}
	}
	s.note("memoryJudged", judged)
	return nil
}

// heapSlope is the least-squares slope of the samples' heap, MiB a minute.
func heapSlope(samples []heapSample) float64 {
	if len(samples) < 2 {
		return 0
	}
	t0 := samples[0].At
	var n, sx, sy, sxx, sxy float64
	for _, h := range samples {
		x := h.At.Sub(t0).Minutes()
		n++
		sx += x
		sy += h.HeapMiB
		sxx += x * x
		sxy += x * h.HeapMiB
	}
	d := n*sxx - sx*sx
	if d == 0 {
		return 0
	}
	return (n*sxy - sx*sy) / d
}

// dump records the estate at the first violation: the violation and the
// object that broke it, each namespace's objects (framework.Env.Diagnose),
// every controller Pod's /metrics, and the controller logs (already
// streamed into the run's directory by the log collector).
func (s *soakDay) dump(ctx context.Context, v soak.Violation) {
	dir := invariants.Dir(s.t)
	s.t.Logf("soak: %v; dumping the estate into %s", v, dir)
	rec := map[string]interface{}{"invariant": v.Invariant, "at": v.At.UTC().Format(time.RFC3339),
		"atSoak": v.At.Sub(s.start).Round(time.Second).String(), "error": v.Err.Error()}
	var oe *soakObjectErr
	if errors.As(v.Err, &oe) {
		rec["object"] = oe.obj
	}
	if b, err := json.MarshalIndent(rec, "", "  "); err == nil {
		_ = os.WriteFile(filepath.Join(dir, "soak-violation.json"), b, 0o644)
	}
	for _, ns := range []string{s.a.ns, s.b.ns, s.c.ns, s.org, s.private, s.intruder} {
		s.e.Diagnose(s.t, ns)
	}
	var pods corev1.PodList
	if err := s.e.Client.List(ctx, &pods, client.InNamespace(framework.ControllerNamespace),
		client.MatchingLabels{"app.kubernetes.io/name": framework.ControllerName}); err == nil {
		for _, p := range pods.Items {
			raw, err := scale.PodMetricsText(ctx, s.e, p.Name)
			if err != nil {
				raw = []byte(err.Error())
			}
			_ = os.WriteFile(filepath.Join(dir, "metrics-"+p.Name+".txt"), raw, 0o644)
		}
	}
	s.writeReport()
}

// writeReport writes the day's numbers, episodes and memory samples to
// soak-report.json in the run's directory.
func (s *soakDay) writeReport() {
	s.mu.Lock()
	rep := map[string]interface{}{"notes": s.notes, "windows": s.windows, "memory": s.heaps,
		"start": s.start.UTC().Format(time.RFC3339), "duration": s.d.String(), "rejected": s.rejected}
	b, err := json.MarshalIndent(rep, "", "  ")
	s.mu.Unlock()
	if err != nil {
		s.t.Logf("soak report: %v", err)
		return
	}
	path := filepath.Join(invariants.Dir(s.t), "soak-report.json")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		s.t.Logf("soak report: %v", err)
		return
	}
	s.t.Logf("soak report: %s", path)
}
