// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// MetricCheckSpec defines a metric gate: a query against a metrics backend
// (or any HTTP JSON API) and a threshold. The MetricCheckReconciler queries
// the backend at spec.interval, evaluates the threshold, and writes the result
// to status. PolicyGate CEL expressions reference these results via the
// metrics.* context variable.
//
// Credentials are read from Secrets in the MetricCheck's namespace
// (*SecretRef fields); a spec never holds a token.
//
// +kubebuilder:validation:XValidation:rule="self.provider != 'prometheus' || (has(self.prometheusURL) && size(self.prometheusURL) > 0)",message="provider prometheus requires prometheusURL"
// +kubebuilder:validation:XValidation:rule="self.provider == 'web' || (has(self.query) && size(self.query) > 0)",message="query is required for every provider except web"
// +kubebuilder:validation:XValidation:rule="self.provider != 'datadog' || has(self.datadog)",message="provider datadog requires datadog"
// +kubebuilder:validation:XValidation:rule="self.provider != 'cloudwatch' || has(self.cloudWatch)",message="provider cloudwatch requires cloudWatch"
// +kubebuilder:validation:XValidation:rule="self.provider != 'newrelic' || has(self.newRelic)",message="provider newrelic requires newRelic"
// +kubebuilder:validation:XValidation:rule="self.provider != 'web' || has(self.web)",message="provider web requires web"
type MetricCheckSpec struct {
	// Provider is the metrics backend: prometheus (default), datadog,
	// cloudwatch, newrelic, or web (any HTTP API that answers JSON).
	// +kubebuilder:validation:Enum=prometheus;datadog;cloudwatch;newrelic;web
	// +kubebuilder:default=prometheus
	Provider string `json:"provider"`

	// PrometheusURL is the base URL of the Prometheus HTTP API (http or https).
	// A path is kept as a prefix: the query goes to <prometheusURL>/api/v1/query.
	// Required when provider is prometheus.
	// Example: http://prometheus.monitoring.svc:9090
	// +optional
	PrometheusURL string `json:"prometheusURL,omitempty"`

	// Query is the query to evaluate, in the provider's language: PromQL
	// (prometheus), a Datadog metrics query (datadog), a CloudWatch metric
	// math or Metrics Insights expression (cloudwatch), or NRQL (newrelic).
	// It must return a single value (one series; the latest point is used).
	// Required for every provider except web. It may contain placeholders such
	// as {{ bundle.version }}; see PerPromotion.
	// +optional
	Query string `json:"query,omitempty"`

	// Prometheus holds optional settings for provider prometheus.
	// +optional
	Prometheus *PrometheusProviderSpec `json:"prometheus,omitempty"`

	// Datadog configures provider datadog.
	// +optional
	Datadog *DatadogProviderSpec `json:"datadog,omitempty"`

	// CloudWatch configures provider cloudwatch.
	// +optional
	CloudWatch *CloudWatchProviderSpec `json:"cloudWatch,omitempty"`

	// NewRelic configures provider newrelic.
	// +optional
	NewRelic *NewRelicProviderSpec `json:"newRelic,omitempty"`

	// Web configures provider web: an HTTP request whose JSON response is read
	// with a JSONPath expression.
	// +optional
	Web *WebProviderSpec `json:"web,omitempty"`

	// Threshold defines how to compare the metric value.
	// +kubebuilder:validation:Required
	Threshold MetricThreshold `json:"threshold"`

	// Interval is how often to re-evaluate the metric (e.g. "1m", "5m").
	// Defaults to "1m" if empty or "0". Other values below "10s" are raised
	// to "10s".
	// +kubebuilder:validation:Pattern=`^$|^(0|(([0-9]+(\.[0-9]*)?|\.[0-9]+)(ns|us|µs|μs|ms|s|m|h))+)$`
	// +optional
	Interval string `json:"interval,omitempty"`

	// PerPromotion makes this MetricCheck a template that is evaluated once
	// per promotion instead of on its own. It is never queried itself. When a
	// Bundle's Graph is built, every PolicyGate in the Pipeline namespace that
	// reads metrics.<name> of this MetricCheck gets an instance: a MetricCheck
	// the Graph owns, labelled with the Bundle, environment and template name,
	// whose query, web.url, web.body and web.headers[].value have the
	// placeholders replaced: {{ bundle.name }}, {{ bundle.version }},
	// {{ bundle.imageTag }}, {{ bundle.imageDigest }}, {{ bundle.commitSHA }},
	// {{ pipeline.name }}, {{ environment.name }}, {{ namespace }}. The gate of
	// that Bundle and environment reads the instance's result as
	// metrics.<name>. The instance is created once the environment's upstream
	// environments are Verified, and suspended once the Bundle stops
	// promoting.
	// +optional
	PerPromotion bool `json:"perPromotion,omitempty"`

	// Suspend stops evaluation. The last result stays in status and goes
	// stale at status.validUntil, so gates that read it block (fail closed).
	// The Graph sets it on per-promotion instances once their Bundle is no
	// longer promoting.
	// +optional
	Suspend bool `json:"suspend,omitempty"`
}

