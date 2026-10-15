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

package receipt

import (
	"context"
	"crypto"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/receipt/receipttest"
)

const imageDigest = "sha256:9f2c000000000000000000000000000000000000000000000000000000000001"

var verifiedAt = time.Date(2026, 10, 14, 10, 24, 3, 0, time.UTC)

func testBundle(typ string) *v1alpha1.Bundle {
	b := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout-1-5-0", Namespace: "payments", UID: "1b2c"},
		Spec: v1alpha1.BundleSpec{
			Type: typ, Pipeline: "checkout",
			Provenance: &v1alpha1.BundleProvenance{Author: "alice", CommitSHA: "abc", CIRunURL: "https://ci/1"},
			Actor: &v1alpha1.Actor{Kind: v1alpha1.ActorAgent, Principal: "system:serviceaccount:ci:fixer",
				Agent: "fixer", Sponsor: "alice@example.com", Asserted: &v1alpha1.AssertedActor{Model: "claude-opus-5"}},
		},
		Status: v1alpha1.BundleStatus{Version: "1.5.0"},
	}
	if typ == "image" || typ == "mixed" {
		b.Spec.Images = []v1alpha1.ImageRef{{Repository: "ghcr.io/acme/checkout", Digest: imageDigest}}
	}
	if typ == "config" || typ == "mixed" {
		b.Spec.ConfigRef = &v1alpha1.ConfigRef{GitRepo: "https://git.example.com/acme/deploy", CommitSHA: "3d1e"}
	}
	return b
}

func testStep() *v1alpha1.PromotionStep {
	done := metav1.NewTime(verifiedAt.Add(-time.Minute))
	return &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout-1-5-0-prod", Namespace: "payments"},
		Spec:       v1alpha1.PromotionStepSpec{PipelineName: "checkout", BundleName: "checkout-1-5-0", Environment: "prod"},
		Status: v1alpha1.PromotionStepStatus{Steps: []v1alpha1.StepStatus{
			{Name: "health-check", State: v1alpha1.StepExecutionCompleted, CompletedAt: &done},
		}},
	}
}

func testDecision() *v1alpha1.PromotionDecision {
	at := metav1.NewTime(verifiedAt.Add(-time.Hour))
	gates := []v1alpha1.DecisionGate{{
		Name: "checkout-1-5-0-prod-approve", Template: "payments/kardinal-agents-approve", TemplateUID: "t1",
		TemplateGeneration: 1, TemplateDigest: "sha256:aa", Expression: "approvals.count >= 1", Ready: true,
		Reason: "1 of 1 approvals", EvaluatedAt: &at, ApprovalRequired: true,
		Approvals: []v1alpha1.GateApprovalStatus{
			{User: "carol@example.com", Decision: "approve", Counted: true,
				Actor: &v1alpha1.Actor{Kind: v1alpha1.ActorHuman, Principal: "carol@example.com"}},
			{User: "alice", Decision: "approve", Counted: false, Reason: "the author cannot approve"},
		},
	}}
	return &v1alpha1.PromotionDecision{TakenAt: at, Gates: gates, PipelineGeneration: 14, PipelineSpecHash: "ph",
		PolicyDigest: PolicyDigest(14, "ph", gates)}
}

func input(typ string) Input {
	return Input{
		Name: "checkout-1-5-0-prod", ClusterID: "9b1e", ControllerVersion: "v0.13.0", ChartVersion: "0.13.0",
		Now: verifiedAt, Bundle: testBundle(typ), Step: testStep(), Decision: testDecision(), Snapshot: true,
		HealthAdapter: "argocd", PR: &PR{URL: "https://github.com/acme/deploy/pull/412", Number: 412, MergeCommit: "m1"},
	}
}

func build(t *testing.T, in Input) (*Statement, []byte) {
	t.Helper()
	b, d, err := Build(in)
	require.NoError(t, err)
	assert.Equal(t, Digest(b), d)
	st, err := Parse(b)
	require.NoError(t, err)
	return st, b
}

