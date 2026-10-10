// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// Hook phases: when a hook runs relative to the environment's promotion.
const (
	// HookPhasePre runs before the PromotionStep starts (database migrations).
	HookPhasePre = "pre"
	// HookPhasePost runs after the environment passed its health check and
	// before the PromotionStep is Verified (integration tests).
	HookPhasePost = "post"
)

// HookRun phases. Succeeded, Failed and Skipped are terminal and never
// change.
const (
	HookRunPending   = "Pending"
	HookRunRunning   = "Running"
	HookRunSucceeded = "Succeeded"
	HookRunFailed    = "Failed"
	// HookRunSkipped: the hook was added to the Pipeline after its step had
	// passed the point it runs at (a pre hook once the step started); it is
	// not run.
	HookRunSkipped = "Skipped"
)

// HookRunSkippedAddedLate starts the status.message of a HookRun Skipped
// because the hook was added to the Pipeline after its step passed the point
// it runs at. A HookRun is also Skipped, with another message, when its
// Bundle was superseded or rejected before the Job started.
const HookRunSkippedAddedLate = "not run: the hook was added to the Pipeline after "

// HookSpec is one pre- or post-deploy hook of a Pipeline environment: a
// Kubernetes Job the controller runs once per Bundle and environment.
type HookSpec struct {
	// Name identifies the hook within its environment and phase. It is part of
	// the HookRun and Job names.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=40
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Name string `json:"name"`

	// Phase is when the hook runs. "pre": after the environment's upstreams
	// are Verified and its gates are ready, before the promotion starts; the
	// promotion starts only when every pre hook succeeded. "post": after the
	// health check passed; the environment is Verified only when every post
	// hook succeeded, and a failed post hook applies onHealthFailure. Hooks of
	// one phase run one after another, in list order.
	// +kubebuilder:validation:Enum=pre;post
	Phase string `json:"phase"`

	// Job is a batch/v1 JobSpec. The controller creates the Job in the
	// Pipeline namespace with the HookRun as its owner. The Pod's
	// serviceAccountName (default "default") must be in the controller's
	// --hook-service-accounts list. restartPolicy defaults to Never, and
	// activeDeadlineSeconds to the timeout.
	// +kubebuilder:validation:Schemaless
	// +kubebuilder:validation:Type=object
	// +kubebuilder:pruning:PreserveUnknownFields
	Job runtime.RawExtension `json:"job"`

	// Timeout bounds the hook from the moment its HookRun starts: a hook that
	// has not finished by then fails and its Job is deleted. Empty or "0" means 30m.
	// +kubebuilder:validation:Pattern=`^$|^(0|(([0-9]+(\.[0-9]*)?|\.[0-9]+)(ns|us|µs|μs|ms|s|m|h))+)$`
	// +optional
	Timeout string `json:"timeout,omitempty"`
}

// HookRunSpec is one execution of a Pipeline hook for one Bundle and
// environment. The kro Graph of the Bundle creates it; the HookRun
// reconciler runs the Job.
type HookRunSpec struct {
	// PipelineName is the Pipeline the hook belongs to.
	// +kubebuilder:validation:MinLength=1
	PipelineName string `json:"pipelineName"`

	// BundleName is the Bundle being promoted.
	// +kubebuilder:validation:MinLength=1
	BundleName string `json:"bundleName"`

	// Environment is the environment the hook runs for.
	// +kubebuilder:validation:MinLength=1
	Environment string `json:"environment"`

	// Hook is the hook's name in the Pipeline.
	// +kubebuilder:validation:MinLength=1
	Hook string `json:"hook"`

	// Phase is "pre" or "post" (see HookSpec.Phase).
	// +kubebuilder:validation:Enum=pre;post
	Phase string `json:"phase"`

	// Job is the batch/v1 JobSpec to run (see HookSpec.Job).
	// +kubebuilder:validation:Schemaless
	// +kubebuilder:validation:Type=object
	// +kubebuilder:pruning:PreserveUnknownFields
	Job runtime.RawExtension `json:"job"`

	// Timeout is the hook timeout (see HookSpec.Timeout).
	// +kubebuilder:validation:Pattern=`^$|^(0|(([0-9]+(\.[0-9]*)?|\.[0-9]+)(ns|us|µs|μs|ms|s|m|h))+)$`
	// +optional
	Timeout string `json:"timeout,omitempty"`

	// StepAdvanced is set by the Graph from the step's state: true once the
	// step passed the point this hook runs at (started, for a pre hook;
	// finished, for a post hook). A HookRun that starts with it set is
	// Skipped: a hook added to the Pipeline too late for this Bundle does not
	// run out of order. It is not part of the spec hash.
	// +optional
	StepAdvanced bool `json:"stepAdvanced,omitempty"`

	// Recorded is set by the Graph from the step's status.hookRecords: the
	// result the step recorded for this hook, when one ran before. A HookRun
	// that starts with a record of its own spec hash does not run the Job
	// again: it takes the recorded result (Succeeded or Failed), or Failed
	// when the earlier run was deleted while it ran (result unknown). It is
	// not part of the spec hash.
	// +optional
	Recorded HookRunRecorded `json:"recorded,omitempty"`
}

