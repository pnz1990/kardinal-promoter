//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	iv "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/imageverification"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/imageverification/signtest"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/scale"
)

// attestImages are the three images of each TestScale_Attestations version.
var attestImages = []string{scale.ImageRepo, scale.ImageRepo + "-sidecar", scale.ImageRepo + "-init"}

// histogramQuantile is the q quantile of histogram name{labels} between two
// scrapes: the upper bound of the bucket it falls in (+Inf as math.Inf).
func histogramQuantile(before, after framework.Metrics, name string, labels map[string]string, q float64) (float64, float64) {
	counts := func(m framework.Metrics) map[float64]float64 {
		out := map[float64]float64{}
		for _, s := range m {
			if s.Name != name+"_bucket" {
				continue
			}
			match := true
			for k, v := range labels {
				if s.Labels[k] != v {
					match = false
				}
			}
			if !match {
				continue
			}
			le, err := strconv.ParseFloat(s.Labels["le"], 64)
			if err != nil {
				le = math.Inf(1)
			}
			out[le] += s.Value
		}
		return out
	}
	b, a := counts(before), counts(after)
	var les []float64
	for le := range a {
		les = append(les, le)
	}
	sort.Float64s(les)
	if len(les) == 0 {
		return 0, 0
	}
	total := a[les[len(les)-1]] - b[les[len(les)-1]]
	for _, le := range les {
		if total > 0 && a[le]-b[le] >= q*total {
			return le, total
		}
	}
	return les[len(les)-1], total
}

// registryFollower collects the suite's registry's log lines from a start
// time while the test runs: every five seconds it reads each registry
// container's lines from the last second it has (timestamps on, so a line
// read twice is counted once). Read once at the end, the log once held none
// of the reads of a run whose versions all verified, most likely rotated
// away: the kubelet rotates a container's log at 10 MiB, the API serves only
// the current file, and this test's pushes and reads come close to that. A
// container that restarted is followed in its new run.
type registryFollower struct {
	stop chan struct{}
	done chan struct{}
	mu   sync.Mutex
	seen map[string]bool
	errs []string
}

func followRegistry(t *testing.T, e *framework.Env, since time.Time) *registryFollower {
	t.Helper()
	f := &registryFollower{stop: make(chan struct{}), done: make(chan struct{}), seen: map[string]bool{}}
	from := map[string]time.Time{}
	poll := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		var pods corev1.PodList
		if err := e.Client.List(ctx, &pods, client.InNamespace("registry"), client.MatchingLabels{"app": "registry"}); err != nil || len(pods.Items) == 0 {
			f.note(fmt.Sprintf("list the suite's registry Pods (components/registry.sh): %v, %d Pods", err, len(pods.Items)))
			return
		}
		for _, p := range pods.Items {
			at, ok := from[p.Name]
			if !ok {
				at = since
			}
			st := metav1.NewTime(at)
			raw, err := e.Kube.CoreV1().Pods("registry").GetLogs(p.Name, &corev1.PodLogOptions{SinceTime: &st, Timestamps: true}).DoRaw(ctx)
			if err != nil {
				f.note(fmt.Sprintf("logs of %s: %v", p.Name, err))
				continue
			}
			f.mu.Lock()
			for _, line := range strings.Split(string(raw), "\n") {
				ts, _, ok := strings.Cut(line, " ")
				if !ok {
					continue
				}
				f.seen[p.Name+" "+line] = true
				if tt, err := time.Parse(time.RFC3339Nano, ts); err == nil && tt.After(at) {
					at = tt
				}
			}
			f.mu.Unlock()
			from[p.Name] = at.Truncate(time.Second)
		}
	}
	go func() {
		defer close(f.done)
		for {
			poll()
			select {
			case <-f.stop:
				poll()
				return
			case <-time.After(5 * time.Second):
			}
		}
	}()
	t.Cleanup(f.end)
	return f
}

func (f *registryFollower) note(msg string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.errs) < 5 {
		f.errs = append(f.errs, msg)
	}
}

func (f *registryFollower) end() {
	select {
	case <-f.stop:
	default:
		close(f.stop)
	}
	<-f.done
}

