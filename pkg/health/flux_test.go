// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package health_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/health"
)

// The Kustomization statuses below are trimmed from ones Flux v2.9.5
// kustomize-controller wrote on a kind cluster (the flux e2e suite of #1356).

const (
	fluxPushed   = "034ce92a1b2c3d4e5f60718293a4b5c6d7e8f901"
	fluxPrevious = "d7d4d8a000000000000000000000000000000000"
)

// kustomization is the Kustomization flux-system/web-prod at generation 3,
// observed by Flux, with a Ready condition of ready (reason, message) and
// status.lastAppliedRevision applied. mutate changes the object.
func kustomization(ready, reason, message, applied string, mutate func(obj map[string]interface{})) *unstructured.Unstructured {
	obj := map[string]interface{}{
		"apiVersion": "kustomize.toolkit.fluxcd.io/v1",
		"kind":       "Kustomization",
		"metadata":   map[string]interface{}{"name": "web-prod", "namespace": "flux-system", "generation": int64(3)},
		"spec":       map[string]interface{}{"interval": "10m", "path": "./env/prod"},
		"status": map[string]interface{}{
			"observedGeneration":    int64(3),
			"lastAppliedRevision":   applied,
			"lastAttemptedRevision": applied,
			"conditions": []interface{}{map[string]interface{}{
				"type": "Ready", "status": ready, "reason": reason, "message": message,
			}},
		},
	}
	if mutate != nil {
		mutate(obj)
	}
	return &unstructured.Unstructured{Object: obj}
}

func checkFlux(t *testing.T, opts health.CheckOptions, objs ...runtime.Object) health.HealthStatus {
	t.Helper()
	opts.Flux = health.FluxConfig{Name: "web-prod", Namespace: "flux-system"}
	got, err := health.NewFluxAdapter(dynfake.NewSimpleDynamicClient(runtime.NewScheme(), objs...)).
		Check(context.Background(), opts)
	require.NoError(t, err)
	return got
}

// TestFluxAdapter_Suspended proves bug 11 of the health spike fixed: a
// promotion into a suspended Kustomization says the Kustomization is
// suspended while it waits. A suspended Kustomization that already applied
// the commit is healthy.
func TestFluxAdapter_Suspended(t *testing.T) {
	suspend := func(obj map[string]interface{}) {
		obj["spec"].(map[string]interface{})["suspend"] = true
	}
	tests := []struct {
		name   string
		obj    *unstructured.Unstructured
		want   wantKind
		reason string
	}{
		{name: "suspended on the previous commit",
			obj:  kustomization("True", "ReconciliationSucceeded", "Applied revision", "main@sha1:"+fluxPrevious, suspend),
			want: isProgressing,
			reason: "Kustomization flux-system/web-prod is suspended; Flux applies nothing until it is resumed " +
				"(Ready=True, observedGen=3, generation=3, lastAppliedRevision=d7d4d8a00000, waiting for 034ce92a1b2c)"},
		{name: "suspended on the pushed commit",
			obj:    kustomization("True", "ReconciliationSucceeded", "Applied revision", "main@sha1:"+fluxPushed, suspend),
			want:   isHealthy,
			reason: "Ready=True, generation=3 matches, lastAppliedRevision=034ce92a1b2c"},
		{name: "not suspended on the previous commit",
			obj:    kustomization("True", "ReconciliationSucceeded", "Applied revision", "main@sha1:"+fluxPrevious, nil),
			want:   isProgressing,
			reason: "Ready=True, observedGen=3, generation=3, lastAppliedRevision=d7d4d8a00000, waiting for 034ce92a1b2c"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := checkFlux(t, health.CheckOptions{ExpectedRevision: fluxPushed}, tt.obj)
			assert.Equal(t, tt.want, kindOf(got), got.Reason)
			assert.Equal(t, tt.reason, got.Reason)
		})
	}
}

// stalledMessage is the Ready=False message Flux writes when it gives up on a
// Deployment past its progress deadline.
const stalledMessage = "health check failed after 1m5.017502721s: failed early due to stalled resources: " +
	"[Deployment/prod/web status: 'Failed']"

