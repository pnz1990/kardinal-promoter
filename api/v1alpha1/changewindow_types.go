// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ChangeWindowSpec defines the desired state of a ChangeWindow.
// +kubebuilder:validation:XValidation:rule="self.type != 'blackout' || (has(self.start) && has(self.end))",message="a blackout ChangeWindow requires start and end"
// +kubebuilder:validation:XValidation:rule="self.type != 'recurring' || has(self.schedule)",message="a recurring ChangeWindow requires schedule"
type ChangeWindowSpec struct {
	// Type is the ChangeWindow type.
	// "blackout": the window is active (blocking) from Start (inclusive) to End (exclusive).
	// "recurring": Schedule describes when promotions are allowed; the window is
	// active (blocking) at every other time.
	// An invalid spec (for example End not after Start, an unknown timezone or a
	// malformed allowedHours) makes the window active, so gates that reference it block.
	// +kubebuilder:validation:Enum=blackout;recurring
	Type string `json:"type"`

	// Start is when a blackout window begins (required for type: blackout).
	// +optional
	Start metav1.Time `json:"start,omitempty"`

	// End is when a blackout window ends (required for type: blackout).
	// +optional
	End metav1.Time `json:"end,omitempty"`

	// Reason is a human-readable explanation for this window.
	// +optional
	Reason string `json:"reason,omitempty"`

	// Schedule configures a recurring allowed-hours window (for type: recurring).
	// +optional
	Schedule *ChangeWindowSchedule `json:"schedule,omitempty"`
}

// ChangeWindowSchedule configures a recurring allowed-hours window.
type ChangeWindowSchedule struct {
	// Timezone is the IANA timezone name (e.g. "America/Los_Angeles"). Default: UTC.
	// "Local" is rejected: it would be the controller's own timezone. An unknown
	// name makes the window invalid: status condition Valid is False with the
	// error, and the window is active (blocking).
	// +optional
	Timezone string `json:"timezone,omitempty"`

	// AllowedDays lists the days of the week when promotions are allowed.
	// Valid values: Mon, Tue, Wed, Thu, Fri, Sat, Sun (full names such as Monday
	// are accepted too). Empty means every day.
	// +optional
	AllowedDays []string `json:"allowedDays,omitempty"`

	// AllowedHours is a time range in "HH:MM-HH:MM" format (24h, in Timezone).
	// The start is inclusive and the end exclusive; "24:00" is allowed as the end.
	// An end before the start is an overnight range that belongs to the start day
	// (for example "22:00-02:00" on Fri allows Friday 22:00 to Saturday 02:00).
	// Empty means the whole day.
	// +kubebuilder:validation:Pattern=`^([01][0-9]|2[0-3]):[0-5][0-9]-(([01][0-9]|2[0-3]):[0-5][0-9]|24:00)$`
	// +optional
	AllowedHours string `json:"allowedHours,omitempty"`
}

// ChangeWindowStatus defines the observed state of a ChangeWindow.
type ChangeWindowStatus struct {
	// Active is true when the ChangeWindow is currently blocking promotions.
	// Written by the ChangeWindow reconciler, which requeues at the next window
	// boundary. PolicyGates evaluate the spec at their own evaluation time with
	// the same logic, so they never depend on this field being fresh.
	// +optional
	Active bool `json:"active,omitempty"`

	// Reason explains the current active/inactive state.
	// +optional
	Reason string `json:"reason,omitempty"`

	// Conditions holds status conditions. Valid is True when the spec can be
	// evaluated. It is False (reason InvalidSpec, the error in the message) when
	// it cannot, for example for an unknown timezone; the window is then active,
	// so every gate that references it blocks until the spec is fixed.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=cw
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.type`
// +kubebuilder:printcolumn:name="Active",type=boolean,JSONPath=`.status.active`
// +kubebuilder:printcolumn:name="Valid",type=string,JSONPath=`.status.conditions[?(@.type=="Valid")].status`
// +kubebuilder:printcolumn:name="Reason",type=string,JSONPath=`.status.reason`,priority=1
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// ChangeWindow defines a cluster-scoped time window during which promotions are
// blocked (K-04). A ChangeWindow blocks nothing by itself: a PolicyGate blocks
// while a window it references is active, for example
// !changewindow.isBlocked("holiday-freeze") or changewindow.isAllowed("business-hours").
// A gate that names a ChangeWindow that does not exist blocks.
type ChangeWindow struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ChangeWindowSpec   `json:"spec,omitempty"`
	Status ChangeWindowStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ChangeWindowList contains a list of ChangeWindow.
type ChangeWindowList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ChangeWindow `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ChangeWindow{}, &ChangeWindowList{})
}
