// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ChangeWindowSpec defines the desired state of a ChangeWindow.
type ChangeWindowSpec struct {
	// Type is the ChangeWindow type.
	// "blackout": no promotions allowed between Start and End.
	// "recurring": promotions allowed only during AllowedDays/AllowedHours windows.
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
	// Timezone is the IANA timezone name (e.g. "America/Los_Angeles").
	// +optional
	Timezone string `json:"timezone,omitempty"`

	// AllowedDays lists the days of the week when promotions are allowed.
	// Valid values: Mon, Tue, Wed, Thu, Fri, Sat, Sun.
	// +optional
	AllowedDays []string `json:"allowedDays,omitempty"`

	// AllowedHours is a time range in "HH:MM-HH:MM" format (24h, local time).
	// +optional
	AllowedHours string `json:"allowedHours,omitempty"`
}

// ChangeWindowStatus defines the observed state of a ChangeWindow.
type ChangeWindowStatus struct {
	// Active is true when the ChangeWindow is currently in effect.
	// Updated by the controller on each reconcile cycle.
	// +optional
	Active bool `json:"active,omitempty"`

	// Reason explains the current active/inactive state.
	// +optional
	Reason string `json:"reason,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=cw
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.type`
// +kubebuilder:printcolumn:name="Active",type=boolean,JSONPath=`.status.active`
// +kubebuilder:printcolumn:name="Reason",type=string,JSONPath=`.status.reason`,priority=1
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// ChangeWindow defines a time window during which promotions are blocked (K-04).
// When active, all pipeline promotions in the cluster are blocked by PolicyGates
// using the changewindow.isBlocked() CEL function.
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
