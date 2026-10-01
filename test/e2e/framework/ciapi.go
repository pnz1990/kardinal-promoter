// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package framework

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// Environment variables hack/e2e/components/ciapi.sh sets.
const (
	// EnvBundleToken is the Bundle API token (never logged).
	EnvBundleToken = "KARDINAL_E2E_BUNDLE_TOKEN"
	// EnvControllerURL is the controller's webhook port (8083) from the host.
	EnvControllerURL = "KARDINAL_E2E_CONTROLLER_URL"
)

// ControllerDeployment is the chart's controller Deployment in
// ControllerNamespace.
const ControllerDeployment = "kardinal-promoter"

// HTTPResult is an HTTP response as a test sees it.
type HTTPResult struct {
	Status int
	Header http.Header
	Body   string
}

// HTTP sends one request and returns the response. headers are set as
// given; their values are never logged (they carry tokens). It fails the test
// only when no response arrives.
func HTTP(t *testing.T, method, url string, headers map[string]string, body []byte) HTTPResult {
	t.Helper()
	res, err := doHTTP(context.Background(), method, url, headers, body)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	return res
}

func doHTTP(ctx context.Context, method, url string, headers map[string]string, body []byte) (HTTPResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rd)
	if err != nil {
		return HTTPResult{}, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	// No redirects: a test asserts on the first response.
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Do(req)
	if err != nil {
		return HTTPResult{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return HTTPResult{}, err
	}
	return HTTPResult{Status: resp.StatusCode, Header: resp.Header, Body: string(raw)}, nil
}

// BundleToken is the controller's Bundle API token.
func BundleToken(t *testing.T) string {
	t.Helper()
	tok := os.Getenv(EnvBundleToken)
	if tok == "" {
		t.Fatalf("%s is not set; the core suite runs hack/e2e/components/ciapi.sh", EnvBundleToken)
	}
	return tok
}

// ControllerURL is the chart controller's webhook port as the host reaches it.
func ControllerURL(t *testing.T) string {
	t.Helper()
	u := os.Getenv(EnvControllerURL)
	if u == "" {
		t.Fatalf("%s is not set; the core suite runs hack/e2e/components/ciapi.sh", EnvControllerURL)
	}
	return strings.TrimRight(u, "/")
}

// PostBundle sends body to base's POST /api/v1/bundles the way
// docs/ci-integration.md tells CI systems to: JSON with the token as a
// bearer. authorization "" sends the controller's token; "-" sends no
// Authorization header; anything else is sent verbatim.
func PostBundle(t *testing.T, base, authorization string, body []byte) HTTPResult {
	t.Helper()
	h := map[string]string{"Content-Type": "application/json"}
	switch authorization {
	case "":
		h["Authorization"] = "Bearer " + BundleToken(t)
	case "-":
	default:
		h["Authorization"] = authorization
	}
	res := HTTP(t, http.MethodPost, base+"/api/v1/bundles", h, body)
	t.Logf("POST %s/api/v1/bundles (%d bytes): HTTP %d %s", base, len(body), res.Status, clip(res.Body))
	return res
}

func clip(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 300 {
		return s[:300] + "..."
	}
	return s
}

// variants numbers the controller variants, so one test can run several.
var variants int32

// Variant is a second controller Deployment made by ControllerVariant.
type Variant struct {
	Name string
	// URL is its webhook port from the host; InClusterURL is the same port
	// through its Service, as a pod (or the git server) reaches it.
	URL, InClusterURL string
}

// ControllerVariant runs a second controller for the test ns: the chart's
// pod template with args appended and the env vars named in dropEnv removed,
// in ControllerNamespace, and a NodePort Service to its webhook port. The
// variant runs under its own ServiceAccount, bound only to the ClusterRoles of
// the chart's ServiceAccount and not to its leader election Role (see
// variantServiceAccount). With --leader-elect=true (the chart's setting,
// checked here) and no access to the Lease it stays a standby for good, even
// if the chart's controller misses a renewal: it reconciles nothing, but its
// webhook port (/webhook/scm, its health and the Bundle API) serves on every
// replica. That is how a test runs the controller with other flags without
// touching the shared one. The Deployment, Service, ServiceAccount and
// ClusterRoleBindings are deleted when the test ends.
func (e *Env) ControllerVariant(t *testing.T, ns string, args []string, dropEnv ...string) *Variant {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var chart appsv1.Deployment
	if err := e.Client.Get(ctx, types.NamespacedName{Namespace: ControllerNamespace, Name: ControllerDeployment}, &chart); err != nil {
		t.Fatalf("read the controller Deployment: %v", err)
	}
	tmpl := chart.Spec.Template.DeepCopy()
	c := &tmpl.Spec.Containers[0]
	if !contains(c.Args, "--leader-elect=true") {
		t.Fatalf("the controller runs without --leader-elect=true (%v); a variant would reconcile beside it", c.Args)
	}
	c.Args = append(c.Args, args...)
	drop := map[string]bool{}
	for _, n := range dropEnv {
		drop[n] = true
	}
	var env []corev1.EnvVar
	for _, v := range c.Env {
		if !drop[v.Name] {
			env = append(env, v)
		}
	}
	c.Env = env

	name := fmt.Sprintf("variant-%s-%d", ns[len(ns)-8:], atomic.AddInt32(&variants, 1))
	labels := map[string]string{"app.kubernetes.io/name": "kardinal-e2e-variant", "kardinal.io/e2e-variant": name}
	chartSA := tmpl.Spec.ServiceAccountName
	if chartSA == "" {
		chartSA = "default"
	}
	e.variantServiceAccount(t, name, chartSA, labels)
	tmpl.Spec.ServiceAccountName = name
	tmpl.Labels = labels
	tmpl.Annotations = nil
	one := int32(1)
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ControllerNamespace, Labels: labels},
		Spec: appsv1.DeploymentSpec{
			Replicas: &one,
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: *tmpl,
		},
	}
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ControllerNamespace, Labels: labels},
		Spec: corev1.ServiceSpec{
			Type:     corev1.ServiceTypeNodePort,
			Selector: labels,
			Ports:    []corev1.ServicePort{{Name: "webhook", Port: 8083, TargetPort: intstr.FromString("webhook")}},
		},
	}
	if err := e.Client.Create(ctx, dep); err != nil {
		t.Fatalf("create controller variant %s: %v", name, err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		for _, err := range []error{e.Client.Delete(ctx, dep), e.Client.Delete(ctx, svc)} {
			if err != nil && !apierrors.IsNotFound(err) {
				t.Errorf("delete controller variant %s: %v", name, err)
			}
		}
	})
	// Create fills in the allocated NodePort.
	if err := e.Client.Create(ctx, svc); err != nil {
		t.Fatalf("create Service %s: %v", name, err)
	}
	ip := e.nodeIP(t)
	v := &Variant{Name: name, URL: fmt.Sprintf("http://%s:%d", ip, svc.Spec.Ports[0].NodePort),
		InClusterURL: fmt.Sprintf("http://%s.%s.svc.cluster.local:8083", name, ControllerNamespace)}
	t.Logf("controller variant %s (args %v, without %v) at %s", name, args, dropEnv, v.URL)

	Eventually(t, 3*time.Minute, "controller variant "+name+" to serve /webhook/scm/health", func(ctx context.Context) (bool, string) {
		var d appsv1.Deployment
		if err := e.Client.Get(ctx, types.NamespacedName{Namespace: ControllerNamespace, Name: name}, &d); err != nil {
			return false, err.Error()
		}
		if d.Status.AvailableReplicas < 1 {
			return false, fmt.Sprintf("%d available", d.Status.AvailableReplicas)
		}
		res, err := doHTTP(ctx, http.MethodGet, v.URL+"/webhook/scm/health", nil, nil)
		if err != nil {
			return false, err.Error()
		}
		return res.Status == http.StatusOK, fmt.Sprintf("HTTP %d", res.Status)
	})
	return v
}

// nodeIP is the kind node's InternalIP, where NodePorts listen.
func (e *Env) nodeIP(t *testing.T) string {
	t.Helper()
	nodes, err := e.Kube.CoreV1().Nodes().List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list nodes: %v", err)
	}
	for _, n := range nodes.Items {
		for _, a := range n.Status.Addresses {
			if a.Type == corev1.NodeInternalIP {
				return a.Address
			}
		}
	}
	t.Fatal("no node has an InternalIP")
	return ""
}
