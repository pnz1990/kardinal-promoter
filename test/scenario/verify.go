// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scenario

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apiserver/pkg/authentication/user"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/actor"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
	agentrunrec "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/agentrun"
)

// AgentResult is what one attempt of an in-cluster verifier does: the
// Verdict it writes (Result "": none), or a crash (the Job fails).
type AgentResult struct {
	Result, Summary string
	Findings        []v1alpha1.VerdictFinding
	Crash           bool
	// Wait leaves the Job running: the attempt has not ended yet.
	Wait bool
	// Edit changes the Verdict before it is written (an agent that writes
	// another Verdict than the one its run gave it).
	Edit func(verdict map[string]interface{})
}

// Agent is an in-cluster verifier's behaviour: what an attempt of an
// AgentRun does. The World's kubelet runs it for every Job of an AgentRun
// whose StepTemplate it is registered for (World.Agents).
type Agent func(run v1alpha1.AgentRun) AgentResult

// AgentVerdictRefusal is a Verdict an agent wrote that the API server refused.
type AgentVerdictRefusal struct {
	Run     string
	Reasons []string
}

// runAgent runs job, an AgentRun's Job, as its Pod would: it reads the run's
// context from the Pod template's annotation, writes the Verdict it holds
// with the agent's result, as the run's ServiceAccount from that very Pod
// (the token's pod-name extra), through admission, and completes the Job.
func (w *World) runAgent(job *batchv1.Job, owner *metav1.OwnerReference) error {
	var run v1alpha1.AgentRun
	if err := w.Client.Get(w.Ctx, client.ObjectKey{Namespace: job.Namespace, Name: owner.Name}, &run); err != nil {
		return client.IgnoreNotFound(err)
	}
	agent := w.Agents[run.Spec.Template]
	res := AgentResult{Result: v1alpha1.VerdictPass, Summary: "it works"}
	if agent != nil {
		res = agent(run)
	}
	if res.Wait {
		return nil
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: job.Namespace, Name: job.Name + "-" + podSuffix(job.Name),
		Labels: map[string]string{"batch.kubernetes.io/controller-uid": string(job.UID), "job-name": job.Name},
		OwnerReferences: []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: job.Name, UID: job.UID,
			Controller: boolPtr(true)}}}}
	if err := w.Client.Create(w.Ctx, pod); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create agent pod: %w", err)
	}
	if res.Result != "" && !res.Crash {
		var rc struct {
			Verdict map[string]interface{} `json:"verdict"`
		}
		if err := json.Unmarshal([]byte(job.Spec.Template.Annotations[agentrunrec.AnnotationContext]), &rc); err != nil {
			return fmt.Errorf("agent %s: the run's context: %w", run.Name, err)
		}
		spec, _ := rc.Verdict["spec"].(map[string]interface{})
		if spec == nil {
			return fmt.Errorf("agent %s: the context has no Verdict to write", run.Name)
		}
		spec["result"], spec["summary"] = res.Result, res.Summary
		if res.Edit != nil {
			res.Edit(rc.Verdict)
		}
		if len(res.Findings) > 0 {
			raw, err := json.Marshal(res.Findings)
			if err != nil {
				return err
			}
			var f []interface{}
			if err := json.Unmarshal(raw, &f); err != nil {
				return err
			}
			spec["findings"] = f
		}
		var v v1alpha1.Verdict
		if err := k8sruntime.DefaultUnstructuredConverter.FromUnstructured(rc.Verdict, &v); err != nil {
			return fmt.Errorf("agent %s: the Verdict: %w", run.Name, err)
		}
		as := &user.DefaultInfo{Name: v.Spec.By, Groups: ServiceAccountGroups(job.Namespace),
			Extra: map[string][]string{
				"authentication.kubernetes.io/credential-id": {"JTI=" + pod.Name},
				"authentication.kubernetes.io/pod-name":      {pod.Name},
			}}
		reasons, err := w.ApplyAs(AsInfo(w.Ctx, as), &v)
		if err != nil && !apierrors.IsAlreadyExists(err) && !strings.Contains(err.Error(), "already exists") {
			return fmt.Errorf("agent %s: write its Verdict: %w", run.Name, err)
		}
		if len(reasons) > 0 {
			w.mu.Lock()
			w.agentRefusals = append(w.agentRefusals, AgentVerdictRefusal{Run: run.Name, Reasons: reasons})
			w.mu.Unlock()
		}
	}
	cond := batchv1.JobComplete
	exit := int32(0)
	if res.Crash {
		cond, exit = batchv1.JobFailed, 1
	}
	pod.Status = corev1.PodStatus{Phase: corev1.PodSucceeded, ContainerStatuses: []corev1.ContainerStatus{{Name: "agent",
		State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: exit, FinishedAt: metav1.NewTime(w.Now())}}}}}
	if res.Crash {
		pod.Status.Phase = corev1.PodFailed
	}
	if err := w.Client.Status().Update(w.Ctx, pod); err != nil {
		return fmt.Errorf("agent pod status: %w", err)
	}
	job.Status.Conditions = append(job.Status.Conditions, batchv1.JobCondition{Type: cond, Status: corev1.ConditionTrue,
		LastTransitionTime: metav1.NewTime(w.Now())})
	if err := w.Client.Status().Update(w.Ctx, job); err != nil {
		return fmt.Errorf("agent job status: %w", err)
	}
	return nil
}