// TestBuild_Subjects: an image version names each image digest, a config
// version its commit (gitCommit), a mixed version both, and a version with
// neither its own Bundle spec.
//
// Covers RECEIPT-STATEMENT-01.
func TestBuild_Subjects(t *testing.T) {
	cases := map[string][]Subject{
		"image":  {{Name: "ghcr.io/acme/checkout", Digest: map[string]string{"sha256": strings.TrimPrefix(imageDigest, "sha256:")}}},
		"config": {{Name: "https://git.example.com/acme/deploy", Digest: map[string]string{"gitCommit": "3d1e"}}},
		"mixed": {
			{Name: "ghcr.io/acme/checkout", Digest: map[string]string{"sha256": strings.TrimPrefix(imageDigest, "sha256:")}},
			{Name: "https://git.example.com/acme/deploy", Digest: map[string]string{"gitCommit": "3d1e"}},
		},
	}
	for typ, want := range cases {
		t.Run(typ, func(t *testing.T) {
			st, _ := build(t, input(typ))
			assert.Equal(t, want, st.Subject)
		})
	}
	t.Run("tag only", func(t *testing.T) {
		in := input("image")
		in.Bundle.Spec.Images[0].Digest, in.Bundle.Spec.Images[0].Tag = "", "1.5.0"
		st, _ := build(t, in)
		require.Len(t, st.Subject, 1)
		assert.Equal(t, "kardinal://9b1e/namespaces/payments/bundles/checkout-1-5-0", st.Subject[0].Name)
		assert.Len(t, st.Subject[0].Digest["sha256"], 64)
		assert.Equal(t, []string{"ghcr.io/acme/checkout:1.5.0"}, st.Predicate.Kardinal.Version.Images)
	})
}

// TestBuild_Record: the VSA core and the kardinalPromotion record hold the
// version, the actor with its asserted fields apart, the gates from the
// snapshot with their approvals, the checks and the change.
//
// Covers RECEIPT-STATEMENT-01.
func TestBuild_Record(t *testing.T) {
	in := input("image")
	valid := metav1.NewTime(verifiedAt.Add(time.Hour))
	in.Metrics = []v1alpha1.MetricCheck{{
		ObjectMeta: metav1.ObjectMeta{Name: "m-1", Labels: map[string]string{"kardinal.io/metric-template": "error-rate"}},
		Status:     v1alpha1.MetricCheckStatus{LastValue: "0.002", Result: "Pass", ValidUntil: &valid},
	}}
	in.ImageVerification = &v1alpha1.ImageVerification{Status: v1alpha1.ImageVerificationStatus{
		Images: []v1alpha1.ImageVerificationResult{{Image: "ghcr.io/acme/checkout@" + imageDigest, Verified: true, Signer: "ci"}},
	}}
	st, _ := build(t, in)
	p := st.Predicate
	assert.Equal(t, StatementType, st.Type)
	assert.Equal(t, PredicateVSA, st.PredicateType)
	assert.Equal(t, ResultPassed, p.VerificationResult)
	assert.Equal(t, "2026-10-14T10:24:03Z", p.TimeVerified)
	assert.Equal(t, "kardinal://9b1e/namespaces/payments/pipelines/checkout/environments/prod", p.ResourceURI)
	assert.Equal(t, "kardinal://9b1e/namespaces/payments/pipelines/checkout?generation=14", p.Policy.URI)
	assert.Equal(t, strings.TrimPrefix(in.Decision.PolicyDigest, "sha256:"), p.Policy.Digest["sha256"])
	assert.Equal(t, []string{LevelGatesPassed, LevelApproved, LevelHealthy}, p.VerifiedLevels)
	assert.Equal(t, map[string]string{"kardinal-promoter": "v0.13.0"}, p.Verifier.Version)

	r := p.Kardinal
	assert.Equal(t, "checkout-1-5-0-prod", r.Receipt)
	assert.True(t, r.DecisionSnapshot)
	assert.Equal(t, Version{Bundle: "checkout-1-5-0", BundleUID: "1b2c", Version: "1.5.0", Type: "image",
		Images:     []string{"ghcr.io/acme/checkout@" + imageDigest},
		Provenance: in.Bundle.Spec.Provenance}, r.Version)
	require.NotNil(t, r.Actor)
	assert.Equal(t, "alice@example.com", r.Actor.Sponsor)
	assert.Equal(t, "claude-opus-5", r.Actor.Asserted.Model)
	require.Len(t, r.Gates, 1)
	g := r.Gates[0]
	assert.Equal(t, "pass", g.Result)
	assert.Equal(t, "payments/kardinal-agents-approve", g.Template)
	assert.Equal(t, map[string]string{"sha256": "aa"}, g.TemplateDigest)
	assert.Len(t, g.Approvals, 2)
	assert.Equal(t, "carol@example.com", g.Approvals[0].Actor.Principal)
	assert.Equal(t, []Metric{{Name: "error-rate", Value: "0.002", Result: "Pass", ValidUntil: "2026-10-14T11:24:03Z"}}, r.Checks.Metrics)
	assert.Equal(t, []ImageCheck{{Image: "ghcr.io/acme/checkout@" + imageDigest, Verified: true, By: "ci"}}, r.Checks.ImageVerification)
	assert.Equal(t, &Health{Adapter: "argocd", Result: "Healthy", At: "2026-10-14T10:23:03Z"}, r.Checks.Health)
	assert.Equal(t, Change{Environment: "prod", PR: in.PR}, r.Change)
	assert.False(t, r.Truncated)
}

