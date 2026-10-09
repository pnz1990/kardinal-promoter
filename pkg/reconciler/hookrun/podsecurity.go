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

// podSecurityViolation returns why pod (with the template's metadata, whose
// annotations carry AppArmor profiles on older Pods) breaks level, or "".
// Below privileged, nodeName is refused as well: it bypasses the scheduler,
// so node selection and taints. hostPort is a check of the standard itself.
func podSecurityViolation(level string, md *metav1.ObjectMeta, pod *corev1.PodSpec) string {
	l, err := ParsePodSecurityLevel(level)
	if err != nil {
		return err.Error()
	}
	if l == PodSecurityPrivileged {
		return ""
	}
	if md == nil {
		md = &metav1.ObjectMeta{}
	}
	var why []string
	res := policy.AggregateCheckResults(psaEvaluator.EvaluatePod(
		psaapi.LevelVersion{Level: psaapi.Level(l), Version: psaapi.LatestVersion()}, md, pod))
	if !res.Allowed {
		msg := res.ForbiddenReason()
		if d := res.ForbiddenDetail(); d != "" {
			msg += ": " + d
		}
		why = append(why, fmt.Sprintf("violates Pod Security %q (%s)", l, msg))
	}
	if pod.NodeName != "" {
		why = append(why, "sets nodeName")
	}
	return strings.Join(why, "; ")
}
