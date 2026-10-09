// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package hookrun

import (
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	psaapi "k8s.io/pod-security-admission/api"
	"k8s.io/pod-security-admission/policy"
)

// Pod Security levels a hook Pod is checked against (--hook-pod-security-level).
const (
	PodSecurityBaseline   = string(psaapi.LevelBaseline)
	PodSecurityRestricted = string(psaapi.LevelRestricted)
	PodSecurityPrivileged = string(psaapi.LevelPrivileged)
)

// DefaultPodSecurityLevel is the level when none is set.
const DefaultPodSecurityLevel = PodSecurityBaseline

// psaEvaluator is the upstream Pod Security Admission evaluator: the same
// checks the API server's PodSecurity admission runs, at the latest policy
// version.
var psaEvaluator = func() policy.Evaluator {
	e, err := policy.NewEvaluator(policy.DefaultChecks(), nil)
	if err != nil {
		panic(fmt.Sprintf("pod security evaluator: %v", err))
	}
	return e
}()

// ParsePodSecurityLevel validates a --hook-pod-security-level value; ""
// means DefaultPodSecurityLevel.
func ParsePodSecurityLevel(s string) (string, error) {
	if s == "" {
		return DefaultPodSecurityLevel, nil
	}
	l, err := psaapi.ParseLevel(s)
	if err != nil {
		return "", fmt.Errorf("pod security level %q: must be baseline, restricted or privileged", s)
	}
	return string(l), nil
}

// podSecurityViolation returns why pod breaks level, or "". On top of the
// Pod Security Standard, every level below privileged refuses nodeName (it
// bypasses the scheduler, so node selection and taints) and hostPort.
func podSecurityViolation(level string, labels map[string]string, pod *corev1.PodSpec) string {
	l, err := ParsePodSecurityLevel(level)
	if err != nil {
		return err.Error()
	}
	if l == PodSecurityPrivileged {
		return ""
	}
	var why []string
	res := policy.AggregateCheckResults(psaEvaluator.EvaluatePod(
		psaapi.LevelVersion{Level: psaapi.Level(l), Version: psaapi.LatestVersion()},
		&metav1.ObjectMeta{Labels: labels}, pod))
	if !res.Allowed {
		why = append(why, fmt.Sprintf("violates Pod Security %q (%s)", l, res.ForbiddenReason()))
		if d := res.ForbiddenDetail(); d != "" {
			why[len(why)-1] = fmt.Sprintf("violates Pod Security %q (%s: %s)", l, res.ForbiddenReason(), d)
		}
	}
	if pod.NodeName != "" {
		why = append(why, "sets nodeName")
	}
	containers := append(append([]corev1.Container(nil), pod.InitContainers...), pod.Containers...)
	for _, c := range containers {
		for _, p := range c.Ports {
			if p.HostPort != 0 {
				why = append(why, "uses a hostPort in container "+c.Name)
			}
		}
	}
	return strings.Join(why, "; ")
}
