// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// StepTemplateOutputVerdict is the output of a verifier: one Verdict.
const StepTemplateOutputVerdict = "Verdict"

// StepTemplateSpec registers an image kardinal may run as a step
// (docs/design/19-verdicts.md, section 8.2). Only cluster admins write
// StepTemplates. In this release a StepTemplate is a verifier: AgentRuns run
// it, and its Pod writes one Verdict.
type StepTemplateSpec struct {
	// Description says what the step does.
	// +kubebuilder:validation:MaxLength=1024
	// +optional
	Description string `json:"description,omitempty"`

	// Output is what the step produces. Only Verdict.
	// +kubebuilder:validation:Enum=Verdict
	// +kubebuilder:default=Verdict
	// +optional
	Output string `json:"output,omitempty"`

	// Image is the container image. A digest (image@sha256:...) is
	// recommended; the controller's --step-template-require-digest refuses
	// tags.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=1024
	Image string `json:"image"`

	// Command overrides the image's entrypoint.
	// +kubebuilder:validation:MaxItems=64
	// +optional
	Command []string `json:"command,omitempty"`

	// Args are the arguments.
	// +kubebuilder:validation:MaxItems=256
	// +optional
	Args []string `json:"args,omitempty"`

	// Env are literal environment variables. Names starting with KARDINAL_
	// are reserved for the run's context.
	// +kubebuilder:validation:MaxItems=64
	// +optional
	Env []StepTemplateEnv `json:"env,omitempty"`

	// SecretEnv are environment variables from Secrets in the Pipeline's
	// namespace. Each Secret must carry the label kardinal.io/step-template
	// with this template's name: a Secret not labelled for it (a Pipeline's
	// git credentials) is refused.
	// +kubebuilder:validation:MaxItems=16
	// +optional
	SecretEnv []StepTemplateSecretEnv `json:"secretEnv,omitempty"`

	// Agent is the AgentIdentity the step runs for: its verdicts count as
	// that agent's, and the one-party rule uses its sponsor. It must be
	// ready, not suspended, and its scope must cover the namespace, the
	// Pipeline and the environment of each run. The Pod does not run as
	// one of the agent's subjects: each AgentRun gets a ServiceAccount of
	// its own that kardinal creates, with only the chart's AgentRun roles
	// (create Verdicts; read the namespace under test), and deletes when the
	// run ends.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Agent string `json:"agent"`

	// Timeout bounds each attempt. Default 30m, at most 24h.
	// +kubebuilder:validation:Pattern=`^$|^(0|(([0-9]+(\.[0-9]*)?|\.[0-9]+)(ns|us|µs|μs|ms|s|m|h))+)$`
	// +optional
	Timeout string `json:"timeout,omitempty"`

	// Resources are the container's resource requests and limits.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`

	// Egress are the destinations the Pod may reach besides DNS, the
	// Kubernetes API server and the namespace under test: the model
	// endpoint, for example.
	// +kubebuilder:validation:MaxItems=16
	// +optional
	Egress []StepTemplateEgress `json:"egress,omitempty"`

	// RuntimeClassName runs the Pod in a sandboxed runtime (gVisor, Kata).
	// +kubebuilder:validation:MaxLength=253
	// +optional
	RuntimeClassName string `json:"runtimeClassName,omitempty"`
}

// StepTemplateEnv is a literal environment variable.
type StepTemplateEnv struct {
	// Name is the variable's name.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[A-Za-z_][A-Za-z0-9_]*$`
	Name string `json:"name"`
	// Value is its value.
	// +kubebuilder:validation:MaxLength=8192
	// +optional
	Value string `json:"value"`
}

// StepTemplateSecretEnv is an environment variable from a Secret key.
type StepTemplateSecretEnv struct {
	// Name is the variable's name.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[A-Za-z_][A-Za-z0-9_]*$`
	Name string `json:"name"`
	// Secret is the Secret's name, in the Pipeline's namespace.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Secret string `json:"secret"`
	// Key is the key in the Secret.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Key string `json:"key"`
}

// StepTemplateEgress is one destination a step's Pod may reach.
type StepTemplateEgress struct {
	// CIDR is the destination address range.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	CIDR string `json:"cidr"`
	// Ports are the TCP ports; empty: all.
	// +kubebuilder:validation:MaxItems=16
	// +optional
	Ports []int32 `json:"ports,omitempty"`
}

// StepTemplateStatus is the observed state of a StepTemplate.
type StepTemplateStatus struct {
	// Ready is true when the template is valid and AgentRuns may run it.
	// +optional
	Ready bool `json:"ready"`
	// Reason says why it is not ready.
	// +optional
	Reason string `json:"reason,omitempty"`
	// ObservedGeneration is the generation Ready is for.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=steptmpl,categories=kardinal;kardinal-all
// +kubebuilder:printcolumn:name="Output",type=string,JSONPath=`.spec.output`
// +kubebuilder:printcolumn:name="Image",type=string,JSONPath=`.spec.image`,priority=1
// +kubebuilder:printcolumn:name="Ready",type=boolean,JSONPath=`.status.ready`
// +kubebuilder:printcolumn:name="Reason",type=string,JSONPath=`.status.reason`,priority=1
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// StepTemplate registers an image kardinal may run as a step: in this
// release, a verifier whose AgentRuns write Verdicts. Cluster-scoped and
// written by cluster admins only.
type StepTemplate struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   StepTemplateSpec   `json:"spec,omitempty"`
	Status StepTemplateStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// StepTemplateList contains a list of StepTemplate.
type StepTemplateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []StepTemplate `json:"items"`
}

func init() {
	SchemeBuilder.Register(&StepTemplate{}, &StepTemplateList{})
}