// TestFluxAdapter_Stalled proves bug 12 of the health spike fixed: Flux
// giving up on the promoted commit because its Deployment stalled is
// terminal, so the step fails at once instead of at health.timeout. Other
// Ready=False results on the promoted commit stay unhealthy; a stall on
// another commit with no Deployment on the Bundle images waits (B94).
func TestFluxAdapter_Stalled(t *testing.T) {
	attempted := func(rev string) func(obj map[string]interface{}) {
		return func(obj map[string]interface{}) {
			obj["status"].(map[string]interface{})["lastAttemptedRevision"] = rev
		}
	}
	tests := []struct {
		name     string
		obj      *unstructured.Unstructured
		expected string
		want     wantKind
		reason   string
	}{
		{name: "the promoted commit stalled",
			obj: kustomization("False", "HealthCheckFailed", stalledMessage, "main@sha1:"+fluxPrevious,
				attempted("main@sha1:"+fluxPushed)),
			expected: fluxPushed, want: isTerminal,
			reason: "Ready=False, observedGen=3, generation=3: " + stalledMessage + " (lastAttemptedRevision=034ce92a1b2c)"},
		{name: "a stall with no expected commit",
			obj:  kustomization("False", "HealthCheckFailed", stalledMessage, "main@sha1:"+fluxPrevious, nil),
			want: isTerminal, reason: "stalled resources"},
		{name: "another commit stalled",
			obj: kustomization("False", "HealthCheckFailed", stalledMessage, "main@sha1:"+fluxPrevious,
				attempted("main@sha1:"+fluxPrevious)),
			expected: fluxPushed, want: isProgressing,
			reason: stalledMessage + " (lastAttemptedRevision=d7d4d8a00000, not 034ce92a1b2c: Flux has not applied the promoted change"},
		{name: "a health check timeout",
			obj: kustomization("False", "HealthCheckFailed", "health check failed after 3m0s: timeout waiting for: "+
				"[Deployment/prod/web status: 'InProgress']", "main@sha1:"+fluxPrevious, attempted("main@sha1:"+fluxPushed)),
			expected: fluxPushed, want: isUnhealthy, reason: "timeout waiting for"},
		{name: "a build failure",
			obj: kustomization("False", "BuildFailed", "kustomize build failed: accumulating resources", "main@sha1:"+fluxPrevious,
				attempted("main@sha1:"+fluxPushed)),
			expected: fluxPushed, want: isUnhealthy, reason: "kustomize build failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := checkFlux(t, health.CheckOptions{ExpectedRevision: tt.expected}, tt.obj)
			assert.Equal(t, tt.want, kindOf(got), got.Reason)
			assert.Contains(t, got.Reason, tt.reason)
		})
	}
}

// fluxLater is a commit another environment pushed after fluxPushed.
const fluxLater = "8e9966475a0b1c2d3e4f5061728394a5b6c7d8e9"

// withInventory sets status.inventory to the Deployments names in namespace
// prod, plus a Service that is never checked.
func withInventory(names ...string) func(obj map[string]interface{}) {
	return func(obj map[string]interface{}) {
		entries := []interface{}{map[string]interface{}{"id": "prod_web__Service", "v": "v1"}}
		for _, n := range names {
			entries = append(entries, map[string]interface{}{"id": "prod_" + n + "_apps_Deployment", "v": "v1"})
		}
		obj["status"].(map[string]interface{})["inventory"] = map[string]interface{}{"entries": entries}
	}
}

func withSpec(key string, value interface{}) func(obj map[string]interface{}) {
	return func(obj map[string]interface{}) {
		obj["spec"].(map[string]interface{})[key] = value
	}
}

func both(fs ...func(obj map[string]interface{})) func(obj map[string]interface{}) {
	return func(obj map[string]interface{}) {
		for _, f := range fs {
			f(obj)
		}
	}
}

// stalledDeployment is Deployment prod/<name> running image, past its
// progress deadline.
func stalledDeployment(name, image string) *unstructured.Unstructured {
	d := deploymentObj(name, 2, image, 1)
	d.Object["status"].(map[string]interface{})["conditions"].([]interface{})[1] = map[string]interface{}{
		"type": "Progressing", "status": "False", "reason": "ProgressDeadlineExceeded"}
	return d
}

// lostReplicas is Deployment prod/<name> running image, rolled out, with no
// replica available.
func lostReplicas(name, image string) *unstructured.Unstructured {
	d := deploymentObj(name, 2, image, 1)
	d.Object["status"].(map[string]interface{})["availableReplicas"] = int64(0)
	return d
}