// HookRunRecorded is the step's record of an earlier run of the hook, as
// strings ("" when there is none): kro renders each from the step's
// status.hookRecords.
type HookRunRecorded struct {
	// SpecHash is the spec hash of the recorded run.
	// +optional
	SpecHash string `json:"specHash,omitempty"`
	// Result is Running, Succeeded or Failed.
	// +optional
	Result string `json:"result,omitempty"`
	// Message is the recorded run's last message.
	// +optional
	Message string `json:"message,omitempty"`
}

// HookRunStatus is the observed state of a HookRun.
type HookRunStatus struct {
	// Phase is Pending until the Job is created, Running while it runs, and
	// Succeeded or Failed once it finished, or Skipped. Succeeded, Failed and
	// Skipped are terminal: the API server refuses to change them, and the
	// Job is never created again, even when it is deleted.
	// +kubebuilder:validation:Enum=Pending;Running;Succeeded;Failed;Skipped
	// +kubebuilder:validation:XValidation:rule="!(oldSelf in ['Succeeded', 'Failed', 'Skipped']) || self == oldSelf",message="a finished HookRun's phase cannot change"
	// +optional
	Phase string `json:"phase,omitempty"`

	// Message says why the HookRun is in its phase.
	// +optional
	Message string `json:"message,omitempty"`

	// JobName is the name of the Job the reconciler created.
	// +optional
	JobName string `json:"jobName,omitempty"`

	// JobUID is the UID of that Job. A Job of that name with another UID, or
	// none at all, while the HookRun runs, fails the HookRun: it is not run
	// again.
	// +optional
	JobUID string `json:"jobUID,omitempty"`

	// SpecHash is a hash of spec.job and spec.timeout when the Job was
	// created. A later spec change is not applied (condition
	// SpecChangedAfterStart).
	// +optional
	SpecHash string `json:"specHash,omitempty"`

	// StartedAt is when the HookRun started (the Job was created).
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`

	// Deadline is StartedAt plus the timeout.
	// +optional
	Deadline *metav1.Time `json:"deadline,omitempty"`

	// FinishedAt is when the HookRun reached Succeeded or Failed.
	// +optional
	FinishedAt *metav1.Time `json:"finishedAt,omitempty"`

	// Conditions: SpecChangedAfterStart is True when spec.job or spec.timeout
	// changed after the Job was created.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=hr
// +kubebuilder:printcolumn:name="Pipeline",type=string,JSONPath=`.spec.pipelineName`
// +kubebuilder:printcolumn:name="Env",type=string,JSONPath=`.spec.environment`
// +kubebuilder:printcolumn:name="Hook",type=string,JSONPath=`.spec.hook`
// +kubebuilder:printcolumn:name="When",type=string,JSONPath=`.spec.phase`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// HookRun is one run of a pre- or post-deploy hook (a Kubernetes Job) for
// one Bundle and environment. Created by the Bundle's kro Graph; reconciled
// by the HookRun reconciler, which creates the Job and records its result.
type HookRun struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   HookRunSpec   `json:"spec,omitempty"`
	Status HookRunStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// HookRunList contains a list of HookRun.
type HookRunList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []HookRun `json:"items"`
}

func init() {
	SchemeBuilder.Register(&HookRun{}, &HookRunList{})
}
