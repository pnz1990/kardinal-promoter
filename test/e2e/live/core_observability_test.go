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
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
)

// healthPort is the controller's --health-probe-bind-address port.
const healthPort = 8081

// TestCore_ControllerProbes checks the controller's probes (chart
// templates/deployment.yaml): the readiness probe is GET /readyz and the
// liveness probe GET /healthz, both on the container port named health,
// which is 8081, the port --health-probe-bind-address binds. The running
// controller is Ready, and both endpoints answer 200 "ok"; verbose output
// names the readyz and healthz checks.
//
// Covers OBS-READYZ-01, OBS-HEALTHZ-01.
func TestCore_ControllerProbes(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	c := controllerContainer(t, e)
	checkProbes(t, c)

	pod := e.RunningPod(t, framework.ControllerNamespace, "app.kubernetes.io/name=kardinal-promoter")
	p, err := e.Kube.CoreV1().Pods(framework.ControllerNamespace).Get(ctx, pod, metav1.GetOptions{})
	require.NoError(t, err)
	assert.True(t, podReady(p), "the controller pod %s is Ready", pod)
	for path, body := range map[string]string{
		"/readyz": "ok", "/healthz": "ok",
		"/readyz?verbose": "[+]readyz ok\n", "/healthz?verbose": "[+]healthz ok\n",
	} {
		code, got, err := e.PodHTTP(ctx, framework.ControllerNamespace, pod, healthPort, "GET", path)
		require.NoError(t, err, "GET %s", path)
		assert.Equal(t, 200, code, "GET %s: %s", path, got)
		assert.True(t, strings.HasPrefix(got, body), "GET %s: body %q", path, got)
	}
}

// TestCore_ReadyzWaitsForCaches runs a second copy of the controller (the
// same image, args, probes and security settings, without leader election and
// without the token Secrets) as a ServiceAccount with no RBAC, so its informer
// caches can never sync. /readyz then answers 500 "[-]readyz failed" and the
// kubelet reports the readiness probe failing, so the pod never becomes Ready
// and would get no Service traffic. /healthz keeps answering 200 "ok": the
// liveness probe passes, and the kubelet does not restart the container while
// it waits, through the probe's whole failure window (initialDelaySeconds +
// failureThreshold x periodSeconds). The copy has its own labels, so it is
// never a kardinal-promoter Service endpoint, and without RBAC it cannot act
// on any object.
//
// Covers OBS-READYZ-01, OBS-HEALTHZ-01.
func TestCore_ReadyzWaitsForCaches(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	ns := e.Namespace(t)
	dep := controllerDeployment(t, e)
	c := controllerContainer(t, e)
	checkProbes(t, c)

	const name = "controller-no-rbac"
	require.NoError(t, e.Client.Create(ctx, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "no-rbac", Namespace: ns}}))
	require.Contains(t, c.Args, "--leader-elect=true")
	c.Args = replaceArg(c.Args, "--leader-elect=true", "--leader-elect=false")
	var env []corev1.EnvVar
	for _, v := range c.Env {
		if v.ValueFrom == nil || v.ValueFrom.FieldRef != nil {
			env = append(env, v)
		}
	}
	c.Env = env
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: map[string]string{"app": name}},
		Spec: corev1.PodSpec{
			ServiceAccountName: "no-rbac",
			SecurityContext:    dep.Spec.Template.Spec.SecurityContext,
			Volumes:            dep.Spec.Template.Spec.Volumes,
			Containers:         []corev1.Container{c},
		},
	}
	require.NoError(t, e.Client.Create(ctx, pod))

	var started time.Time
	framework.Eventually(t, 2*time.Minute, "the controller copy to start", func(ctx context.Context) (bool, string) {
		p, err := e.Kube.CoreV1().Pods(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		for _, cs := range p.Status.ContainerStatuses {
			if cs.State.Running != nil {
				started = cs.State.Running.StartedAt.Time
				return true, ""
			}
			return false, fmt.Sprintf("container state %+v", cs.State)
		}
		return false, "phase " + string(p.Status.Phase)
	})

	framework.Eventually(t, time.Minute, "the kubelet to report the readiness probe failing", func(ctx context.Context) (bool, string) {
		evs, err := probeEvents(ctx, e, ns, name, "Readiness probe failed")
		if err != nil {
			return false, err.Error()
		}
		return len(evs) > 0, "no Unhealthy readiness Event yet"
	})
	code, body, err := e.PodHTTP(ctx, ns, name, healthPort, "GET", "/readyz")
	require.NoError(t, err)
	assert.Equal(t, 500, code, "GET /readyz before the caches sync: %s", body)
	assert.Contains(t, body, "[-]readyz failed")
	code, body, err = e.PodHTTP(ctx, ns, name, healthPort, "GET", "/healthz")
	require.NoError(t, err)
	assert.Equal(t, 200, code, "GET /healthz: %s", body)
	assert.Equal(t, "ok", body)

	lp := c.LivenessProbe
	window := time.Duration(lp.InitialDelaySeconds+lp.FailureThreshold*lp.PeriodSeconds) * time.Second
	framework.Consistently(t, time.Until(started.Add(window+5*time.Second)),
		"the copy unready but live, never restarted", func(ctx context.Context) (bool, string) {
			p, err := e.Kube.CoreV1().Pods(ns).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return ctx.Err() != nil, err.Error()
			}
			if podReady(p) {
				return false, "the pod became Ready"
			}
			for _, cs := range p.Status.ContainerStatuses {
				if cs.RestartCount != 0 || cs.State.Running == nil {
					return false, fmt.Sprintf("restarts=%d state=%+v", cs.RestartCount, cs.State)
				}
			}
			code, body, err := e.PodHTTP(ctx, ns, name, healthPort, "GET", "/healthz")
			if err != nil {
				return ctx.Err() != nil, err.Error()
			}
			return code == 200, fmt.Sprintf("GET /healthz: %d %s", code, body)
		})
	evs, err := probeEvents(ctx, e, ns, name, "Liveness probe failed")
	require.NoError(t, err)
	assert.Empty(t, evs, "no failed liveness probe")
}