// TestBuild_Levels: verifiedLevels claim only what the record shows: an
// overridden gate adds OVERRIDDEN, a gate that did not pass removes
// GATES_PASSED, approvals that did not count do not make APPROVED, and a
// rollback names the Bundle it restores.
//
// Covers RECEIPT-STATEMENT-02.
func TestBuild_Levels(t *testing.T) {
	cases := map[string]struct {
		mutate func(*Input)
		want   []string
	}{
		"overridden": {func(in *Input) {
			in.Decision.Gates[0].Override = &v1alpha1.DecisionOverride{CreatedBy: "bob", Reason: "hotfix", Verified: true}
		}, []string{LevelGatesPassed, LevelApproved, LevelHealthy, LevelOverridden}},
		"gate not passing (no snapshot)": {func(in *Input) {
			in.Snapshot = false
			in.Decision.Gates[0].Ready = false
		}, []string{LevelApproved, LevelHealthy}},
		"no counted approval": {func(in *Input) {
			in.Decision.Gates[0].Approvals = in.Decision.Gates[0].Approvals[1:]
		}, []string{LevelGatesPassed, LevelHealthy}},
		"no gates": {func(in *Input) { in.Decision = nil }, []string{LevelHealthy}},
		"unhealthy": {func(in *Input) {
			in.Step.Status.Steps[0].State = v1alpha1.StepExecutionFailed
		}, []string{LevelGatesPassed, LevelApproved}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			in := input("image")
			tc.mutate(&in)
			st, _ := build(t, in)
			assert.Equal(t, tc.want, st.Predicate.VerifiedLevels)
		})
	}
	t.Run("rollback", func(t *testing.T) {
		in := input("image")
		in.Bundle.Spec.Provenance.RollbackOf = "checkout-1-4-0"
		st, _ := build(t, in)
		assert.Equal(t, "checkout-1-4-0", st.Predicate.Kardinal.RollbackOf)
	})
}