// TestFluxAdapter_SharedBranch proves bug 6 of the health spike fixed: when
// Flux applied a later commit of the shared branch (a sibling environment's
// push), the environment is healthy only if the Kustomization's Deployments
// run the Bundle images and are rolled out. Otherwise it waits for its commit.
func TestFluxAdapter_SharedBranch(t *testing.T) {
	bundle := []health.ImageExpectation{{Repository: podinfo, Tag: "6.15.0"}}
	later := func(mutate func(obj map[string]interface{})) *unstructured.Unstructured {
		return kustomization("True", "ReconciliationSucceeded", "Applied revision", "main@sha1:"+fluxLater, mutate)
	}
	unavailable := deploymentObj("web", 2, podinfo+":6.15.0", 1)
	unavailable.Object["status"].(map[string]interface{})["availableReplicas"] = int64(0)
	const waiting = "lastAppliedRevision=8e9966475a0b, waiting for 034ce92a1b2c"
	const accepted = "Ready=True, generation=3 matches, lastAppliedRevision=8e9966475a0b " +
		"(not 034ce92a1b2c, but the Kustomization's Deployments run the Bundle images)"
	healthCheck := withSpec("healthChecks", []interface{}{map[string]interface{}{
		"apiVersion": "apps/v1", "kind": "Deployment", "name": "web", "namespace": "prod"}})
	tests := []struct {
		name   string
		objs   []runtime.Object
		images []health.ImageExpectation
		want   wantKind
		reason string
	}{
		{name: "the inventory Deployment runs the Bundle image",
			objs: []runtime.Object{later(withInventory("web")), deploymentObj("web", 2, podinfo+":6.15.0", 1)},
			want: isHealthy, reason: accepted},
		{name: "the health-checked Deployment runs the Bundle image",
			objs: []runtime.Object{later(healthCheck), deploymentObj("web", 2, podinfo+":6.15.0", 1)},
			want: isHealthy, reason: accepted},
		{name: "a second Deployment runs an image the Bundle does not have",
			objs: []runtime.Object{later(withInventory("web", "cache")), deploymentObj("web", 2, podinfo+":6.15.0", 1),
				deploymentObj("cache", 1, "docker.io/library/redis:7", 1)},
			want: isHealthy, reason: accepted},
		{name: "the Deployment runs the previous image",
			objs: []runtime.Object{later(withInventory("web")), deploymentObj("web", 2, podinfo+":6.14.0", 1)},
			want: isProgressing, reason: waiting},
		{name: "a second Deployment runs another tag of the Bundle repository",
			objs: []runtime.Object{later(withInventory("web", "web-canary")), deploymentObj("web", 2, podinfo+":6.15.0", 1),
				deploymentObj("web-canary", 2, podinfo+":6.14.0", 1)},
			want: isProgressing, reason: waiting},
		{name: "a second Deployment that runs no Bundle image lost its replicas",
			objs: []runtime.Object{later(withInventory("web", "cache")), deploymentObj("web", 2, podinfo+":6.15.0", 1),
				lostReplicas("cache", "docker.io/library/redis:7")},
			want: isProgressing, reason: waiting},
		{name: "the Deployment runs no Bundle repository",
			objs: []runtime.Object{later(withInventory("cache")), deploymentObj("cache", 1, "docker.io/library/redis:7", 1)},
			want: isProgressing, reason: waiting},
		{name: "the Deployment is not available",
			objs: []runtime.Object{later(withInventory("web")), unavailable},
			want: isProgressing, reason: waiting},
		{name: "the inventory Deployment is gone",
			objs: []runtime.Object{later(withInventory("web"))},
			want: isProgressing, reason: waiting},
		{name: "no Deployments", objs: []runtime.Object{later(nil)},
			want: isProgressing, reason: waiting},
		{name: "the Kustomization applies to another cluster",
			objs: []runtime.Object{later(both(withInventory("web"),
				withSpec("kubeConfig", map[string]interface{}{"secretRef": map[string]interface{}{"name": "remote"}}))),
				deploymentObj("web", 2, podinfo+":6.15.0", 1)},
			want: isProgressing, reason: waiting},
		{name: "no Bundle images to compare",
			objs:   []runtime.Object{later(withInventory("web")), deploymentObj("web", 2, podinfo+":6.15.0", 1)},
			images: []health.ImageExpectation{}, want: isProgressing, reason: waiting},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			images := bundle
			if tt.images != nil {
				images = tt.images
			}
			got := checkFlux(t, health.CheckOptions{ExpectedRevision: fluxPushed, ExpectedImages: images}, tt.objs...)
			assert.Equal(t, tt.want, kindOf(got), got.Reason)
			assert.Contains(t, got.Reason, tt.reason)
		})
	}
}

