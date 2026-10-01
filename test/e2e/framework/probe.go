// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package framework

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
)

// probeImage is the podinfo release the fixtures deploy first, which the
// chart and multi-cluster suites pull onto their nodes
// (hack/e2e/components/podinfo.sh, PODINFO_IMAGE in hack/e2e/versions.env).
// It ships curl, nc and a shell, so a probe Pod can make in-cluster requests.
const probeImage = fixtures.Image + ":" + fixtures.V1

// Probe is a long-running Pod tests exec curl in, to reach the controller
// from inside the cluster the way another pod (or an Ingress) would.
type Probe struct {
	Namespace string
	Name      string
	IP        string
	e         *Env
}

// Probe starts a Pod named name in ns with labels and waits until it runs.
// opts change the Pod before it is created (for example, to mount a Secret).
// The Pod is deleted with the namespace.
func (e *Env) Probe(t *testing.T, ns, name string, labels map[string]string, opts ...func(*corev1.Pod)) *Probe {
	t.Helper()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: labels},
		Spec: corev1.PodSpec{
			TerminationGracePeriodSeconds: new(int64),
			Containers: []corev1.Container{{
				Name:            "probe",
				Image:           probeImage,
				ImagePullPolicy: corev1.PullIfNotPresent,
				Command:         []string{"sleep", "86400"},
			}},
		},
	}
	for _, opt := range opts {
		opt(pod)
	}
	ctx := context.Background()
	if err := e.Client.Create(ctx, pod); err != nil {
		t.Fatalf("create probe Pod %s/%s: %v", ns, name, err)
	}
	p := &Probe{Namespace: ns, Name: name, e: e}
	Eventually(t, 2*time.Minute, "probe Pod "+name+" running", func(ctx context.Context) (bool, string) {
		var got corev1.Pod
		if err := e.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &got); err != nil {
			return false, err.Error()
		}
		p.IP = got.Status.PodIP
		return got.Status.Phase == corev1.PodRunning && p.IP != "", string(got.Status.Phase)
	})
	return p
}

// Exec runs argv in the probe Pod and returns stdout+stderr. It does not
// fail the test.
func (p *Probe) Exec(t *testing.T, argv ...string) (string, error) {
	t.Helper()
	args := append([]string{"--context", p.e.Context, "-n", p.Namespace, "exec", p.Name, "--"}, argv...)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "kubectl", args...).CombinedOutput()
	return string(out), err
}

// CurlResult is one curl request's outcome.
type CurlResult struct {
	// Code is the HTTP status, 0 when no response came (refused, timed out).
	Code int
	Body string
	// Headers are the response headers, lower-cased names.
	Headers map[string]string
	// Err is curl's error when there was no response.
	Err string
}

var curlStatus = regexp.MustCompile(`(?m)^HTTP/[0-9.]+ ([0-9]{3})`)

// Curl runs curl in the probe Pod with args (method, headers, URL...) and a
// connect timeout of connectTimeout. It never fails the test: a request that
// gets no response has Code 0.
func (p *Probe) Curl(t *testing.T, connectTimeout time.Duration, args ...string) CurlResult {
	t.Helper()
	argv := append([]string{"curl", "-sS", "-i", "--connect-timeout", fmt.Sprint(int(connectTimeout.Seconds())),
		"--max-time", fmt.Sprint(int(connectTimeout.Seconds()) + 10)}, args...)
	out, err := p.Exec(t, argv...)
	res := CurlResult{Headers: map[string]string{}}
	head, body, _ := strings.Cut(strings.ReplaceAll(out, "\r\n", "\n"), "\n\n")
	// curl -i prints the last response; a 100 Continue comes first.
	for strings.HasPrefix(body, "HTTP/") {
		head, body, _ = strings.Cut(body, "\n\n")
	}
	if m := curlStatus.FindStringSubmatch(head); m != nil {
		res.Code, _ = strconv.Atoi(m[1])
		for _, line := range strings.Split(head, "\n")[1:] {
			if k, v, ok := strings.Cut(line, ":"); ok {
				res.Headers[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
			}
		}
		res.Body = body
	} else if err != nil {
		res.Err = strings.TrimSpace(out)
	}
	t.Logf("probe %s: curl %s -> %d %s", p.Name, strings.Join(redactArgs(args), " "), res.Code, firstLine(res.Body+res.Err))
	return res
}

// redactArgs hides Authorization header values in logged curl args.
func redactArgs(args []string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		if strings.HasPrefix(strings.ToLower(a), "authorization:") {
			a = "Authorization: <redacted>"
		}
		out[i] = a
	}
	return out
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	if len(s) > 200 {
		return s[:200]
	}
	return s
}
