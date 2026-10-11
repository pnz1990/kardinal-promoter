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
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
	"k8s.io/streaming/pkg/httpstream"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"
)

// uiPortName is the name of the controller Service's UI port (the chart's
// service.yaml).
const uiPortName = "ui"

// uiTarget is where kardinal dashboard forwards to: a ready controller Pod
// behind a Service with a ui port, and that port on the Pod.
type uiTarget struct {
	Service, Pod string
	Port         int32
}

// findUITarget finds, in ns, the Service of the kardinal-promoter chart with
// a ui port, and a ready Pod behind it, from its EndpointSlices.
func findUITarget(ctx context.Context, c sigs_client.Client, ns string) (uiTarget, error) {
	var svcs corev1.ServiceList
	if err := c.List(ctx, &svcs, sigs_client.InNamespace(ns),
		sigs_client.MatchingLabels{"app.kubernetes.io/name": "kardinal-promoter"}); err != nil {
		return uiTarget{}, fmt.Errorf("list Services in %s: %w", ns, HumanError(err))
	}
	for _, svc := range svcs.Items {
		hasUI := false
		for _, p := range svc.Spec.Ports {
			hasUI = hasUI || p.Name == uiPortName
		}
		if !hasUI {
			continue
		}
		var slices discoveryv1.EndpointSliceList
		if err := c.List(ctx, &slices, sigs_client.InNamespace(ns),
			sigs_client.MatchingLabels{discoveryv1.LabelServiceName: svc.Name}); err != nil {
			return uiTarget{}, fmt.Errorf("list the endpoints of Service %s/%s: %w", ns, svc.Name, HumanError(err))
		}
		for _, s := range slices.Items {
			var port int32
			for _, p := range s.Ports {
				if p.Name != nil && *p.Name == uiPortName && p.Port != nil {
					port = *p.Port
				}
			}
			if port == 0 {
				continue
			}
			for _, ep := range s.Endpoints {
				ready := ep.Conditions.Ready == nil || *ep.Conditions.Ready
				if ready && ep.TargetRef != nil && ep.TargetRef.Kind == "Pod" {
					return uiTarget{Service: svc.Name, Pod: ep.TargetRef.Name, Port: port}, nil
				}
			}
		}
		return uiTarget{}, fmt.Errorf("the controller Service %s/%s has no ready Pod: kardinal doctor", ns, svc.Name)
	}
	return uiTarget{}, fmt.Errorf("no kardinal-promoter Service with a %s port in %s: is the controller installed there? "+
		"(--controller-namespace for another namespace, --address for a UI you reach another way)", uiPortName, ns)
}

// localUIPort is the local port kardinal dashboard listens on: the UI's own
// port 8082 when it is free, else one the system picks (0).
func localUIPort() int {
	l, err := net.Listen("tcp", "127.0.0.1:8082")
	if err != nil {
		return 0
	}
	_ = l.Close()
	return 8082
}

// forwardUI forwards a local port on 127.0.0.1 to the UI port of a
// controller Pod in ns until ctx is done, and returns the UI's URL once the
// forward is ready, and a channel closed when it ends (with its error).
// A test replaces it.
var forwardUI = func(ctx context.Context, ns string) (string, <-chan error, error) {
	c, _, err := buildClient()
	if err != nil {
		return "", nil, err
	}
	target, err := findUITarget(ctx, c, ns)
	if err != nil {
		return "", nil, err
	}
	cfg, _, err := buildRestConfig()
	if err != nil {
		return "", nil, err
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return "", nil, fmt.Errorf("kubernetes client: %w", err)
	}
	req := cs.CoreV1().RESTClient().Post().Resource("pods").Namespace(ns).Name(target.Pod).SubResource("portforward")
	transport, upgrader, err := spdy.RoundTripperFor(cfg)
	if err != nil {
		return "", nil, fmt.Errorf("port-forward: %w", err)
	}
	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, http.MethodPost, req.URL())
	// As kubectl does: WebSockets first, SPDY when the API server cannot
	// upgrade to them.
	if ws, err := portforward.NewSPDYOverWebsocketDialer(req.URL(), cfg); err == nil {
		dialer = portforward.NewFallbackDialer(ws, dialer, func(err error) bool {
			return httpstream.IsUpgradeFailure(err) || httpstream.IsHTTPSProxyError(err)
		})
	}
	ready := make(chan struct{})
	ports := []string{strconv.Itoa(localUIPort()) + ":" + strconv.Itoa(int(target.Port))}
	fw, err := portforward.NewOnAddressesWithContext(ctx, dialer, []string{"127.0.0.1"}, ports, ready, io.Discard, io.Discard)
	if err != nil {
		return "", nil, fmt.Errorf("port-forward to %s/%s: %w", ns, target.Pod, err)
	}
	done := make(chan error, 1)
	go func() { done <- fw.ForwardPorts() }()
	select {
	case <-ready:
	case err := <-done:
		if err == nil {
			err = ctx.Err()
		}
		return "", nil, fmt.Errorf("port-forward to %s/%s: %w", ns, target.Pod, err)
	case <-ctx.Done():
		return "", nil, ctx.Err()
	}
	got, err := fw.GetPorts()
	if err != nil || len(got) == 0 {
		return "", nil, fmt.Errorf("port-forward to %s/%s: no local port: %w", ns, target.Pod, err)
	}
	u := url.URL{Scheme: "http", Host: net.JoinHostPort("localhost", strconv.Itoa(int(got[0].Local))), Path: "/ui/"}
	return u.String(), done, nil
}
