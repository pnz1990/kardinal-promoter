// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cmd

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestDashboard(t *testing.T) {
	cases := []struct {
		name     string
		address  string
		envURL   string
		forward  error
		noOpen   bool
		want     string
		wantErr  string
		forwards bool
		route    string
	}{
		{name: "address", address: "http://ui.example/ui/", noOpen: true,
			want: "kardinal UI: http://ui.example/ui/\n"},
		{name: "KARDINAL_UI_URL", envURL: "http://env.example/ui/", noOpen: true,
			want: "kardinal UI: http://env.example/ui/\n"},
		{name: "a pipeline's stage", address: "http://ui.example/ui/#old", noOpen: true, route: uiRoute("team-a", "my-app", "prod"),
			want: "kardinal UI: http://ui.example/ui/#/p/team-a/my-app/env/prod\n"},
		{name: "a pipeline, forwarded", noOpen: true, forwards: true, route: uiRoute("default", "demo", ""),
			want: "kardinal UI: http://127.0.0.1:8082/ui/#/p/default/demo\nForwarding to the controller in kardinal-system; Ctrl-C stops.\n"},
		{name: "port-forward", noOpen: true, forwards: true,
			want: "kardinal UI: http://127.0.0.1:8082/ui/\nForwarding to the controller in kardinal-system; Ctrl-C stops.\n"},
		{name: "no controller", forward: errors.New("no kardinal-promoter Service with a ui port in kardinal-system"),
			wantErr: "dashboard: no kardinal-promoter Service with a ui port in kardinal-system"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("KARDINAL_UI_URL", tc.envURL)
			old := forwardUI
			t.Cleanup(func() { forwardUI = old })
			forwarded := false
			forwardUI = func(ctx context.Context, ns string) (string, <-chan error, error) {
				forwarded = true
				assert.Equal(t, "kardinal-system", ns)
				if tc.forward != nil {
					return "", nil, tc.forward
				}
				done := make(chan error, 1)
				go func() { <-ctx.Done(); done <- nil }()
				return "http://127.0.0.1:8082/ui/", done, nil
			}
			ctx, cancel := context.WithCancel(context.Background())
			if tc.forwards {
				// Ctrl-C: the forward ends and dashboard returns.
				cancel()
			}
			defer cancel()
			var out bytes.Buffer
			err := dashboardFn(ctx, &out, tc.address, "kardinal-system", tc.noOpen, tc.route)
			if tc.wantErr != "" {
				require.EqualError(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, out.String())
			assert.Equal(t, tc.forwards, forwarded)
		})
	}
}

