// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package receipt

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// leakyInput is the test promotion with credentials where a Bundle created
// with kubectl (or by an old controller) can carry them: a token in
// provenance.ciRunURL (also with a percent-encoded name, which the CRD's
// rule does not see), a token in the config repository's userinfo, and one
// in a gate's reason.
func leakyInput() Input {
	in := input("mixed")
	in.Bundle.Spec.Provenance.CIRunURL = "https://ci.example.com/run/1?a=1&token=s3cret-ci#%74oken=s3cret-frag"
	in.Bundle.Spec.ConfigRef.GitRepo = "https://bot:s3cret-git@git.example.com/acme/deploy"
	in.Decision.Gates[0].Reason = "see https://ci.example.com/x?access_token=s3cret-gate"
	return in
}

// TestBuild_RedactsCredentials (#2158): a receipt is signed and its Rekor
// sink publishes it for good, so a credential a Bundle carries never
// reaches the statement: every string of kardinalPromotion and every
// subject name is redacted before signing (a URL's userinfo, a credential
// parameter's value), the paths changed are recorded in
// kardinalPromotion.redacted, the redacted statement signs and verifies,
// and the Bundle (the cache's object) is left as it was.
//
// Covers RECEIPT-REDACT-01.
func TestBuild_RedactsCredentials(t *testing.T) {
	in := leakyInput()
	before := in.Bundle.DeepCopy()
	raw, digest, err := Build(in)
	require.NoError(t, err)
	for _, secret := range []string{"s3cret-ci", "s3cret-frag", "s3cret-git", "s3cret-gate", "bot:"} {
		assert.NotContains(t, string(raw), secret)
	}
	assert.Equal(t, before, in.Bundle, "the Bundle is not changed")
	assert.Equal(t, "see https://ci.example.com/x?access_token=s3cret-gate", in.Decision.Gates[0].Reason, "nor the step's snapshot")

	st, err := Parse(raw)
	require.NoError(t, err)
	k := st.Predicate.Kardinal
	assert.Equal(t, "https://ci.example.com/run/1?a=1&token=REDACTED#%74oken=REDACTED", k.Version.Provenance.CIRunURL)
	assert.Equal(t, "https://git.example.com/acme/deploy@3d1e", k.Version.Config)
	assert.Equal(t, "see https://ci.example.com/x?access_token=REDACTED", k.Gates[0].Reason)
	assert.Equal(t, []string{
		"kardinalPromotion.gates[0].reason",
		"kardinalPromotion.version.config",
		"kardinalPromotion.version.provenance.ciRunURL",
		"subject[1].name",
	}, k.Redacted)
	var names []string
	for _, s := range st.Subject {
		names = append(names, s.Name)
	}
	assert.Contains(t, names, "https://git.example.com/acme/deploy")
	assert.Equal(t, string(raw), scm.RedactText(string(raw)), "nothing left that redaction would change")

	s, pub := keySigner(t)
	signed, err := s.Sign(context.Background(), raw)
	require.NoError(t, err)
	v, err := Verify(signed.Bundle, VerifyOptions{Key: pub, Statement: raw})
	require.NoError(t, err)
	assert.Equal(t, digest, Digest(raw))
	assert.NotEmpty(t, v.Statement.Predicate.Kardinal.Redacted)
}

// TestBuild_NothingToRedact: a statement without credentials records no
// redaction, and is the statement built before redaction existed.
func TestBuild_NothingToRedact(t *testing.T) {
	raw, _, err := Build(input("mixed"))
	require.NoError(t, err)
	st, err := Parse(raw)
	require.NoError(t, err)
	assert.Empty(t, st.Predicate.Kardinal.Redacted)
	assert.False(t, strings.Contains(string(raw), `"redacted"`))
	assert.Equal(t, "https://ci/1", st.Predicate.Kardinal.Version.Provenance.CIRunURL)
}

// TestRedactValue reaches every kind of string the record holds: a field,
// a pointer's, a slice element's and a map value.
func TestRedactValue(t *testing.T) {
	rec := Record{
		Actor: &v1alpha1.Actor{Kind: v1alpha1.ActorAgent, Principal: "p",
			Asserted: &v1alpha1.AssertedActor{Session: "https://x.example/?sig=s3cret"}},
		Checks: Checks{Metrics: []Metric{{Name: "m", Value: "https://u:s3cret@x.example/"}}},
		Gates:  []Gate{},
	}
	got, paths, err := redactRecord(rec)
	require.NoError(t, err)
	assert.Equal(t, []string{"kardinalPromotion.actor.asserted.session", "kardinalPromotion.checks.metrics[0].value"}, paths)
	assert.Equal(t, "https://x.example/?sig=REDACTED", got.Actor.Asserted.Session)
	assert.Equal(t, "https://x.example/", got.Checks.Metrics[0].Value)
	assert.Equal(t, "https://x.example/?sig=s3cret", rec.Actor.Asserted.Session, "the input is copied, not changed")

	m := map[string]string{"a": "https://x.example/?token=s3cret", "b": "plain"}
	type withMap struct {
		M map[string]string `json:"m"`
	}
	w := withMap{M: m}
	var mp []string
	redactValue(reflect.ValueOf(&w).Elem(), "x", &mp)
	assert.Equal(t, []string{"x.m[a]"}, mp)
	assert.Equal(t, "https://x.example/?token=REDACTED", w.M["a"])
}
