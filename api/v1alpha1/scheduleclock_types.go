// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ScheduleClockSpec defines the desired state of a ScheduleClock.
type ScheduleClockSpec struct {
	// Interval is how often the ScheduleClock updates status.tick.
	// Uses Go duration format (e.g. "1m", "30s").
	// Minimum recommended value is 30s; sub-minute precision is rarely needed
	// for business-hour gates.
	// +kubebuilder:default="1m"
	// +kubebuilder:validation:Pattern=`^$|^(0|(([0-9]+(\.[0-9]*)?|\.[0-9]+)(ns|us|µs|μs|ms|s|m|h))+)$`
	// +optional
	Interval string `json:"interval,omitempty"`
}

// ScheduleClockStatus defines the observed state of a ScheduleClock.
type ScheduleClockStatus struct {
	// Tick is the RFC3339 timestamp written by the reconciler on every interval.
	// PolicyGate nodes that reference this ScheduleClock in their Graph scope
	// will re-evaluate their readyWhen expressions each time Tick changes, because
	// the Graph controller watches all nodes in scope for watch events.
	// This is the sole purpose of this field: to generate Kubernetes watch events
	// on a regular interval so that time-based PolicyGate expressions are
	// re-evaluated without a dedicated recheckAfter primitive in kro.
	// +optional
	Tick string `json:"tick,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=sclock
// +kubebuilder:printcolumn:name="Interval",type=string,JSONPath=`.spec.interval`
// +kubebuilder:printcolumn:name="Last-Tick",type=string,JSONPath=`.status.tick`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// ScheduleClock is an Owned node that writes a timestamp to status.tick on a
// configurable interval, generating Kubernetes watch events. PolicyGates with
// schedule.* expressions reference the ScheduleClock in their Graph scope;
// each tick causes the Graph controller to re-evaluate all dependant nodes.
//
// One ScheduleClock per cluster is sufficient. Deploy to kardinal-system.
//
// This CRD eliminates the ctrl.Result{RequeueAfter} timer loop in the
// PolicyGate reconciler (PG-4 in docs/design/11-graph-purity-tech-debt.md).
type ScheduleClock struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ScheduleClockSpec   `json:"spec,omitempty"`
	Status ScheduleClockStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ScheduleClockList contains a list of ScheduleClock.
type ScheduleClockList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ScheduleClock `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ScheduleClock{}, &ScheduleClockList{})
}
