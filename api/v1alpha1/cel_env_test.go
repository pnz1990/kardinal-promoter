// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package v1alpha1_test

import (
	"github.com/google/cel-go/cel"
	"k8s.io/apiserver/pkg/cel/environment"
)

// serverCELEnv is the CEL environment the API server compiles CRD rules
// in (k8s.io/apiserver's base environment: its string, list, regex, URL
// and other libraries, at the default compatibility version), with opts
// (the rule's self and oldSelf). A rule a test compiles here compiles on
// the API server, and one that uses a Kubernetes library function
// (lowerAscii, replace, findAll) is not refused by a bare cel.NewEnv.
func serverCELEnv(opts ...cel.EnvOption) (*cel.Env, error) {
	return environment.MustBaseEnvSet(environment.DefaultCompatibilityVersion()).NewExpressionsEnv().Extend(opts...)
}