// podSuffix is the five-character suffix the Job controller gives a Pod,
// derived from the Job's name so that it is stable.
func podSuffix(job string) string {
	const chars = "bcdfghjklmnpqrstvwxz2456789"
	h := uint32(2166136261)
	for i := 0; i < len(job); i++ {
		h = (h ^ uint32(job[i])) * 16777619
	}
	out := make([]byte, 5)
	for i := range out {
		out[i] = chars[h%uint32(len(chars))]
		h /= uint32(len(chars))
		h = h*2654435761 + 1
	}
	return string(out)
}

// AgentVerdictRefusals are the Verdicts agents wrote that the API server
// refused.
func (w *World) AgentVerdictRefusals() []AgentVerdictRefusal {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]AgentVerdictRefusal(nil), w.agentRefusals...)
}

// The verify gate of the matrix: check VerifyCheck, run by agent QAAgent
// (sponsor QASponsor) from StepTemplate QAAgent, and QALead, a person who
// may verify too.
const (
	VerifyCheck = "explore"
	QAAgent     = "qa-explorer"
	QASponsor   = "carol@corp.example.com"
	QALead      = "qa-lead@corp.example.com"
)

// QATemplate is the StepTemplate of QAAgent.
func QATemplate() *v1alpha1.StepTemplate {
	return &v1alpha1.StepTemplate{ObjectMeta: metav1.ObjectMeta{Name: QAAgent},
		Spec: v1alpha1.StepTemplateSpec{Image: "ghcr.io/acme/qa-explorer@sha256:" + strings.Repeat("ab", 32), Agent: QAAgent, Timeout: "30m"}}
}

// VerifyAll records who's result on every verify gate instance that waits,
// for bundles (all when empty), as kardinal verify does: as who, with their
// actor stamp, on the gate's verified environment and check.
func (w *World) VerifyAll(who, result string, bundles ...string) error {
	var gates v1alpha1.PolicyGateList
	if err := w.Client.List(w.Ctx, &gates); err != nil {
		return fmt.Errorf("list gates: %w", err)
	}
	want := map[string]bool{}
	for _, b := range bundles {
		want[b] = true
	}
	seen := map[string]bool{}
	id := Identity{Username: who, Groups: []string{"system:authenticated"}}
	for _, g := range gates.Items {
		b := g.Labels["kardinal.io/bundle"]
		if g.Spec.Verify == nil || b == "" || (len(want) > 0 && !want[b]) || g.Status.Ready {
			continue
		}
		key := g.Namespace + "/" + b + "/" + g.Spec.Verify.Environment + "/" + g.Spec.Verify.Check
		if seen[key] {
			continue
		}
		seen[key] = true
		if err := w.RecordVerdict(id, g.Namespace, b, g.Spec.Verify.Environment, g.Spec.Verify.Check, result, nil); err != nil {
			return fmt.Errorf("verify %s: %w", key, err)
		}
	}
	return nil
}

