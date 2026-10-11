// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scm_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// TestControllerTokenSecret_Refuses (#1725 QA): a Pipeline in the
// controller's namespace whose git.secretRef names the controller's own SCM
// token Secret is refused; another Secret, another namespace, or a
// controller with no token Secret refuses nothing.
//
// Covers CHART-SCM-ALIAS-02.
func TestControllerTokenSecret_Refuses(t *testing.T) {
	c := &scm.ControllerTokenSecret{Namespace: "kardinal-system", Name: "kardinal-promoter-github-token"}
	pipe := func(ns, secret, secretNS string) *v1alpha1.Pipeline {
		p := &v1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: ns}}
		if secret != "" {
			p.Spec.Git.SecretRef = &v1alpha1.SecretRef{Name: secret, Namespace: secretNS}
		}
		return p
	}
	err := c.Refuses(pipe("kardinal-system", "kardinal-promoter-github-token", ""))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is the controller's own SCM token Secret")
	assert.Error(t, c.Refuses(pipe("kardinal-system", "kardinal-promoter-github-token", "kardinal-system")))
	assert.NoError(t, c.Refuses(pipe("kardinal-system", "demo-git-token", "")))
	assert.NoError(t, c.Refuses(pipe("team-a", "kardinal-promoter-github-token", "")), "a same-named Secret of another namespace")
	assert.NoError(t, c.Refuses(pipe("kardinal-system", "", "")))
	var none *scm.ControllerTokenSecret
	assert.NoError(t, none.Refuses(pipe("kardinal-system", "kardinal-promoter-github-token", "")))
}