// controllerDeployment is the kardinal-promoter Deployment.
func controllerDeployment(t *testing.T, e *framework.Env) *appsv1.Deployment {
	t.Helper()
	dep, err := e.Kube.AppsV1().Deployments(framework.ControllerNamespace).Get(context.Background(),
		"kardinal-promoter", metav1.GetOptions{})
	require.NoError(t, err)
	return dep
}

// controllerContainer is a copy of the Deployment's controller container.
func controllerContainer(t *testing.T, e *framework.Env) corev1.Container {
	t.Helper()
	for _, c := range controllerDeployment(t, e).Spec.Template.Spec.Containers {
		if c.Name == "controller" {
			return *c.DeepCopy()
		}
	}
	t.Fatal("the kardinal-promoter Deployment has no controller container")
	return corev1.Container{}
}

// checkProbes checks c's readiness probe is GET /readyz and its liveness
// probe GET /healthz on the health port, which the health probe server binds.
func checkProbes(t *testing.T, c corev1.Container) {
	t.Helper()
	for _, p := range []struct {
		kind  string
		probe *corev1.Probe
		path  string
	}{{"readiness", c.ReadinessProbe, "/readyz"}, {"liveness", c.LivenessProbe, "/healthz"}} {
		require.NotNil(t, p.probe, "%s probe", p.kind)
		require.NotNil(t, p.probe.HTTPGet, "%s probe is an HTTP GET", p.kind)
		assert.Equal(t, p.path, p.probe.HTTPGet.Path, "%s probe path", p.kind)
		assert.Equal(t, intstr.FromString("health"), p.probe.HTTPGet.Port, "%s probe port", p.kind)
	}
	var port int32
	for _, p := range c.Ports {
		if p.Name == "health" {
			port = p.ContainerPort
		}
	}
	assert.EqualValues(t, healthPort, port, "the health container port")
	assert.Contains(t, c.Args, fmt.Sprintf("--health-probe-bind-address=:%d", healthPort))
}

// replaceArg returns args with old replaced by repl.
func replaceArg(args []string, old, repl string) []string {
	out := make([]string, 0, len(args))
	for _, a := range args {
		if a == old {
			a = repl
		}
		out = append(out, a)
	}
	return out
}

// probeEvents lists the kubelet's Unhealthy Events for pod whose message
// starts with prefix.
func probeEvents(ctx context.Context, e *framework.Env, ns, pod, prefix string) ([]corev1.Event, error) {
	list, err := e.Kube.CoreV1().Events(ns).List(ctx, metav1.ListOptions{FieldSelector: "involvedObject.name=" + pod})
	if err != nil {
		return nil, err
	}
	var out []corev1.Event
	for _, ev := range list.Items {
		if ev.Reason == "Unhealthy" && strings.HasPrefix(ev.Message, prefix) {
			out = append(out, ev)
		}
	}
	return out, nil
}