// reads stops following and counts the requests for repo's manifests and
// blobs the registry answered (distribution logs a "response completed"
// line for each), with the errors met reading its log.
func (f *registryFollower) reads(repo string) (int, []string) {
	f.end()
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for line := range f.seen {
		if strings.Contains(line, "/v2/"+repo+"/") && strings.Contains(line, "response completed") {
			n++
		}
	}
	return n, f.errs
}

// TestScale_Attestations (#2062, evidence-scale): AttestBundles versions of
// three images each (500 in full) arrive spread over AttestWithin, every
// image with a key signature, a vulnerability scan and AttestReferrers
// referrers in all (SBOM-like attestations the policy does not ask for).
// Each Pipeline's prod waits at a gate over attestations.vulns that is
// rechecked only hourly. Every version is Verified in both
// environments; the ImageVerification reconcile p95 stays within
// AttestReconcileP95; the registry sees at most AttestReadsPerImage
// requests per image; and once everything is Verified the gates are not
// evaluated again in a quiet two minutes: attestation results reach a
// gate on change, not by polling.
//
// Covers SCALE-ATTEST-01.
func TestScale_Attestations(t *testing.T) {
	r := scale.Begin(t)
	e, ns, ctx := r.E, r.Fleet.NS, context.Background()
	reg := framework.NewRegistry(t)
	repo := "e2e/attest-" + ns[len(ns)-8:]
	release, err := signtest.NewKey()
	require.NoError(t, err)
	pem, err := release.PublicPEM()
	require.NoError(t, err)
	require.NoError(t, e.Client.Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "cosign", Namespace: ns, Labels: map[string]string{"kardinal.io/referenceable": "true"}},
		Data:       map[string][]byte{"cosign.pub": pem},
	}))
	policy := &v1alpha1.ImageVerificationPolicy{
		Authorities: []v1alpha1.SignatureAuthority{{Name: "release",
			Key: &v1alpha1.KeyAuthority{SecretRef: v1alpha1.SecretKeyName{Name: "cosign", Key: "cosign.pub"}}}},
		SignatureRepository: reg.Repository(repo),
		InsecureRegistries:  []string{reg.Host()},
		Timeout:             "15m",
		Attestations: []v1alpha1.AttestationPolicy{{Name: "vulns", PredicateType: iv.PredicateCosignVuln,
			Authorities: []string{"release"}}},
	}
	// Rechecked hourly, longer than the test: in the quiet window below an
	// evaluation can only come from a change.
	e.CreateGate(t, framework.Gate(ns, "no-critical", "prod", "attestations.vulns.effective.critical == 0", "1h"))

	n, refs := r.P.AttestBundles, max(r.P.AttestReferrers, 2)
	scan := map[string]any{"scanner": map[string]any{"uri": "pkg:github/aquasecurity/trivy@0.58.0", "version": "0.58.0",
		"result": map[string]any{"Results": []any{}}},
		"metadata": map[string]any{"scanFinishedOn": time.Now().UTC().Format(time.RFC3339)}}
	digest := func(i, j int) string { return fmt.Sprintf("sha256:%064x", 1+i*len(attestImages)+j) }
	pushStart := time.Now()
	const pushers = 16
	transport := framework.RegistryTransport(t, pushers)
	require.NoError(t, scale.Parallel(pushers, n*len(attestImages), func(k int) error {
		i, j := k/len(attestImages), k%len(attestImages)
		d := digest(i, j)
		var bundles [][]byte
		b, err := release.Bundle(attestImages[j], d)
		if err != nil {
			return err
		}
		bundles = append(bundles, b)
		if b, err = release.Attest(attestImages[j], d, iv.PredicateCosignVuln, scan); err != nil {
			return err
		}
		bundles = append(bundles, b)
		for f := 2; f < refs; f++ {
			if b, err = release.Attest(attestImages[j], d, "https://spdx.dev/Document", map[string]any{"n": f}); err != nil {
				return err
			}
			bundles = append(bundles, b)
		}
		for _, b := range bundles {
			if err := signtest.PushBundle(ctx, reg.HostRepository(repo), d, b, []name.Option{name.Insecure},
				remote.WithTransport(transport)); err != nil {
				return fmt.Errorf("push %s: %w", d, err)
			}
		}
		return nil
	}))
	r.Note("attestPushSeconds", int(time.Since(pushStart).Seconds()))

	pipelines := make([]string, n)
	for i := range pipelines {
		pipelines[i] = r.Fleet.Pipeline(t, fmt.Sprintf("att-%03d", i+1),
			[]v1alpha1.EnvironmentSpec{{Name: "test"}, {Name: "prod"}},
			func(p *v1alpha1.Pipeline) { p.Spec.ImageVerification = policy.DeepCopy() }).Name
	}
	for _, p := range pipelines {
		waitPipelineValid(t, e, ns, p)
	}

	before, start := scale.LeaderMetrics(t, e), time.Now()
	registry := followRegistry(t, e, start)
	spacing := r.P.AttestWithin / time.Duration(n)
	for i, p := range pipelines {
		var images []v1alpha1.ImageRef
		for j, img := range attestImages {
			images = append(images, v1alpha1.ImageRef{Repository: img, Digest: digest(i, j)})
		}
		r.E.CreateBundleObject(t, &v1alpha1.Bundle{
			ObjectMeta: metav1.ObjectMeta{Namespace: ns, GenerateName: p + "-"},
			Spec:       v1alpha1.BundleSpec{Type: "image", Pipeline: p, Images: images},
		})
		time.Sleep(spacing)
	}
	require.True(t, r.Fleet.WaitSettled(t, r.P.Settle), "every version settles")
	r.Note("attestSettleSeconds", int(time.Since(start).Seconds()))
	after := scale.LeaderMetrics(t, e)
	p95, reconciles := histogramQuantile(before, after, "controller_runtime_reconcile_time_seconds",
		map[string]string{"controller": "imageverification"}, 0.95)
	r.Note("attestIVReconcileP95Seconds", p95)
	r.Note("attestIVReconciles", int(reconciles))
	// The signals must be there: a missing metric or log line fails the
	// test, it never counts as 0.
	require.GreaterOrEqual(t, reconciles, float64(n), "controller_runtime_reconcile_time_seconds{controller=imageverification} has a reconcile per version")
	assert.LessOrEqual(t, p95, r.P.AttestReconcileP95.Seconds(), "ImageVerification reconcile p95 (AttestReconcileP95)")
	reads, logErrs := registry.reads(repo)
	require.GreaterOrEqual(t, reads, n*len(attestImages), "the registry logged at least one read per image (its \"response completed\" lines; errors reading its log: %v)", logErrs)
	perImage := float64(reads) / float64(n*len(attestImages))
	r.Note("attestRegistryReadsPerImage", perImage)
	assert.LessOrEqual(t, perImage, float64(r.P.AttestReadsPerImage), "registry requests per image (AttestReadsPerImage)")

	var ivs v1alpha1.ImageVerificationList
	require.NoError(t, e.Client.List(ctx, &ivs, client.InNamespace(ns)))
	verified := 0
	for _, v := range ivs.Items {
		if v.Status.Phase == v1alpha1.ImageVerificationVerified && len(v.Status.Attestations) == len(attestImages) {
			verified++
		}
	}
	assert.Equal(t, n, verified, "every version's ImageVerification Verified with each image's scan")

	// Quiet: nothing changes, so the gates are not evaluated again (their
	// recheck is hourly).
	quiet := scale.LeaderMetrics(t, e)
	require.Positive(t, quiet.Sum("kardinal_gate_evaluations_total", nil), "the leader reports kardinal_gate_evaluations_total")
	time.Sleep(2 * time.Minute)
	end := scale.LeaderMetrics(t, e)
	evals := end.Sum("kardinal_gate_evaluations_total", nil) - quiet.Sum("kardinal_gate_evaluations_total", nil)
	r.Note("attestQuietGateEvaluations", int(evals))
	assert.LessOrEqual(t, evals, float64(n)/20+2, "gate evaluations in a quiet two minutes over %d Verified versions", n)
	if os.Getenv(scale.EnvRace) == "1" {
		t.Log("controller built with -race: reconcile times are about 5x slower")
	}
	r.Finish(scale.AllVerified, scale.Skip("the versions deploy by digest three images the overlays name by tag", "env-content"))
}
