// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package health

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

// newPodProblem describes why a new pod of Deployment d is not ready (for
// example `container app is waiting: ErrImagePull: ...`), or returns "" when
// it cannot tell: no new pod is unready, or the pods cannot be read.
type newPodProblem func(d *appsv1.Deployment) string

var podGVR = schema.GroupVersionResource{Version: "v1", Resource: "pods"}

// progressingReplicaSet matches the messages of the Progressing condition
// the Deployment controller sets, each of which names the Deployment's new
// ReplicaSet: "Created new replica set" and "Found new replica set"
// (pkg/controller/deployment/sync.go), which stay until the rollout makes
// progress, so a new pod that never starts keeps the first one; "is
// progressing", "has successfully progressed" and "has timed out
// progressing" (progress.go and util/deployment_util.go).
var progressingReplicaSet = regexp.MustCompile(`^(?:(?:Created|Found) new replica set "([a-z0-9.-]+)"|ReplicaSet "([a-z0-9.-]+)" (?:is progressing|has successfully progressed|has timed out progressing)\.)$`)

// maxPodMessage bounds the container message quoted in a health reason.
const maxPodMessage = 200

// podProblemLookup reads the pods of a Deployment's new ReplicaSet through
// dyn, uncached, or is nil without dyn. It runs only while replicas are
// unavailable, so it does not need an informer on every pod. The new
// ReplicaSet is the one the Progressing condition names; its pods carry its
// pod-template-hash label (the ReplicaSet name is <deployment>-<hash>).
func podProblemLookup(ctx context.Context, dyn dynamic.Interface) newPodProblem {
	if dyn == nil {
		return nil
	}
	return func(d *appsv1.Deployment) string {
		prog := deploymentCondition(d, string(appsv1.DeploymentProgressing))
		if prog == nil || d.Spec.Selector == nil {
			return ""
		}
		m := progressingReplicaSet.FindStringSubmatch(prog.Message)
		if m == nil {
			return ""
		}
		rs := m[1] + m[2]
		if !strings.HasPrefix(rs, d.Name+"-") {
			return ""
		}
		hash := strings.TrimPrefix(rs, d.Name+"-")
		selector, err := metav1.LabelSelectorAsSelector(d.Spec.Selector)
		if err != nil {
			return ""
		}
		list, err := dyn.Resource(podGVR).Namespace(d.Namespace).List(ctx, metav1.ListOptions{
			LabelSelector: selector.String() + ",pod-template-hash=" + hash,
			Limit:         50,
		})
		if err != nil {
			// A chart without list on pods, say: the reason stays without it.
			return ""
		}
		pods := make([]corev1.Pod, 0, len(list.Items))
		for i := range list.Items {
			var p corev1.Pod
			if runtime.DefaultUnstructuredConverter.FromUnstructured(list.Items[i].Object, &p) == nil {
				pods = append(pods, p)
			}
		}
		return unreadyPod(pods)
	}
}

// unreadyPod describes the first pod (by name) that has a container that is
// not ready, naming the container's waiting or terminated reason, or a
// running container that fails its readiness probe. Init containers count
// first: a pod does not start its containers before they complete.
func unreadyPod(pods []corev1.Pod) string {
	sort.Slice(pods, func(i, j int) bool { return pods[i].Name < pods[j].Name })
	for i := range pods {
		p := &pods[i]
		if p.DeletionTimestamp != nil {
			continue
		}
		for _, statuses := range [][]corev1.ContainerStatus{p.Status.InitContainerStatuses, p.Status.ContainerStatuses} {
			for _, cs := range statuses {
				if why := containerProblem(cs); why != "" {
					return fmt.Sprintf("new pod %s: container %s %s", p.Name, cs.Name, why)
				}
			}
		}
		if p.Status.Phase == corev1.PodPending && len(p.Status.ContainerStatuses) == 0 {
			for _, c := range p.Status.Conditions {
				if c.Type == corev1.PodScheduled && c.Status == corev1.ConditionFalse {
					return fmt.Sprintf("new pod %s is not scheduled: %s", p.Name, trimMessage(c.Reason, c.Message))
				}
			}
		}
	}
	return ""
}

// containerProblem says why a container is not ready, or "" when it is (or
// is an init container that completed).
func containerProblem(cs corev1.ContainerStatus) string {
	switch {
	case cs.State.Waiting != nil && cs.State.Waiting.Reason != "":
		return "is waiting: " + trimMessage(cs.State.Waiting.Reason, cs.State.Waiting.Message)
	case cs.State.Terminated != nil && cs.State.Terminated.ExitCode != 0:
		return fmt.Sprintf("terminated with exit code %d: %s", cs.State.Terminated.ExitCode,
			trimMessage(cs.State.Terminated.Reason, cs.State.Terminated.Message))
	case cs.State.Running != nil && !cs.Ready:
		return "is running but not ready (readiness probe)"
	}
	return ""
}

func trimMessage(reason, message string) string {
	message = strings.Join(strings.Fields(message), " ")
	if len(message) > maxPodMessage {
		// Cut on a rune boundary: the reason is written to a status field,
		// which must be valid UTF-8.
		cut := maxPodMessage
		for cut > 0 && !utf8.RuneStart(message[cut]) {
			cut--
		}
		message = message[:cut] + "..."
	}
	if message == "" {
		return reason
	}
	return reason + ": " + message
}

// withPodProblem adds why a new pod is not ready to the reason of a
// Deployment check that is not Healthy while replicas are unavailable: a
// rolling update whose new pod never starts reads as old replicas that stay,
// and a health.timeout message should name the pod's failure (#1365).
func withPodProblem(st HealthStatus, d *appsv1.Deployment, pods newPodProblem) HealthStatus {
	if st.Healthy || pods == nil || d.Status.UnavailableReplicas == 0 && d.Status.AvailableReplicas >= d.Status.UpdatedReplicas {
		return st
	}
	if why := pods(d); why != "" {
		st.Reason += "; " + why
	}
	return st
}
