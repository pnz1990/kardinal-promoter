// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scm

import (
	"github.com/prometheus/client_golang/prometheus"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
)

// Circuit labels: circuit is "owner" for a repository owner's circuit (owner
// the user, org or group) and "quota" for the token's rate-limit circuit
// (owner ""). Owners whose circuit is pruned (closed, no failures) lose their
// series, so the labels stay as few as the owners that are failing.
var (
	// CircuitOpenGauge is 1 while a circuit is open or half-open, else 0.
	CircuitOpenGauge = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "kardinal_scm_circuit_open",
		Help: "1 while an SCM circuit breaker is open or half-open (calls for its owner, or for the token's quota, wait), else 0.",
	}, []string{"circuit", "owner"})
	// CircuitOpens counts the times a closed circuit opened.
	CircuitOpens = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "kardinal_scm_circuit_opens_total",
		Help: "Times an SCM circuit breaker opened after its owner's calls, or the token's quota, failed.",
	}, []string{"circuit", "owner"})
)

func init() {
	ctrlmetrics.Registry.MustRegister(CircuitOpenGauge, CircuitOpens)
}

// watchCircuit makes cb report its state under the circuit and owner labels.
func watchCircuit(cb *CircuitBreaker, circuit, owner string) {
	cb.onChange = func(open bool) {
		if open {
			CircuitOpens.WithLabelValues(circuit, owner).Inc()
			CircuitOpenGauge.WithLabelValues(circuit, owner).Set(1)
			return
		}
		CircuitOpenGauge.WithLabelValues(circuit, owner).Set(0)
	}
}