// TestBuild_SizeCap: a record over 256 KiB drops the approvals that did not
// count first, counts them, sets truncated, and keeps the levels of the
// full record.
//
// Covers RECEIPT-STATEMENT-03.
func TestBuild_SizeCap(t *testing.T) {
	in := input("image")
	g := &in.Decision.Gates[0]
	for i := 0; i < 101; i++ {
		g.Approvals = append(g.Approvals, v1alpha1.GateApprovalStatus{
			User: "user", Decision: "approve", Reason: strings.Repeat("x", 4000)})
	}
	b, _, err := Build(in)
	require.NoError(t, err)
	assert.LessOrEqual(t, len(b), v1alpha1.MaxReceiptStatementBytes)
	st, err := Parse(b)
	require.NoError(t, err)
	r := st.Predicate.Kardinal
	assert.True(t, r.Truncated)
	assert.Len(t, r.Gates[0].Approvals, 1, "only the counted approval is kept")
	assert.Equal(t, 102, r.Gates[0].ApprovalsOmitted)
	assert.Equal(t, []string{LevelGatesPassed, LevelApproved, LevelHealthy}, st.Predicate.VerifiedLevels)

	t.Run("gates past the first", func(t *testing.T) {
		in := input("image")
		for i := 0; i < 63; i++ {
			gate := in.Decision.Gates[0]
			gate.Name = strings.Repeat("g", 8) + string(rune('a'+i%26))
			gate.Expression = strings.Repeat("e", 1000)
			gate.Message = strings.Repeat("m", 1000)
			gate.Approvals = nil
			for j := 0; j < 101; j++ {
				gate.Approvals = append(gate.Approvals, v1alpha1.GateApprovalStatus{User: strings.Repeat("u", 200), Decision: "approve", Counted: true})
			}
			in.Decision.Gates = append(in.Decision.Gates, gate)
		}
		b, _, err := Build(in)
		require.NoError(t, err)
		assert.LessOrEqual(t, len(b), v1alpha1.MaxReceiptStatementBytes)
		st, err := Parse(b)
		require.NoError(t, err)
		assert.Len(t, st.Predicate.Kardinal.Gates, 1)
		assert.Equal(t, 63, st.Predicate.Kardinal.OmittedGates)
	})
}

// TestPolicyDigest: the policy digest is stable across a template's status
// writes (it reads the spec digest and generation only), sorted by template,
// and changes with the template's spec or the Pipeline's generation.
//
// Covers RECEIPT-STATEMENT-02.
func TestPolicyDigest(t *testing.T) {
	spec := v1alpha1.PolicyGateSpec{Expression: "!schedule.isWeekend"}
	d1, err := TemplateDigest(spec)
	require.NoError(t, err)
	tmpl := v1alpha1.PolicyGate{Spec: spec, Status: v1alpha1.PolicyGateStatus{Ready: true, Reason: "a"}}
	tmpl.Status.Reason = "b" // a status write
	d2, err := TemplateDigest(tmpl.Spec)
	require.NoError(t, err)
	assert.Equal(t, d1, d2)

	a := v1alpha1.DecisionGate{Template: "ns/a", TemplateUID: "u1", TemplateGeneration: 1, TemplateDigest: d1}
	b := v1alpha1.DecisionGate{Template: "ns/b", TemplateUID: "u2", TemplateGeneration: 3, TemplateDigest: d1}
	assert.Equal(t, PolicyDigest(1, "h", []v1alpha1.DecisionGate{a, b}), PolicyDigest(1, "h", []v1alpha1.DecisionGate{b, a}))
	assert.NotEqual(t, PolicyDigest(1, "h", []v1alpha1.DecisionGate{a}), PolicyDigest(2, "h", []v1alpha1.DecisionGate{a}))
	spec.Expression = "true"
	d3, err := TemplateDigest(spec)
	require.NoError(t, err)
	a3 := a
	a3.TemplateDigest = d3
	assert.NotEqual(t, PolicyDigest(1, "h", []v1alpha1.DecisionGate{a}), PolicyDigest(1, "h", []v1alpha1.DecisionGate{a3}))
}