// SecretKeyRef names one key of a Secret in the namespace of the object that
// holds the reference.
type SecretKeyRef struct {
	// Name is the Secret name.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Key is the key in the Secret's data.
	// +kubebuilder:validation:MinLength=1
	Key string `json:"key"`
}

// PrometheusProviderSpec holds optional settings for provider prometheus.
type PrometheusProviderSpec struct {
	// AuthorizationSecretRef names a Secret key whose value is sent as the
	// Authorization header, for example "Bearer <token>" or "Basic <base64>".
	// +optional
	AuthorizationSecretRef *SecretKeyRef `json:"authorizationSecretRef,omitempty"`
}

// DatadogProviderSpec configures provider datadog. The query goes to the
// metrics query API (GET <address>/api/v1/query) over [now-window, now]; the
// value is the latest non-null point of the single series it returns.
type DatadogProviderSpec struct {
	// Site is the Datadog site: datadoghq.com (default), datadoghq.eu,
	// us3.datadoghq.com, us5.datadoghq.com, ap1.datadoghq.com or
	// ddog-gov.com. The API is https://api.<site>.
	// +kubebuilder:validation:Pattern=`^$|^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$`
	// +optional
	Site string `json:"site,omitempty"`

	// Address overrides the API base URL (http or https), for example a proxy.
	// +optional
	Address string `json:"address,omitempty"`

	// Window is how far back the query looks. Defaults to 5m.
	// +kubebuilder:validation:Pattern=`^$|^(0|(([0-9]+(\.[0-9]*)?|\.[0-9]+)(ns|us|µs|μs|ms|s|m|h))+)$`
	// +optional
	Window string `json:"window,omitempty"`

	// APIKeySecretRef names the Secret key holding the Datadog API key
	// (DD-API-KEY header).
	APIKeySecretRef SecretKeyRef `json:"apiKeySecretRef"`

	// ApplicationKeySecretRef names the Secret key holding the Datadog
	// application key (DD-APPLICATION-KEY header).
	ApplicationKeySecretRef SecretKeyRef `json:"applicationKeySecretRef"`
}