// TestFluxAdapter_StalledSharedBranch: when Flux gave up on another commit
// of the shared branch (a sibling environment pushed after the promoted
// commit) and a Deployment that runs the Bundle images is itself past its
// progress deadline, Flux applied the Bundle's change and that rollout
// stalled: the step fails at once. The stall of a Deployment that runs no
// Bundle image stays unhealthy while a Bundle Deployment runs the Bundle
// images, healthy or not (review item 2 of the flux suite). With no
// Deployment on the Bundle images Flux has not applied the promoted change,
// and the step waits (B94).
func TestFluxAdapter_StalledSharedBranch(t *testing.T) {
	bundle := []health.ImageExpectation{{Repository: podinfo, Tag: "6.15.0"}}
	stalledLater := func(mutate func(obj map[string]interface{})) *unstructured.Unstructured {
		return kustomization("False", "HealthCheckFailed", stalledMessage, "main@sha1:"+fluxPrevious, both(
			func(obj map[string]interface{}) {
				obj["status"].(map[string]interface{})["lastAttemptedRevision"] = "main@sha1:" + fluxLater
			}, mutate))
	}
	const unhealthyReason = "Ready=False, observedGen=3, generation=3: " + stalledMessage
	const notApplied = unhealthyReason + " (lastAttemptedRevision=8e9966475a0b, not 034ce92a1b2c: Flux has not applied " +
		"the promoted change, and the Kustomization's Deployments do not run the Bundle images)"
	tests := []struct {
		name   string
		objs   []runtime.Object
		images []health.ImageExpectation
		want   wantKind
		reason string
	}{
		{name: "the stalled Deployment runs the Bundle image",
			objs: []runtime.Object{stalledLater(withInventory("web")), stalledDeployment("web", podinfo+":6.15.0")},
			want: isTerminal,
			reason: unhealthyReason + " (lastAttemptedRevision=8e9966475a0b, " +
				"not 034ce92a1b2c, but Deployment prod/web, which runs the Bundle images, stalled)"},
		{name: "the Bundle Deployment is healthy and a Deployment that runs no Bundle image stalled",
			objs: []runtime.Object{stalledLater(withInventory("web", "cache")), deploymentObj("web", 2, podinfo+":6.15.0", 1),
				stalledDeployment("cache", "docker.io/library/redis:7")},
			want: isUnhealthy, reason: unhealthyReason},
		{name: "the Bundle Deployment lost its replicas but did not stall",
			objs: []runtime.Object{stalledLater(withInventory("web", "cache")), lostReplicas("web", podinfo+":6.15.0"),
				stalledDeployment("cache", "docker.io/library/redis:7")},
			want: isUnhealthy, reason: unhealthyReason},
		{name: "the stalled Deployment runs the previous image",
			objs: []runtime.Object{stalledLater(withInventory("web")), stalledDeployment("web", podinfo+":6.14.0")},
			want: isProgressing, reason: notApplied},
		{name: "no Deployments",
			objs: []runtime.Object{stalledLater(func(map[string]interface{}) {})},
			want: isProgressing, reason: notApplied},
		{name: "no Bundle images to compare",
			objs:   []runtime.Object{stalledLater(withInventory("web")), stalledDeployment("web", podinfo+":6.15.0")},
			images: []health.ImageExpectation{}, want: isProgressing, reason: notApplied},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			images := bundle
			if tt.images != nil {
				images = tt.images
			}
			got := checkFlux(t, health.CheckOptions{ExpectedRevision: fluxPushed, ExpectedImages: images}, tt.objs...)
			assert.Equal(t, tt.want, kindOf(got), got.Reason)
			assert.Equal(t, tt.reason, got.Reason)
		})
	}
}