func keySigner(t *testing.T) (*Signer, []byte) {
	t.Helper()
	priv, pub, err := receipttest.Key()
	require.NoError(t, err)
	return &Signer{Mode: v1alpha1.ReceiptSigningKey, Key: func(context.Context) (crypto.Signer, string, error) {
		k, err := ParsePrivateKey(priv)
		return k, "kardinal-system/receipt-key", err
	}}, pub
}

// TestSignVerify_Key: a statement signed with a static key verifies with
// its public key, and every check of verify-receipt fails on its own case:
// a changed statement, another key, another environment, another subject,
// a required level missing or a forbidden one present.
//
// Covers RECEIPT-SIGN-01, RECEIPT-VERIFY-01.
func TestSignVerify_Key(t *testing.T) {
	s, pub := keySigner(t)
	stmt, _ := build(t, input("image"))
	raw, _, err := Build(input("image"))
	require.NoError(t, err)
	signed, err := s.Sign(context.Background(), raw)
	require.NoError(t, err)
	assert.Equal(t, v1alpha1.ReceiptSigningKey, signed.SignedBy.Mode)
	assert.Equal(t, "kardinal-system/receipt-key", signed.SignedBy.Identity)
	assert.NotEmpty(t, signed.SignedBy.KeyHint)
	assert.Nil(t, signed.LogIndex)

	ok := VerifyOptions{Key: pub, Subject: imageDigest, Environment: "prod", Statement: raw,
		Require: []string{"GATES_PASSED", "KARDINAL_APPROVED", "!OVERRIDDEN"}}
	v, err := Verify(signed.Bundle, ok)
	require.NoError(t, err)
	assert.Equal(t, stmt, v.Statement)
	assert.Equal(t, "key", v.Signer)

	_, otherPub := keySigner(t)
	tampered := append([]byte{}, raw...)
	tampered[len(tampered)-2] = ' '
	cases := map[string]struct {
		mutate func(*VerifyOptions)
		want   string
	}{
		"tampered statement": {func(o *VerifyOptions) { o.Statement = tampered }, "changed after it was signed"},
		"another key":        {func(o *VerifyOptions) { o.Key = otherPub }, "signature"},
		"another env":        {func(o *VerifyOptions) { o.Environment = "uat" }, "not environment uat"},
		"another subject":    {func(o *VerifyOptions) { o.Subject = "sha256:" + strings.Repeat("0", 64) }, "not about"},
		"required level":     {func(o *VerifyOptions) { o.Require = []string{"VERIFIED_BY_AGENT"} }, "KARDINAL_VERIFIED_BY_AGENT is required"},
		"forbidden level":    {func(o *VerifyOptions) { o.Require = []string{"!APPROVED"} }, "must be absent"},
		"no key, no id":      {func(o *VerifyOptions) { o.Key = nil }, "either a public key"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			o := ok
			tc.mutate(&o)
			_, err := Verify(signed.Bundle, o)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
	t.Run("changed bundle payload", func(t *testing.T) {
		var m map[string]any
		require.NoError(t, json.Unmarshal(signed.Bundle, &m))
		env := m["dsseEnvelope"].(map[string]any)
		env["payload"] = env["payload"].(string)[:len(env["payload"].(string))-4] + "AAAA"
		b, err := json.Marshal(m)
		require.NoError(t, err)
		_, err = Verify(b, VerifyOptions{Key: pub})
		require.Error(t, err)
	})
}

// TestSignVerify_Keyless: keyless signing gets a certificate from Fulcio
// for an ephemeral key and a timestamp, and verifies only with the exact
// identity and issuer and the trusted root.
//
// Covers RECEIPT-SIGN-02, RECEIPT-VERIFY-01.
func TestSignVerify_Keyless(t *testing.T) {
	const id = "https://kubernetes.io/namespaces/kardinal-system/serviceaccounts/kardinal-promoter"
	const issuer = "https://oidc.example.com/id/1"
	ss, err := receipttest.NewSigstore(id, issuer)
	require.NoError(t, err)
	defer ss.Close()
	token := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(token, []byte(receipttest.Token("system:serviceaccount:kardinal-system:kardinal-promoter")+"\n"), 0o600))
	s := &Signer{Mode: v1alpha1.ReceiptSigningKeyless, FulcioURL: ss.Fulcio, TSAURL: ss.TSA, TokenFile: token}
	raw, _, err := Build(input("config"))
	require.NoError(t, err)
	signed, err := s.Sign(context.Background(), raw)
	require.NoError(t, err)
	assert.Equal(t, v1alpha1.ReceiptSigner{Mode: v1alpha1.ReceiptSigningKeyless, Identity: id, Issuer: issuer}, signed.SignedBy)

	v, err := Verify(signed.Bundle, VerifyOptions{Identity: id, Issuer: issuer, TrustedRoot: ss.Root, Subject: "3d1e"})
	require.NoError(t, err)
	assert.Equal(t, id, v.Signer)

	for name, o := range map[string]VerifyOptions{
		"wrong identity": {Identity: id + "x", Issuer: issuer, TrustedRoot: ss.Root},
		"wrong issuer":   {Identity: id, Issuer: issuer + "x", TrustedRoot: ss.Root},
		"no issuer":      {Identity: id, TrustedRoot: ss.Root},
		"no root":        {Identity: id, Issuer: issuer},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Verify(signed.Bundle, o)
			require.Error(t, err)
		})
	}
}

