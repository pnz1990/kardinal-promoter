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
	// The PolicyGate reconciler watches every ScheduleClock: each time Tick
	// changes, it re-evaluates every PolicyGate instance in the cluster except
	// those of finished Bundles, so time-based expressions such as
	// schedule.isWeekend see time pass. A gate does not reference the clock.
	// This is the sole purpose of this field.
	// +optional
	Tick string `json:"tick,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=sclock
// +kubebuilder:printcolumn:name="Interval",type=string,JSONPath=`.spec.interval`
// +kubebuilder:printcolumn:name="Last-Tick",type=string,JSONPath=`.status.tick`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// ScheduleClock writes a timestamp to status.tick on a configurable interval,
// generating Kubernetes watch events. Each tick makes the PolicyGate
// reconciler re-evaluate every gate instance except those of finished
// Bundles, so schedule.* expressions see time pass.
//
// One ScheduleClock per cluster is sufficient. Deploy to kardinal-system.
//
// Each gate is also re-evaluated every spec.recheckInterval; without a
// ScheduleClock that is the only periodic re-evaluation.
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