// CloudWatchProviderSpec configures provider cloudwatch. The query is a
// GetMetricData expression (metric math, SEARCH, or a Metrics Insights
// SELECT) over [now-window, now]; the value is the latest point of the single
// result it returns.
//
// Credentials come from the Secret keys below. Without them the controller's
// own AWS identity (IRSA, EKS Pod Identity, environment) is used, but only
// when the controller runs with --metriccheck-cloudwatch-ambient-credentials
// (chart value metricCheck.cloudWatch.ambientCredentials); otherwise the
// check fails.
//
// +kubebuilder:validation:XValidation:rule="has(self.accessKeyIDSecretRef) == has(self.secretAccessKeySecretRef)",message="set both accessKeyIDSecretRef and secretAccessKeySecretRef, or neither"
// +kubebuilder:validation:XValidation:rule="!has(self.sessionTokenSecretRef) || has(self.accessKeyIDSecretRef)",message="sessionTokenSecretRef requires accessKeyIDSecretRef and secretAccessKeySecretRef"
type CloudWatchProviderSpec struct {
	// Region is the AWS region, for example us-east-1.
	// +kubebuilder:validation:Pattern=`^[a-z]{2}(-[a-z]+)+-[0-9]+$`
	Region string `json:"region"`

	// Endpoint overrides the CloudWatch API URL (http or https), for example
	// a VPC endpoint. Defaults to https://monitoring.<region>.amazonaws.com.
	// +optional
	Endpoint string `json:"endpoint,omitempty"`

	// Period is the granularity of the returned points, in seconds. Defaults
	// to 60.
	// +kubebuilder:validation:Minimum=1
	// +optional
	Period int32 `json:"period,omitempty"`

	// Window is how far back the query looks. Defaults to 5m.
	// +kubebuilder:validation:Pattern=`^$|^(0|(([0-9]+(\.[0-9]*)?|\.[0-9]+)(ns|us|µs|μs|ms|s|m|h))+)$`
	// +optional
	Window string `json:"window,omitempty"`

	// AccessKeyIDSecretRef names the Secret key holding the AWS access key ID.
	// +optional
	AccessKeyIDSecretRef *SecretKeyRef `json:"accessKeyIDSecretRef,omitempty"`

	// SecretAccessKeySecretRef names the Secret key holding the AWS secret
	// access key.
	// +optional
	SecretAccessKeySecretRef *SecretKeyRef `json:"secretAccessKeySecretRef,omitempty"`

	// SessionTokenSecretRef names the Secret key holding an AWS session token,
	// for temporary credentials.
	// +optional
	SessionTokenSecretRef *SecretKeyRef `json:"sessionTokenSecretRef,omitempty"`
}

// NewRelicProviderSpec configures provider newrelic. The query is NRQL, run
// through NerdGraph (POST <address>, actor.account.nrql). It must return one
// row; the value is its field resultField, or its only field.
type NewRelicProviderSpec struct {
	// AccountID is the New Relic account the NRQL query runs in.
	// +kubebuilder:validation:Minimum=1
	AccountID int64 `json:"accountID"`

	// Region selects the NerdGraph endpoint: US (default,
	// https://api.newrelic.com/graphql) or EU (https://api.eu.newrelic.com/graphql).
	// +kubebuilder:validation:Enum=US;EU
	// +optional
	Region string `json:"region,omitempty"`

	// Address overrides the NerdGraph URL (http or https).
	// +optional
	Address string `json:"address,omitempty"`

	// ResultField is the field of the result row to read, for example
	// "count" or "average.duration". Empty means the row must have exactly
	// one field.
	// +optional
	ResultField string `json:"resultField,omitempty"`

	// APIKeySecretRef names the Secret key holding a New Relic user API key
	// (API-Key header).
	APIKeySecretRef SecretKeyRef `json:"apiKeySecretRef"`
}

// WebProviderSpec configures provider web: an HTTP request to any JSON API.
// The check fails unless the response status is 2xx and JSONPath selects
// exactly one value, which is then compared with the threshold (numerically,
// or as text when threshold.text is set).
type WebProviderSpec struct {
	// URL is the http or https URL to call. It may contain placeholders such
	// as {{ bundle.version }}; see MetricCheckSpec.PerPromotion.
	// +kubebuilder:validation:MinLength=1
	URL string `json:"url"`

	// Method is GET (default) or POST.
	// +kubebuilder:validation:Enum=GET;POST
	// +optional
	Method string `json:"method,omitempty"`

	// Headers are sent with the request. A header takes its value from value
	// or, for credentials, from valueFromSecret.
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=32
	// +optional
	Headers []WebHeader `json:"headers,omitempty"`

	// Body is the request body, for POST. It may contain placeholders.
	// +optional
	Body string `json:"body,omitempty"`

	// JSONPath selects the value from the JSON response, in kubectl syntax,
	// for example {.data.errorRate} or {.checks[0].status}. It must select
	// exactly one string, number or boolean.
	// +kubebuilder:validation:Pattern=`^\{.*\}$`
	JSONPath string `json:"jsonPath"`

	// TimeoutSeconds bounds the request. Defaults to 10, at most 60.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=60
	// +optional
	TimeoutSeconds int32 `json:"timeoutSeconds,omitempty"`
}