// TestSigner_Validate: a configuration that cannot sign is refused before
// any call: keyless without proof of time, an unknown mode, and a KMS
// reference whose provider the controller was not built with (it would
// otherwise reach the CLI-plugin fallback, a subprocess).
//
// Covers RECEIPT-SIGN-03.
func TestSigner_Validate(t *testing.T) {
	cases := map[string]struct {
		s    Signer
		want string
	}{
		"keyless without time":   {Signer{Mode: "keyless", FulcioURL: "https://f", TokenFile: "/t"}, "Rekor or a timestamp authority"},
		"keyless without fulcio": {Signer{Mode: "keyless", TokenFile: "/t", RekorURL: "https://r"}, "Fulcio URL"},
		"key without key":        {Signer{Mode: "key"}, "key Secret"},
		"unknown":                {Signer{Mode: "pgp"}, "unknown receipt signing mode"},
		"kms not built":          {Signer{Mode: "kms", KMSRef: "awskms:///alias/x"}, "not built with the awskms KMS provider"},
		"kms plugin":             {Signer{Mode: "kms", KMSRef: "myplugin://key"}, "not built with the myplugin KMS provider"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := tc.s.Sign(context.Background(), []byte("{}"))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

// TestParsePrivateKey refuses keys other than unencrypted ECDSA P-256.
func TestParsePrivateKey(t *testing.T) {
	priv, _, err := receipttest.Key()
	require.NoError(t, err)
	_, err = ParsePrivateKey(priv)
	require.NoError(t, err)
	for name, raw := range map[string]string{
		"not pem":   "hello",
		"encrypted": "-----BEGIN ENCRYPTED PRIVATE KEY-----\nAAAA\n-----END ENCRYPTED PRIVATE KEY-----\n",
		"garbage":   "-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ParsePrivateKey([]byte(raw))
			require.Error(t, err)
		})
	}
}

// TestName bounds receipt names.
func TestName(t *testing.T) {
	assert.Equal(t, "checkout-1-5-0-prod", Name("checkout-1-5-0", "Prod"))
	long := Name(strings.Repeat("b", 250), "prod")
	assert.Len(t, long, 253)
	assert.NotEqual(t, long, Name(strings.Repeat("b", 250), "prod-eu"))
}
