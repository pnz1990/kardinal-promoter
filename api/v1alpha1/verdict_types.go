// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Verdict results.
const (
	VerdictPass         = "pass"
	VerdictFail         = "fail"
	VerdictInconclusive = "inconclusive"
)

// VerdictSpec is one verifier's result for one Bundle, environment and check
// (docs/design/19-verdicts.md). A Verdict is never changed: delete it to
// revoke it. The chart's <release>-verdicts ValidatingAdmissionPolicy admits
// it only when spec.by is the requesting user, spec.groups are among the
// requester's groups, the kardinal.io/bundle, kardinal.io/environment and
// kardinal.io/check labels match the spec, and spec.run is set only by the
// Pod of that AgentRun.
// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="a Verdict is immutable: delete it and create a new one"
// +kubebuilder:validation:XValidation:rule="self.result == 'pass' || size(self.summary) > 0",message="a fail or inconclusive verdict says why (spec.summary)"
type VerdictSpec struct {
	// Pipeline is the Bundle's Pipeline. The Graph counts the Verdict only
	// for a Bundle of this Pipeline.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Pipeline string `json:"pipeline"`

	// Bundle is the name of the Bundle verified, in the Verdict's namespace.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Bundle string `json:"bundle"`

	// BundleUID is the UID of that Bundle: the Graph counts the Verdict only
	// for the Bundle with this UID, so a Verdict cannot carry over to a new
	// Bundle that reuses the name.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	BundleUID string `json:"bundleUID"`

	// Environment is the environment verified: the version runs there.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	Environment string `json:"environment"`

	// Check is the verification's name (a gate's verify.check).
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Check string `json:"check"`

	// Result is pass, fail or inconclusive.
	// +kubebuilder:validation:Enum=pass;fail;inconclusive
	Result string `json:"result"`

	// Summary says what the verifier found. Required for fail and
	// inconclusive.
	// +kubebuilder:validation:MaxLength=1024
	// +kubebuilder:default=""
	// +optional
	Summary string `json:"summary"`

	// EvidenceURL links to the verifier's evidence (a CI run, a report).
	// +kubebuilder:validation:MaxLength=2048
	// +kubebuilder:validation:Pattern=`^https?://`
	// +optional
	EvidenceURL string `json:"evidenceURL,omitempty"`

	// Findings are what the verifier wants a reviewer to see.
	// +kubebuilder:validation:MaxItems=50
	// +optional
	Findings []VerdictFinding `json:"findings,omitempty"`

	// By is the Kubernetes username of the writer, as the API server
	// authenticates it.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	By string `json:"by"`

	// Groups are the writer's groups that count for verify.allowedGroups.
	// Each must be one of the requester's groups.
	// +kubebuilder:validation:MaxItems=64
	// +kubebuilder:validation:items:MaxLength=253
	// +optional
	Groups []string `json:"groups,omitempty"`

	// Actor is the actor stamp (docs/design/17-actors.md). Agents must set
	// it; the <release>-actor policy checks it.
	// +optional
	Actor *Actor `json:"actor,omitempty"`

	// Run binds the Verdict to the AgentRun whose Pod wrote it. Admission
	// admits it only from that run's Pod, and only when the Verdict is named
	// after the run.
	// +optional
	Run *VerdictRun `json:"run,omitempty"`

	// StartedAt is when the verifier says it started (asserted).
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`

	// DurationSeconds is how long the verifier says it ran (asserted).
	// +kubebuilder:validation:Minimum=0
	// +optional
	DurationSeconds int64 `json:"durationSeconds,omitempty"`
}

// VerdictFinding is one thing a verifier found.
type VerdictFinding struct {
	// Severity is info, low, medium, high or critical.
	// +kubebuilder:validation:Enum=info;low;medium;high;critical
	Severity string `json:"severity"`

	// Title is the finding in a line.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	Title string `json:"title"`

	// Detail explains it.
	// +kubebuilder:validation:MaxLength=2048
	// +optional
	Detail string `json:"detail,omitempty"`

	// URL links to it.
	// +kubebuilder:validation:MaxLength=2048
	// +kubebuilder:validation:Pattern=`^https?://`
	// +optional
	URL string `json:"url,omitempty"`
}

// VerdictRun names the AgentRun that wrote a Verdict.
type VerdictRun struct {
	// Name is the AgentRun's name.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=52
	Name string `json:"name"`

	// Index is the run's number among the check's runs (1-based).
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=10
	Index int `json:"index"`

	// Attempt is the attempt's number (1-based).
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=6
	Attempt int `json:"attempt"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced,shortName=vd,categories=kardinal-all
// +kubebuilder:printcolumn:name="Bundle",type=string,JSONPath=`.spec.bundle`
// +kubebuilder:printcolumn:name="Env",type=string,JSONPath=`.spec.environment`
// +kubebuilder:printcolumn:name="Check",type=string,JSONPath=`.spec.check`
// +kubebuilder:printcolumn:name="Result",type=string,JSONPath=`.spec.result`
// +kubebuilder:printcolumn:name="By",type=string,JSONPath=`.spec.by`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Verdict records that a verifier (an agent, a person or CI) found a Bundle
// to pass, fail or be inconclusive in an environment for a check. The
// promotion Graph copies the Verdicts of its Bundle into the verify gates
// (PolicyGate spec.verdicts), and the PolicyGate reconciler counts them
// against spec.verify.
type Verdict struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec VerdictSpec `json:"spec"`
}

// +kubebuilder:object:root=true

// VerdictList contains a list of Verdict.
type VerdictList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Verdict `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Verdict{}, &VerdictList{})
}