func TestFindUITarget(t *testing.T) {
	t.Parallel()
	name, port := uiPortName, int32(8082)
	yes, no := true, false
	svc := func(n string, ports ...string) *corev1.Service {
		s := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "kardinal-system", Name: n,
			Labels: map[string]string{"app.kubernetes.io/name": "kardinal-promoter"}}}
		for _, p := range ports {
			s.Spec.Ports = append(s.Spec.Ports, corev1.ServicePort{Name: p})
		}
		return s
	}
	slice := func(svcName string, eps ...discoveryv1.Endpoint) *discoveryv1.EndpointSlice {
		return &discoveryv1.EndpointSlice{
			ObjectMeta: metav1.ObjectMeta{Namespace: "kardinal-system", Name: svcName + "-x",
				Labels: map[string]string{discoveryv1.LabelServiceName: svcName}},
			Ports:     []discoveryv1.EndpointPort{{Name: &name, Port: &port}},
			Endpoints: eps,
		}
	}
	pod := func(n string, ready *bool) discoveryv1.Endpoint {
		return discoveryv1.Endpoint{Conditions: discoveryv1.EndpointConditions{Ready: ready},
			TargetRef: &corev1.ObjectReference{Kind: "Pod", Name: n}}
	}
	cases := []struct {
		name    string
		objs    []sigs_client.Object
		want    uiTarget
		wantErr string
	}{
		{name: "ready pod", objs: []sigs_client.Object{svc("kardinal-promoter", "metrics", "ui"),
			slice("kardinal-promoter", pod("old", &no), pod("kp-1", &yes))},
			want: uiTarget{Service: "kardinal-promoter", Pod: "kp-1", Port: 8082}},
		{name: "no ready pod", objs: []sigs_client.Object{svc("kardinal-promoter", "ui"), slice("kardinal-promoter", pod("old", &no))},
			wantErr: "the controller Service kardinal-system/kardinal-promoter has no ready Pod: kardinal doctor"},
		{name: "no ui port", objs: []sigs_client.Object{svc("kardinal-promoter-webhook", "webhook")},
			wantErr: "no kardinal-promoter Service with a ui port in kardinal-system"},
		{name: "not installed", wantErr: "no kardinal-promoter Service with a ui port in kardinal-system"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := fake.NewClientBuilder().WithScheme(rootScheme).WithObjects(tc.objs...).Build()
			got, err := findUITarget(context.Background(), c, "kardinal-system")
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestUILink (CLI UX 7): why and status print the UI page of what they show
// on stderr when the UI's address is known (KARDINAL_UI_URL), and nothing
// otherwise (a port-forward may not be running).
func TestUILink(t *testing.T) {
	c := policyClient(t, policyPipeline("demo", "test", "prod"))
	prev := newClient
	newClient = func() (sigs_client.Client, string, error) { return c, "default", nil }
	t.Cleanup(func() { newClient, globalOutput, globalNamespace = prev, "", "" })
	var stdout bytes.Buffer
	run := func(args ...string) string {
		var errb bytes.Buffer
		keepStyle(t)
		stdout.Reset()
		root := NewRootCmd()
		root.SetOut(&stdout)
		root.SetErr(&errb)
		root.SetArgs(args)
		// -w runs until its context ends.
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		require.NoError(t, root.ExecuteContext(ctx))
		return errb.String()
	}
	t.Setenv("KARDINAL_UI_URL", "")
	assert.NotContains(t, run("why", "demo", "prod"), "UI:")
	t.Setenv("KARDINAL_UI_URL", "https://kardinal.example.com/ui/")
	assert.Contains(t, run("why", "demo", "prod"), "UI: https://kardinal.example.com/ui/#/p/default/demo/env/prod\n")
	assert.Contains(t, run("status", "demo"), "UI: https://kardinal.example.com/ui/#/p/default/demo\n")
	assert.NotContains(t, run("status", "demo", "-o", "json"), "UI:", "not with -o json")
	for _, args := range [][]string{{"why", "demo", "prod", "-o", "json"}, {"why", "demo", "prod", "-w"}, {"status", "demo", "-w"}} {
		assert.NotContains(t, run(args...), "UI:", "not with %v", args)
		assert.NotContains(t, stdout.String(), "kardinal.example.com", "not with %v", args)
	}
	// Not a UI address: no link, a warning that says why.
	t.Setenv("KARDINAL_UI_URL", "javascript:alert(1)//")
	got := run("why", "demo", "prod")
	assert.NotContains(t, got, "UI:")
	assert.Contains(t, got, `warning: no UI link: KARDINAL_UI_URL "javascript:alert(1)//" is not an http(s) URL with a host`)
	assert.Empty(t, uiURL("default", "demo"), "-o json has no url for it")
	t.Setenv("KARDINAL_UI_URL", "")
	assert.Equal(t, "http://localhost:8082/ui/#/p/default/demo", uiURL("default", "demo"), "the one route form")
	assert.Equal(t, "#/p/team%20a/my-app/env/prod", uiRoute("team a", "my-app", "prod"))
}

func TestCheckUIAddress(t *testing.T) {
	t.Parallel()
	cases := []struct {
		address string
		ok      bool
	}{
		{"https://kardinal.example.com/ui/", true},
		{"http://127.0.0.1:8082/ui/", true},
		{"http://[::1]:8082/ui/#old", true},
		{"javascript:alert(1)//", false},
		{"file:///etc/passwd", false},
		{"/Applications/Calculator.app", false},
		{"-a Terminal", false},
		{"--help", false},
		{"https://", false},
		{"https:///ui/", false},
		{"kardinal.example.com/ui/", false},
		{"https://kardinal.example.com/ui/\x1b[2J", false},
		{"https://kardinal.example.com/ui/\nfake: line", false},
		{"https://kardinal.example.com/a b", false},
		{"https://kardinal.example.com/ui/\u202e/lmth.exe", false}, // a bidi override (format character)
		{"https://kardinal.exämple.com/ui/", false},                // not ASCII: punycode it
		{"https://admin@kardinal.example.com/ui/", false},          // userinfo
		{"https://kardinal.example.com@evil.example/ui/", false},   // userinfo that looks like a host
		{"https://xn--krdinal-cua.example/ui/", true},
	}
	for _, tc := range cases {
		t.Run(tc.address, func(t *testing.T) {
			err := checkUIAddress(tc.address)
			if tc.ok {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), "is not an http(s) URL with a host, in ASCII and without user@: set it to the UI's address, e.g. https://kardinal.example.com/ui/")
			assert.ErrorContains(t, openBrowser(tc.address), "open a browser:", "nothing unchecked reaches the opener")
		})
	}
}

// TestDashboard_RefusesABadAddress: dashboard refuses an --address or
// KARDINAL_UI_URL that is not a UI address, before it opens anything.
func TestDashboard_RefusesABadAddress(t *testing.T) {
	t.Setenv("KARDINAL_UI_URL", "")
	err := dashboardFn(context.Background(), &bytes.Buffer{}, "-a Terminal", "kardinal-system", false, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `dashboard: --address "-a Terminal" is not an http(s) URL with a host`)
	t.Setenv("KARDINAL_UI_URL", "file:///etc/passwd")
	var out bytes.Buffer
	err = dashboardFn(context.Background(), &out, "", "kardinal-system", false, "#/p/default/demo")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `dashboard: KARDINAL_UI_URL "file:///etc/passwd" is not an http(s) URL with a host`)
	assert.Empty(t, out.String(), "nothing printed or opened")
}