// RecordVerdict writes id's Verdict on bundle as kardinal verify does (its
// actor stamp from the index; mutate, when set, changes it before it is
// written), through admission; a refusal is an error.
func (w *World) RecordVerdict(id Identity, ns, bundle, env, check, result string, mutate func(*v1alpha1.Verdict)) error {
	var b v1alpha1.Bundle
	if err := w.Client.Get(w.Ctx, client.ObjectKey{Namespace: ns, Name: bundle}, &b); err != nil {
		return fmt.Errorf("get bundle %s: %w", bundle, err)
	}
	stamp, err := w.StampFor(id, actor.Where{Namespace: ns, Pipeline: b.Spec.Pipeline, Environment: env})
	if err != nil {
		return err
	}
	v := &v1alpha1.Verdict{ObjectMeta: metav1.ObjectMeta{Namespace: ns,
		Name: lifecycle.VerdictName(bundle, env, check, id.Username, w.Now().Add(time.Duration(w.uids.Add(1)))),
		Labels: map[string]string{"kardinal.io/bundle": bundle, "kardinal.io/environment": env, "kardinal.io/check": check,
			"kardinal.io/pipeline": b.Spec.Pipeline},
		OwnerReferences: []metav1.OwnerReference{{APIVersion: v1alpha1.GroupVersion.String(), Kind: "Bundle", Name: b.Name, UID: b.UID}}},
		Spec: v1alpha1.VerdictSpec{Pipeline: b.Spec.Pipeline, Bundle: b.Name, BundleUID: string(b.UID), Environment: env,
			Check: check, Result: result, Summary: result + " by " + id.Username, By: id.Username, Groups: id.Groups, Actor: stamp}}
	if mutate != nil {
		mutate(v)
	}
	reasons, err := w.ApplyAs(w.CtxOf(id), v)
	if err != nil {
		return err
	}
	if len(reasons) > 0 {
		return fmt.Errorf("verdict refused: %s", strings.Join(reasons, "; "))
	}
	return nil
}

// checkVerified checks the evidence of a version the matrix's verify gate
// passed: the agent's run voted (its Verdict counted, as QAAgent for
// QASponsor), a VerdictRecorded AuditEvent names that agent, and a PR of the
// gated environment has the Verification section.
func (p *Plan) checkVerified(t *testing.T, w *World, bundle string) {
	t.Helper()
	counted := false
	for _, g := range w.GateInstances(p.NS, bundle) {
		for _, v := range g.Status.Verdicts {
			if v.Counted && v.Run > 0 && strings.Contains(v.Verifier, QAAgent) && strings.Contains(v.Verifier, QASponsor) {
				counted = true
			}
		}
	}
	if !counted {
		t.Errorf("no gate of %s counted a run of agent %s for %s", bundle, QAAgent, QASponsor)
	}
	var events v1alpha1.AuditEventList
	if err := w.Client.List(w.Ctx, &events, client.InNamespace(p.NS)); err != nil {
		t.Errorf("list audit events: %v", err)
		return
	}
	recorded := false
	for _, e := range events.Items {
		if e.Spec.Action == "VerdictRecorded" && e.Spec.BundleName == bundle && e.Spec.Actor != nil &&
			e.Spec.Actor.Agent == QAAgent && e.Spec.Actor.Sponsor == QASponsor {
			recorded = true
		}
	}
	if !recorded {
		t.Errorf("no VerdictRecorded AuditEvent of %s names agent %s for %s", bundle, QAAgent, QASponsor)
	}
	if p.Delivery != "pr-review" {
		return
	}
	found := false
	for _, pr := range w.SCM.PRs() {
		if strings.Contains(pr.Body, "### Verification: "+VerifyCheck+" in ") && strings.Contains(pr.Body, bundle) {
			found = true
		}
	}
	if !found {
		t.Errorf("no PR of %s has a Verification section", bundle)
	}
}
