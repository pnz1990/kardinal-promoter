// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scm

import (
	"fmt"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// ControllerTokenSecret is the Secret that holds the controller's own SCM
// token (--scm-token-secret-name in --scm-token-secret-namespace). A Pipeline
// must not name it as its git.secretRef: the Pipeline's author would choose
// where the controller's token goes (spec.git.url), and an existing Secret of
// its own exempts the Pipeline from --scm-allowed-repositories.
type ControllerTokenSecret struct {
	Namespace, Name string
}

// Refuses returns why p may not use its git.secretRef, or nil. A nil c, or
// one with no name (a token from a flag, not a Secret), refuses nothing.
func (c *ControllerTokenSecret) Refuses(p *v1alpha1.Pipeline) error {
	if p == nil {
		return nil
	}
	ref := p.Spec.Git.SecretRef
	if ref == nil {
		return nil
	}
	ns := ref.Namespace
	if ns == "" {
		ns = p.Namespace
	}
	return c.RefusesSecret(ns, ref.Name)
}

// RefusesSecret returns why the Secret name in namespace may not be used as
// a Pipeline's git Secret (a render Job's mount, say), or nil.
func (c *ControllerTokenSecret) RefusesSecret(namespace, name string) error {
	if c == nil || c.Name == "" || name != c.Name || namespace != c.Namespace {
		return nil
	}
	return fmt.Errorf("git.secretRef %q is the controller's own SCM token Secret (--scm-token-secret-name in %s): "+
		"a Pipeline may not use it; create a separate Secret with a token for this repository, label it "+
		"kardinal.io/referenceable=true and name it in spec.git.secretRef", c.Name, c.Namespace)
}