// TestFluxAdapter_FailedOnAnotherCommit proves B94 fixed: Ready=False on a
// commit other than the promoted one, while no Deployment of the
// Kustomization runs the Bundle images, waits instead of counting a health
// failure. That is a fix-forward after a failed release until Flux fetches
// the fix, and a RollbackPolicy must not roll the fix back meanwhile. Once a
// Deployment runs the Bundle images, or when the attempted revision is not a
// git commit or there is none, Ready=False stays unhealthy.
func TestFluxAdapter_FailedOnAnotherCommit(t *testing.T) {
	bundle := []health.ImageExpectation{{Repository: podinfo, Tag: "6.15.0"}}
	const timeout = "health check failed after 3m0s: timeout waiting for: [Deployment/prod/web status: 'InProgress']"
	const build = "kustomize build failed: accumulating resources"
	failed := func(reason, message, attempted string, mutate func(obj map[string]interface{})) *unstructured.Unstructured {
		return kustomization("False", reason, message, "main@sha1:"+fluxPrevious, both(func(obj map[string]interface{}) {
			obj["status"].(map[string]interface{})["lastAttemptedRevision"] = attempted
		}, withInventory("web"), func(obj map[string]interface{}) {
			if mutate != nil {
				mutate(obj)
			}
		}))
	}
	const notApplied = " (lastAttemptedRevision=d7d4d8a00000, not 034ce92a1b2c: Flux has not applied the promoted " +
		"change, and the Kustomization's Deployments do not run the Bundle images)"
	previous := "main@sha1:" + fluxPrevious
	tests := []struct {
		name       string
		objs       []runtime.Object
		noRevision bool
		getErr     bool
		want       wantKind
		reason     string
	}{
		{name: "a timeout on the previous commit, the Deployment on the previous image",
			objs: []runtime.Object{failed("HealthCheckFailed", timeout, previous, nil), deploymentObj("web", 2, podinfo+":6.14.0", 1)},
			want: isProgressing, reason: "Ready=False, observedGen=3, generation=3: " + timeout + notApplied},
		{name: "a build failure on the previous commit, the Deployment gone",
			objs: []runtime.Object{failed("BuildFailed", build, previous, nil)},
			want: isProgressing, reason: "Ready=False, observedGen=3, generation=3: " + build + notApplied},
		{name: "a second Deployment runs another tag of the Bundle repository",
			objs: []runtime.Object{failed("HealthCheckFailed", timeout, previous, withInventory("web", "web-canary")),
				deploymentObj("web", 2, podinfo+":6.15.0", 1), deploymentObj("web-canary", 2, podinfo+":6.14.0", 1)},
			want: isProgressing, reason: "Ready=False, observedGen=3, generation=3: " + timeout + notApplied},
		{name: "the Kustomization applies to another cluster",
			objs: []runtime.Object{failed("HealthCheckFailed", timeout, previous,
				withSpec("kubeConfig", map[string]interface{}{"secretRef": map[string]interface{}{"name": "remote"}})),
				deploymentObj("web", 2, podinfo+":6.15.0", 1)},
			want: isProgressing, reason: "Ready=False, observedGen=3, generation=3: " + timeout + notApplied},
		{name: "a suspended Kustomization",
			objs: []runtime.Object{failed("BuildFailed", build, previous, withSpec("suspend", true))},
			want: isProgressing, reason: "Kustomization flux-system/web-prod is suspended; Flux applies nothing until it is " +
				"resumed (Ready=False, observedGen=3, generation=3: " + build + notApplied + ")"},
		{name: "a timeout on a later commit, the Deployment on the Bundle image",
			objs: []runtime.Object{failed("HealthCheckFailed", timeout, "main@sha1:"+fluxLater, nil), deploymentObj("web", 2, podinfo+":6.15.0", 1)},
			want: isUnhealthy, reason: "Ready=False, observedGen=3, generation=3: " + timeout},
		{name: "a build failure on a later commit, the Deployment on the Bundle image past its deadline",
			objs: []runtime.Object{failed("BuildFailed", build, "main@sha1:"+fluxLater, nil), stalledDeployment("web", podinfo+":6.15.0")},
			want: isUnhealthy, reason: "Ready=False, observedGen=3, generation=3: " + build},
		{name: "an OCI artifact revision",
			objs: []runtime.Object{failed("HealthCheckFailed", timeout, "latest@sha256:6c1a4b0e", nil), deploymentObj("web", 2, podinfo+":6.14.0", 1)},
			want: isUnhealthy, reason: "Ready=False, observedGen=3, generation=3: " + timeout},
		{name: "no attempted revision",
			objs: []runtime.Object{failed("ArtifactFailed", "Source artifact not found", "", nil)},
			want: isUnhealthy, reason: "Ready=False, observedGen=3, generation=3: Source artifact not found"},
		{name: "no expected commit",
			objs:       []runtime.Object{failed("HealthCheckFailed", timeout, previous, nil), deploymentObj("web", 2, podinfo+":6.14.0", 1)},
			noRevision: true, want: isUnhealthy, reason: "Ready=False, observedGen=3, generation=3: " + timeout},
		{name: "a Deployment that cannot be read",
			objs:   []runtime.Object{failed("HealthCheckFailed", timeout, previous, nil), deploymentObj("web", 2, podinfo+":6.14.0", 1)},
			getErr: true, want: isUnhealthy, reason: "Ready=False, observedGen=3, generation=3: " + timeout},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := health.CheckOptions{ExpectedRevision: fluxPushed, ExpectedImages: bundle,
				Flux: health.FluxConfig{Name: "web-prod", Namespace: "flux-system"}}
			if tt.noRevision {
				opts.ExpectedRevision = ""
			}
			c := dynfake.NewSimpleDynamicClient(runtime.NewScheme(), tt.objs...)
			if tt.getErr {
				c.PrependReactor("get", "deployments", func(k8stesting.Action) (bool, runtime.Object, error) {
					return true, nil, errors.New("the server is currently unable to handle the request")
				})
			}
			got, err := health.NewFluxAdapter(c).Check(context.Background(), opts)
			require.NoError(t, err)
			assert.Equal(t, tt.want, kindOf(got), got.Reason)
			assert.Equal(t, tt.reason, got.Reason)
		})
	}
}

