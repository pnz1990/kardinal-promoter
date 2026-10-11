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
	}{
		{name: "address", address: "http://ui.example/ui/", noOpen: true,
			want: "kardinal UI: http://ui.example/ui/\n"},
		{name: "KARDINAL_UI_URL", envURL: "http://env.example/ui/", noOpen: true,
			want: "kardinal UI: http://env.example/ui/\n"},
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
			err := dashboardFn(ctx, &out, tc.address, "kardinal-system", tc.noOpen)
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
