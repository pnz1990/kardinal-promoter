// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package v1alpha1_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestScmProviderCRDRules: a ScmProvider's Secrets are in its own namespace,
// and a ClusterScmProvider must name theirs. The API server escapes the CEL
// keyword "namespace" as __namespace__ in rules; the plain cel-go evaluation
// here sees the escaped key, so self uses it too.
func TestScmProviderCRDRules(t *testing.T) {
	ref := func(ns string) map[string]interface{} {
		r := map[string]interface{}{"name": "tok"}
		if ns != "" {
			r["__namespace__"] = ns
		}
		return r
	}
	obj := func(secretNS, webhookNS string, webhook bool) map[string]interface{} {
		spec := map[string]interface{}{"type": "github", "secretRef": ref(secretNS)}
		if webhook {
			spec["webhookSecretRef"] = ref(webhookNS)
		}
		return map[string]interface{}{"spec": spec}
	}
	ns := crdSchema(t, "kardinal.io_scmproviders.yaml")
	assert.Empty(t, failingRules(t, ns, obj("", "", true)))
	assert.Equal(t, []string{"a ScmProvider's Secrets are in its own namespace: leave spec.secretRef.namespace empty"},
		failingRules(t, ns, obj("other", "", false)))
	assert.Equal(t, []string{"a ScmProvider's Secrets are in its own namespace: leave spec.webhookSecretRef.namespace empty"},
		failingRules(t, ns, obj("", "other", true)))

	cluster := crdSchema(t, "kardinal.io_clusterscmproviders.yaml")
	assert.Empty(t, failingRules(t, cluster, obj("scm", "scm", true)))
	assert.Empty(t, failingRules(t, cluster, obj("scm", "", false)))
	assert.Equal(t, []string{"spec.secretRef.namespace is required on a ClusterScmProvider"},
		failingRules(t, cluster, obj("", "", false)))
	assert.Equal(t, []string{"spec.webhookSecretRef.namespace is required on a ClusterScmProvider"},
		failingRules(t, cluster, obj("scm", "", true)))
}