// WebHeader is one HTTP header of a web MetricCheck.
//
// +kubebuilder:validation:XValidation:rule="has(self.value) != has(self.valueFromSecret)",message="set exactly one of value and valueFromSecret"
type WebHeader struct {
	// Name is the header name.
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9-]+$`
	Name string `json:"name"`

	// Value is the header value. It may contain placeholders. Do not put
	// credentials here: use valueFromSecret.
	// +optional
	Value *string `json:"value,omitempty"`

	// ValueFromSecret names a Secret key whose value is sent as the header
	// value.
	// +optional
	ValueFromSecret *SecretKeyRef `json:"valueFromSecret,omitempty"`
}

// MetricThreshold defines the threshold comparison.
//
// +kubebuilder:validation:XValidation:rule="!has(self.text) || self.operator in ['eq', 'ne']",message="threshold.text compares strings: use operator eq or ne"
type MetricThreshold struct {
	// Value is the numeric threshold to compare against. Ignored when text is
	// set.
	// +optional
	Value float64 `json:"value"`

	// Operator is the comparison operator: lt, gt, lte, gte, eq, ne.
	// The gate passes when: metric_value <operator> threshold.value
	// Example: operator=lt, value=0.01 → gate passes if metric < 0.01
	// +kubebuilder:validation:Enum=lt;gt;lte;gte;eq;ne
	Operator string `json:"operator"`

	// Text, when set, compares the value as a string instead of a number,
	// with operator eq or ne. For provider web, for example
	// {jsonPath: "{.status}", threshold: {operator: eq, text: "healthy"}}.
	// +optional
	Text *string `json:"text,omitempty"`
}

// MetricCheckStatus records the most recent metric evaluation result.
type MetricCheckStatus struct {
	// LastValue is the most recent value the query returned.
	// Empty string means no evaluation has completed yet.
	// +optional
	LastValue string `json:"lastValue,omitempty"`

	// LastEvaluatedAt is the timestamp of the most recent evaluation.
	// +optional
	LastEvaluatedAt *metav1.Time `json:"lastEvaluatedAt,omitempty"`

	// ValidUntil is when the current result goes stale: lastEvaluatedAt plus
	// three intervals, and at least 30s. The MetricCheck reconciler writes it
	// with each evaluation. A PolicyGate evaluated after this time, or when it
	// is unset, sees metrics.<name>.result as "Stale" and
	// metrics.<name>.stale as true, so a result that is no longer refreshed
	// cannot pass a gate.
	// +optional
	ValidUntil *metav1.Time `json:"validUntil,omitempty"`

	// Result is the evaluation result: "Pass" or "Fail".
	// Empty when no evaluation has completed.
	// +kubebuilder:validation:Enum=Pass;Fail
	// +optional
	Result string `json:"result,omitempty"`

	// Reason is a human-readable explanation of the current result. On a query
	// error it holds the HTTP status and, for a Prometheus API error, its error
	// text; the response body is never copied here.
	// +optional
	Reason string `json:"reason,omitempty"`
}

// MetricCheck is a metric gate backed by Prometheus, Datadog, CloudWatch,
// New Relic or any HTTP JSON API (web).
// The MetricCheckReconciler queries the backend, evaluates the threshold,
// and writes the result to status. PolicyGate CEL expressions reference
// these results via `metrics.<name>.value`, `metrics.<name>.result` and
// `metrics.<name>.stale`.
//
// MetricCheck objects are typically created alongside PolicyGates that
// reference them. MetricCheck is namespaced and must be in the namespace of
// the PolicyGate template that uses it: an org policy namespace for an org
// gate, the Pipeline namespace for a team gate.
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
