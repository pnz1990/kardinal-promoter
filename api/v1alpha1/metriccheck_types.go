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

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// MetricCheckSpec defines a Prometheus-backed metric gate.
// The MetricCheckReconciler queries Prometheus at spec.interval, evaluates
// the threshold, and writes results to status. PolicyGate CEL expressions
// can reference these results via the metrics.* context variable.
type MetricCheckSpec struct {
	// Provider is the metrics backend. Currently only "prometheus" is supported.
	// +kubebuilder:validation:Enum=prometheus
	// +kubebuilder:default=prometheus
	Provider string `json:"provider"`

	// PrometheusURL is the base URL of the Prometheus server.
	// Example: http://prometheus.monitoring.svc:9090
	// +kubebuilder:validation:MinLength=1
	PrometheusURL string `json:"prometheusURL"`

	// Query is the PromQL query string to evaluate.
	// The query must return a scalar or a single-element vector.
	// +kubebuilder:validation:MinLength=1
	Query string `json:"query"`

	// Threshold defines how to compare the metric value.
	// +kubebuilder:validation:Required
	Threshold MetricThreshold `json:"threshold"`

	// Interval is how often to re-evaluate the metric (e.g. "1m", "5m").
	// Defaults to "1m" if empty.
	// +optional
	Interval string `json:"interval,omitempty"`
}

// MetricThreshold defines the threshold comparison.
type MetricThreshold struct {
	// Value is the numeric threshold to compare against.
	Value float64 `json:"value"`

	// Operator is the comparison operator: lt, gt, lte, gte, eq.
	// The gate passes when: metric_value <operator> threshold.value
	// Example: operator=lt, value=0.01 → gate passes if metric < 0.01
	// +kubebuilder:validation:Enum=lt;gt;lte;gte;eq
	Operator string `json:"operator"`
}

// MetricCheckStatus records the most recent metric evaluation result.
type MetricCheckStatus struct {
	// LastValue is the most recent metric value returned by the Prometheus query.
	// Empty string means no evaluation has completed yet.
	// +optional
	LastValue string `json:"lastValue,omitempty"`

	// LastEvaluatedAt is the timestamp of the most recent evaluation.
	// +optional
	LastEvaluatedAt *metav1.Time `json:"lastEvaluatedAt,omitempty"`

	// Result is the evaluation result: "Pass" or "Fail".
	// Empty when no evaluation has completed.
	// +kubebuilder:validation:Enum=Pass;Fail
	// +optional
	Result string `json:"result,omitempty"`

	// Reason is a human-readable explanation of the current result.
	// +optional
	Reason string `json:"reason,omitempty"`
}

// MetricCheck is a Prometheus-backed metric gate.
// The MetricCheckReconciler queries Prometheus, evaluates the threshold,
// and writes the result to status. PolicyGate CEL expressions reference
// these results via `metrics.<name>.value` and `metrics.<name>.result`.
//
// MetricCheck objects are typically created alongside PolicyGates that
// reference them. They are cluster-scoped to the same namespace as the
// PolicyGate that uses them.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Provider",type=string,JSONPath=`.spec.provider`
// +kubebuilder:printcolumn:name="Result",type=string,JSONPath=`.status.result`
// +kubebuilder:printcolumn:name="LastValue",type=string,JSONPath=`.status.lastValue`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type MetricCheck struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MetricCheckSpec   `json:"spec,omitempty"`
	Status MetricCheckStatus `json:"status,omitempty"`
}

// MetricCheckList contains a list of MetricCheck objects.
//
// +kubebuilder:object:root=true
type MetricCheckList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MetricCheck `json:"items"`
}

func init() {
	SchemeBuilder.Register(&MetricCheck{}, &MetricCheckList{})
}