// reconciling is the Kustomization Flux v2.9.5 reports while it reconciles
// again: Ready=Unknown, reason Progressing, the last applied revision still
// in place.
func reconciling(applied, attempted string, mutate func(obj map[string]interface{})) *unstructured.Unstructured {
	return kustomization("Unknown", "Progressing", "Reconciliation in progress", applied, both(
		func(obj map[string]interface{}) {
			obj["status"].(map[string]interface{})["lastAttemptedRevision"] = attempted
		}, withInventory("web"), func(obj map[string]interface{}) {
			if mutate != nil {
				mutate(obj)
			}
		}))
}

// TestFluxAdapter_ReconcilingAgain proves the bake flicker of the flux e2e
// suite fixed: Flux marks a Kustomization Ready=Unknown at the start of
// every reconcile, also when it reconciles again the commit it applied. That
// result is that of the Deployments that run a Bundle repository, so a bake
// neither stops on a healthy release nor misses a failure that Flux is still
// checking. A Deployment that runs no Bundle image makes it wait, never fail
// (review item 1 of the flux suite). A Kustomization that is applying
// another commit still waits.
func TestFluxAdapter_ReconcilingAgain(t *testing.T) {
	bundle := []health.ImageExpectation{{Repository: podinfo, Tag: "6.15.0"}}
	pushed, previous, later := "main@sha1:"+fluxPushed, "main@sha1:"+fluxPrevious, "main@sha1:"+fluxLater
	good := deploymentObj("web", 2, podinfo+":6.15.0", 1)
	unready := deploymentObj("web", 2, podinfo+":6.15.0", 1)
	unready.Object["status"].(map[string]interface{})["availableReplicas"] = int64(0)
	stalled := deploymentObj("web", 2, podinfo+":6.15.0", 1)
	stalled.Object["status"].(map[string]interface{})["conditions"].([]interface{})[1] = map[string]interface{}{
		"type": "Progressing", "status": "False", "reason": "ProgressDeadlineExceeded"}
	tests := []struct {
		name   string
		objs   []runtime.Object
		want   wantKind
		reason string
	}{
		{name: "the promoted commit again, Deployment healthy",
			objs: []runtime.Object{reconciling(pushed, pushed, nil), good}, want: isHealthy,
			reason: "Ready=Unknown while Flux reconciles lastAppliedRevision=034ce92a1b2c again: " +
				"Deployment prod/web: Available=True, 1/1 replicas updated and available"},
		{name: "the promoted commit again, Deployment lost its replicas",
			objs: []runtime.Object{reconciling(pushed, pushed, nil), unready}, want: isUnhealthy,
			reason: "Ready=Unknown while Flux reconciles lastAppliedRevision=034ce92a1b2c again: " +
				"Deployment prod/web: 0 of 1 updated replicas available"},
		{name: "the promoted commit again, Deployment past its deadline",
			objs: []runtime.Object{reconciling(pushed, pushed, nil), stalled}, want: isTerminal,
			reason: "ProgressDeadlineExceeded"},
		{name: "a sibling's later commit again, Deployment on the Bundle image",
			objs: []runtime.Object{reconciling(later, later, nil), good}, want: isHealthy,
			reason: "(not 034ce92a1b2c, but the Kustomization's Deployments run the Bundle images)"},
		{name: "the previous commit again",
			objs: []runtime.Object{reconciling(previous, previous, nil), deploymentObj("web", 2, podinfo+":6.14.0", 1)},
			want: isProgressing, reason: "lastAppliedRevision=d7d4d8a00000, waiting for 034ce92a1b2c"},
		{name: "applying the promoted commit",
			objs: []runtime.Object{reconciling(previous, pushed, nil), good}, want: isProgressing,
			reason: "Ready=Unknown, observedGen=3, generation=3: Reconciliation in progress"},
		{name: "a new generation",
			objs: []runtime.Object{reconciling(pushed, pushed, func(obj map[string]interface{}) {
				obj["metadata"].(map[string]interface{})["generation"] = int64(4)
			}), good}, want: isProgressing, reason: "Ready=Unknown, observedGen=3, generation=4"},
		{name: "no Deployments",
			objs: []runtime.Object{reconciling(pushed, pushed, func(obj map[string]interface{}) {
				delete(obj["status"].(map[string]interface{}), "inventory")
			})}, want: isProgressing, reason: "Ready=Unknown, observedGen=3, generation=3: Reconciliation in progress"},
		{name: "a Deployment that runs no Bundle image stalled while the Bundle Deployment is healthy",
			objs: []runtime.Object{reconciling(pushed, pushed, withInventory("web", "cache")), good,
				stalledDeployment("cache", "docker.io/library/redis:7")}, want: isProgressing,
			reason: "Ready=Unknown while Flux reconciles lastAppliedRevision=034ce92a1b2c again: " +
				"Deployment prod/web: Available=True, 1/1 replicas updated and available; waiting for Flux, because a " +
				"Deployment that runs no Bundle image is not healthy: Deployment prod/cache rollout failed: ProgressDeadlineExceeded"},
		{name: "a Deployment that runs no Bundle image lost its replicas while the Bundle Deployment is healthy",
			objs: []runtime.Object{reconciling(pushed, pushed, withInventory("web", "cache")), good,
				lostReplicas("cache", "docker.io/library/redis:7")}, want: isProgressing,
			reason: "is not healthy: Deployment prod/cache: 0 of 1 updated replicas available"},
		{name: "the Bundle Deployment lost its replicas and another Deployment stalled",
			objs: []runtime.Object{reconciling(pushed, pushed, withInventory("web", "cache")), unready,
				stalledDeployment("cache", "docker.io/library/redis:7")}, want: isUnhealthy,
			reason: "Ready=Unknown while Flux reconciles lastAppliedRevision=034ce92a1b2c again: " +
				"Deployment prod/web: 0 of 1 updated replicas available"},
		{name: "only Deployments that run no Bundle image",
			objs: []runtime.Object{reconciling(pushed, pushed, withInventory("cache")),
				deploymentObj("cache", 1, "docker.io/library/redis:7", 1)},
			want: isProgressing, reason: "Ready=Unknown, observedGen=3, generation=3: Reconciliation in progress"},
		{name: "a sibling's commit again while a second Deployment runs another tag of the Bundle repository",
			objs: []runtime.Object{reconciling(later, later, withInventory("web", "web-canary")), good,
				deploymentObj("web-canary", 2, podinfo+":6.14.0", 1)},
			want: isProgressing, reason: "lastAppliedRevision=8e9966475a0b, waiting for 034ce92a1b2c"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := checkFlux(t, health.CheckOptions{ExpectedRevision: fluxPushed, ExpectedImages: bundle}, tt.objs...)
			assert.Equal(t, tt.want, kindOf(got), got.Reason)
			assert.Contains(t, got.Reason, tt.reason)
			// The flux check does not date the target update (status.targetUpdatedAt).
			assert.False(t, got.TargetUpdated, "TargetUpdated")
		})
	}
}
