//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/receipt"
	iv "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/imageverification"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/imageverification/signtest"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
)

// The prod gates of the journey.
const (
	journeyCAB    = "cab"        // the change request and a person's approval naming its ticket
	journeyVerify = "explore-qa" // an agent verifier's verdict (verifyGate)
)

// TestAgentEvidence_Journey is the v0.13 acceptance test (#2062): an agent's
// version reaches prod through every kind of evidence v0.13 records, and
// the evidence holds up without the cluster.
//
//  1. Agent A (an AgentIdentity, autonomy auto, sponsor S) opens a version
//     with its own token: kardinal create bundle by digest, with a commit.
//  2. prod's gates run. Attestations: the image's SLSA provenance must be
//     level 3 from the version's commit, and its scan, after the security
//     key's OpenVEX, must have no critical finding. An agent verifier: agent
//     V (another sponsor) runs as an AgentRun and its passing Verdict
//     counts. A ServiceNow change request (the fake ticket API) must be
//     approved, and a person's approval must name its ticket in the
//     ticketID field. A's own approval of its version is recorded and does
//     not count (its own party).
//  3. The version promotes: test, then prod through its PR.
//  4. prod's receipt is signed and names A and S, every gate as it was at
//     the decision, the counted approval with its field and A's uncounted
//     one, V's verdict, the approved and verified ticket, SLSA level 3 for
//     its only subject, and the PR. Its levels are GATES_PASSED, APPROVED,
//     VERIFIED_BY_AGENT, HEALTHY and SLSA_BUILD_LEVEL_3, and not OVERRIDDEN
//     or FREEZE_EXEMPTED.
//  5. A user bound only to the auditor roles exports an evidence pack:
//     sha256sum -c passes, nothing is forbidden or missing, and it holds the
//     version, both agents, both approvals, the verdict, the change request,
//     the gate instances, the receipts with their signed envelopes and the
//     version's AuditEvents.
//  6. kardinal verify-receipt verifies prod's receipt from the pack's
//     receipts.ndjson in a network namespace with no network, with every
//     level above required; one changed byte fails it.
//  7. The compliance mapping in the pack (controls.json) cites every
//     record: each file of the pack that holds a record is named by a
//     shipped row, and so is each action of the version's AuditEvents.
//
// Covers AGENT-EVIDENCE-01, AGENT-EVIDENCE-02, AGENT-EVIDENCE-03, AGENT-EVIDENCE-04.
func TestAgentEvidence_Journey(t *testing.T) {
	e := framework.New(t)
	c := e.CLI(t)
	ctx := context.Background()
	a := newArgoApp(t, e, "test", "prod")
	pub := receiptPublicKey(t, e)
	digest := fixtures.V2Digest
	image := fixtures.Image + "@" + digest

	// The image is signed by the release key and carries level-3 provenance
	// and a scan whose critical finding the security key's VEX says does not
	// apply (as TestStep_ImageVerificationAttestationsPassGate).
	attested := &attestedApp{app: a, sigs: newSignatures(t, a.ns)}
	var err error
	attested.release, err = signtest.NewKey()
	require.NoError(t, err)
	attested.security, err = signtest.NewKey()
	require.NoError(t, err)
	relPEM, err := attested.release.PublicPEM()
	require.NoError(t, err)
	secPEM, err := attested.security.PublicPEM()
	require.NoError(t, err)
	require.NoError(t, e.Client.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "security", Namespace: a.ns,
		Labels: map[string]string{"kardinal.io/referenceable": "true"}}, Data: map[string][]byte{"cosign.pub": secPEM}}))
	policy := keyPolicy(t, e, a.ns, attested.sigs, relPEM, "3m")
	policy.Authorities = append(policy.Authorities, v1alpha1.SignatureAuthority{Name: "security",
		Key: &v1alpha1.KeyAuthority{SecretRef: v1alpha1.SecretKeyName{Name: "security", Key: "cosign.pub"}}})
	policy.Attestations = []v1alpha1.AttestationPolicy{
		{Name: "provenance", PredicateType: iv.PredicateSLSAProvenance, Authorities: []string{"release"}, Required: true,
			SLSA: &v1alpha1.SLSAPolicy{Builders: []v1alpha1.SLSABuilder{{ID: attestBuilderL3, Level: 3}}}},
		{Name: "vulns", PredicateType: iv.PredicateCosignVuln, Authorities: []string{"release"}, Required: true,
			VEX: &v1alpha1.VEXPolicy{Authorities: []string{"security"}}},
	}
	sig, err := attested.release.Bundle(fixtures.Image, digest)
	require.NoError(t, err)
	attested.sigs.pushBundle(t, digest, sig)
	attested.attest(t, attested.release, iv.PredicateSLSAProvenance, attestProvenance(attestBuilderL3))
	attested.attest(t, attested.release, iv.PredicateCosignVuln, attestScan())
	attested.attest(t, attested.security, iv.PredicateOpenVEX, attestVEX())

	// The agents: A writes the version, V verifies it.
	sponsor := "sponsor-" + a.ns + "@example.com"
	author := policyAgent(t, e, a.ns, "author", sponsor, v1alpha1.AutonomyAuto)
	authorName, authorUser := a.ns+"-author", "system:serviceaccount:"+a.ns+":author"
	verifier := registerAgent(t, e, a.ns, "qa", "bob@example.com", nil)

	// prod's gates.
	e.CreateGate(t, framework.Gate(a.ns, attestGate, "prod", "attestations.provenance.slsa.buildLevel >= 3 && "+
		"attestations.provenance.slsa.sourceCommit == bundle.provenance.commitSHA && attestations.vulns.effective.critical == 0", ""))
	e.CreateGate(t, verifyGate(a.ns, v1alpha1.GateVerifyPolicy{Required: 1, AllowedAgents: []string{verifier},
		Run: &v1alpha1.GateVerifyRun{Template: verifier, Count: 1, Namespace: a.ns}}))
	snowTemplate(t, e, a.ns)
	cab := framework.Gate(a.ns, journeyCAB, "prod", `changeRequests["prod-change"].state == "approved" && `+
		`approvals.fields.ticketID.exists(id, id == changeRequests["prod-change"].ticket)`, recheck)
	// Agents may approve here, but never their own version: A's approval is
	// recorded and does not count.
	cab.Spec.Approval = &v1alpha1.GateApprovalPolicy{Required: 1, ExcludeAuthor: true, AllowAgents: true,
		AllowedAgents: []string{authorName}, Fields: []v1alpha1.ApprovalField{
			{Name: "ticketID", Required: true, Description: "ServiceNow change number"}}}
	e.CreateGate(t, cab)
	p := a.pipeline(map[string]string{"prod": "pr-review"})
	p.Spec.ImageVerification = policy
	a.apply(t, p)
	waitPipelineValid(t, e, a.ns, pipelineName)

	// 1. The agent opens the version.
	r := author.Exec(framework.CLIOptions{}, author.Args(a.ns, "create", "bundle", pipelineName, "--image", image,
		"--commit", attestCommit)...)
	require.Zero(t, r.Code, r.Output())
	bundle := bundleCreatedName(t, r.Stdout)
	var b v1alpha1.Bundle
	require.NoError(t, e.Client.Get(ctx, types.NamespacedName{Namespace: a.ns, Name: bundle}, &b))
	require.NotNil(t, b.Spec.Actor, "the agent's version carries its actor stamp")
	agentActor := v1alpha1.Actor{Kind: v1alpha1.ActorAgent, Principal: authorUser, Agent: authorName, Sponsor: sponsor}
	assert.Equal(t, agentActor.Kind, b.Spec.Actor.Kind)
	assert.Equal(t, agentActor.Agent, b.Spec.Actor.Agent)
	assert.Equal(t, agentActor.Sponsor, b.Spec.Actor.Sponsor)
	assert.Equal(t, agentActor.Principal, b.Spec.Actor.Principal)

	// 2. The gates.
	e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Verified", promoteTimeout)
	ivDone := waitImageVerification(t, e, a.ns, bundle, "Verified")
	e.WaitGateReady(t, a.ns, bundle, "prod", attestGate, true, "", promoteTimeout)
	waitRuns(t, e, a.ns, bundle, 1)
	e.WaitGateReady(t, a.ns, bundle, "prod", journeyVerify, true, "verified: 1 of 1", gateTimeout)

	crName := "prod-change-prod--" + bundle
	inst := waitChangeRequest(t, e, a.ns, crName, "the prod change request opened its ticket", func(cr *v1alpha1.ChangeRequest) (bool, string) {
		return cr.Status.Ticket != nil && cr.Status.State == "pending", fmt.Sprintf("state %q conditions %v", cr.Status.State, cr.Status.Conditions)
	})
	ticket := inst.Status.Ticket
	e.SetFakeSnowTicket(t, ticket.Key, map[string]interface{}{"state": "-2", "approval": "approved",
		"approvers": []map[string]string{{"name": "Jane Smith", "state": "approved"}}})

	r = author.Exec(framework.CLIOptions{}, author.Args(a.ns, "approve", bundle, "--env", "prod", "--field", "ticketID="+ticket.Key)...)
	require.Zero(t, r.Code, r.Output())
	own := approvalRecord(t, e, a.ns, bundle, "prod", journeyCAB, authorUser)
	assert.False(t, own.Counted, "the agent's approval of its own version does not count")
	assert.Contains(t, own.Reason, "own party")
	a.noStep(t, bundle, "prod", 10*time.Second)

	const approver = "alice@example.com"
	alice := userKubeconfig(t, e, a.ns, approver, "release-managers")
	r = c.Exec(framework.CLIOptions{Kubeconfig: alice}, c.Args(a.ns, "approve", bundle, "--env", "prod", "--field", "ticketID="+ticket.Key)...)
	require.Zero(t, r.Code, r.Output())
	e.WaitGateReady(t, a.ns, bundle, "prod", journeyCAB, true, "", gateTimeout)

	// 3. The version promotes.
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "WaitingForMerge", promoteTimeout)
	pr := a.openPR(t, bundle, "prod")
	a.merge(t, pr)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)

	// 4. The signed receipt.
	rc := promotionReceipt(t, e, a.ns, bundle, "prod", v1alpha1.ReceiptStored)
	st, err := receipt.Parse([]byte(rc.Spec.Statement))
	require.NoError(t, err)
	rec := st.Predicate.Kardinal
	assert.True(t, rec.DecisionSnapshot, "the gates as they were at the decision")
	require.NotNil(t, rec.Actor)
	assert.Equal(t, agentActor.Agent, rec.Actor.Agent)
	assert.Equal(t, agentActor.Sponsor, rec.Actor.Sponsor)
	assert.Equal(t, v1alpha1.ActorAgent, rec.Actor.Kind)
	require.Len(t, st.Subject, 1, "the image is the receipt's only subject")
	assert.Equal(t, strings.TrimPrefix(digest, "sha256:"), st.Subject[0].Digest["sha256"])
	gates := map[string]receipt.Gate{}
	for _, g := range rec.Gates {
		gates[g.Template[strings.LastIndex(g.Template, "/")+1:]] = g
	}
	for _, name := range []string{attestGate, journeyVerify, journeyCAB} {
		require.Contains(t, gates, name, "the receipt records prod's gate %s", name)
		assert.Equal(t, "pass", gates[name].Result, name)
	}
	approvals := map[string]receipt.Approval{}
	for _, ap := range gates[journeyCAB].Approvals {
		approvals[ap.User] = ap
	}
	require.Contains(t, approvals, approver)
	assert.True(t, approvals[approver].Counted)
	assert.Equal(t, map[string]string{"ticketID": ticket.Key}, approvals[approver].Fields)
	require.Contains(t, approvals, authorUser, "the agent's own approval is recorded")
	assert.False(t, approvals[authorUser].Counted)
	assert.Contains(t, approvals[authorUser].Reason, "own party")
	require.NotEmpty(t, gates[journeyVerify].Verdicts)
	verdict := gates[journeyVerify].Verdicts[0]
	assert.True(t, verdict.Counted, verdict.Reason)
	assert.Equal(t, "pass", verdict.Result)
	assert.Equal(t, "agent "+verifier+" for bob@example.com", verdict.Verifier)
	require.Len(t, rec.ChangeRequests, 1)
	cr := rec.ChangeRequests[0]
	assert.Equal(t, ticket.Key, cr.Ticket)
	assert.Equal(t, "approved", cr.State)
	assert.True(t, cr.Verified, "kardinal opened the ticket with the promotion's correlation id")
	assert.Equal(t, []string{"Jane Smith"}, cr.Approvers)
	require.NotNil(t, rec.Checks.SLSA)
	assert.Equal(t, 3, rec.Checks.SLSA.BuildLevel)
	assert.Empty(t, rec.Checks.SLSA.OtherSubjects)
	require.NotNil(t, rec.Change.PR)
	assert.Equal(t, pr.Number, rec.Change.PR.Number)
	levels := st.Predicate.VerifiedLevels
	for _, want := range []string{receipt.LevelGatesPassed, receipt.LevelApproved, receipt.LevelVerifiedByAgent,
		receipt.LevelHealthy, "SLSA_BUILD_LEVEL_3"} {
		assert.Contains(t, levels, want)
	}
	assert.NotContains(t, levels, receipt.LevelOverridden)
	assert.NotContains(t, levels, receipt.LevelFreezeExempted)
	verified := map[string]bool{}
	for _, at := range ivDone.Status.Attestations {
		verified[at.Name] = at.Verified
	}
	assert.Equal(t, map[string]bool{"provenance": true, "vulns": true}, verified,
		"the provenance and the scan verified (the VEX is the scan's, not an attestation result of its own)")

	// 5. The evidence pack, exported by an auditor.
	auditor := "auditor-" + a.ns + "@example.com"
	pack := filepath.Join(t.TempDir(), "pack.tar.gz")
	r = c.Exec(framework.CLIOptions{Kubeconfig: auditorKubeconfig(t, e, "", auditor)},
		c.Args(a.ns, "audit", "evidence-pack", "--since", "1h", "-o", pack)...)
	require.Zero(t, r.Code, r.Output())
	dir, m := packOf(t, pack)
	sums := exec.Command("sha256sum", "-c", "SHA256SUMS")
	sums.Dir = dir
	out, err := sums.CombinedOutput()
	require.NoError(t, err, "sha256sum -c passes on the pack as exported:\n%s", out)
	assert.Equal(t, auditor, m["exportedBy"])
	coverage := m["coverage"].(map[string]interface{})
	assert.Empty(t, coverage["forbidden"], "the auditor roles read everything the pack holds")
	assert.Empty(t, coverage["missing"])
	for _, w := range warningsOf(coverage) {
		assert.NotContains(t, w, "receipt", "no receipt is withheld: %s", w)
	}

	ours := func(file string) []map[string]interface{} {
		var out []map[string]interface{}
		for _, rec := range ndjson(t, dir, file) {
			if md, _ := rec["metadata"].(map[string]interface{}); md["namespace"] == a.ns {
				out = append(out, rec)
			}
		}
		return out
	}
	assert.Equal(t, []string{a.ns + "/" + bundle}, names(ours("bundles.ndjson")))
	packed := ours("bundles.ndjson")[0]["spec"].(map[string]interface{})["actor"].(map[string]interface{})
	assert.Equal(t, authorName, packed["agent"], "the pack's version names its agent")
	assert.Equal(t, sponsor, packed["sponsor"])
	agents := map[string]bool{}
	for _, rec := range ndjson(t, dir, "agentidentities.ndjson") {
		agents[fmt.Sprint(rec["metadata"].(map[string]interface{})["name"])] = true
	}
	assert.True(t, agents[authorName] && agents[verifier], "both agents are in the pack's agent registry: %v", agents)
	users := []string{}
	for _, ap := range ours("approvals.ndjson") {
		users = append(users, fmt.Sprint(ap["spec"].(map[string]interface{})["user"]))
	}
	assert.ElementsMatch(t, []string{approver, authorUser}, users, "both approvals")
	require.Len(t, ours("verdicts.ndjson"), 1, "V's verdict")
	crs := ours("changerequests.ndjson")
	require.Len(t, crs, 1, "the prod change request instance")
	assert.Equal(t, ticket.Key, crs[0]["status"].(map[string]interface{})["ticket"].(map[string]interface{})["key"])
	assert.Len(t, ours("policygates.ndjson"), 3, "prod's three gate instances")
	var prodEnvelope string
	for _, rr := range ours("receipts.ndjson") {
		md := rr["metadata"].(map[string]interface{})
		if md["name"] == rc.Name {
			prodEnvelope, _ = rr["status"].(map[string]interface{})["bundle"].(string)
		}
	}
	require.NotEmpty(t, prodEnvelope, "prod's receipt is in the pack with its signed envelope")
	actions := map[string]bool{}
	for _, ev := range ours("auditevents.ndjson") {
		if spec := ev["spec"].(map[string]interface{}); spec["bundleName"] == bundle {
			actions[fmt.Sprint(spec["action"])] = true
		}
	}
	for _, want := range []string{"PromotionStarted", "PromotionSucceeded", "ApprovalRecorded", "VerdictRecorded",
		"ChangeRequestOpened", "ChangeRequestApproved", "PRMerged"} {
		assert.True(t, actions[want], "the pack holds the version's %s AuditEvent (has %v)", want, actions)
	}

	// 6. The receipt verifies from the pack alone, with no network.
	file := filepath.Join(t.TempDir(), "prod.sigstore.json")
	require.NoError(t, os.WriteFile(file, []byte(prodEnvelope), 0o600))
	args := []string{"verify-receipt", "--file", file, "--key", pub, "-n", a.ns, "--pipeline", pipelineName, "--env", "prod",
		"--subject", digest, "--cluster", clusterIdentity(t, e),
		"--require", "GATES_PASSED,APPROVED,VERIFIED_BY_AGENT,HEALTHY,SLSA_BUILD_LEVEL_3,!OVERRIDDEN,!FREEZE_EXEMPTED"}
	vout, err := offline(t, os.Getenv(framework.EnvCLI), args...)
	require.NoError(t, err, vout)
	assert.Contains(t, vout, "receipt "+rc.Name+": every check below that was made passed")
	tampered := filepath.Join(t.TempDir(), "tampered.sigstore.json")
	require.NoError(t, os.WriteFile(tampered, flipPayloadByte(t, prodEnvelope), 0o600))
	args[2] = tampered
	vout, err = offline(t, os.Getenv(framework.EnvCLI), args...)
	require.Error(t, err, "one changed byte fails it:\n%s", vout)
	assert.Contains(t, vout, "NOT VERIFIED")

	// 7. The compliance mapping cites every record.
	files, cited := citations(t, dir)
	for _, f := range packRecordFiles(t, m) {
		assert.True(t, files[f], "controls.json: no shipped row cites %s, which holds records (cited: %v)", f, sortedKeys(files))
	}
	for act := range actions {
		assert.True(t, cited[act], "controls.json: no shipped row cites the version's %s AuditEvent", act)
	}
}

// warningsOf are the coverage warnings of a pack's manifest.
func warningsOf(coverage map[string]interface{}) []string {
	var out []string
	ws, _ := coverage["warnings"].([]interface{})
	for _, w := range ws {
		out = append(out, fmt.Sprint(w))
	}
	return out
}

// citations are the files and the AuditEvent actions the shipped rows of
// the pack's controls.json name.
func citations(t *testing.T, dir string) (files, actions map[string]bool) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "controls.json"))
	require.NoError(t, err)
	var doc struct {
		Frameworks []struct {
			Controls []struct {
				Evidence []struct {
					Status  string   `json:"status"`
					Files   []string `json:"files"`
					Actions []string `json:"actions"`
				} `json:"evidence"`
			} `json:"controls"`
		} `json:"frameworks"`
	}
	require.NoError(t, json.Unmarshal(raw, &doc))
	files, actions = map[string]bool{}, map[string]bool{}
	for _, f := range doc.Frameworks {
		for _, c := range f.Controls {
			for _, ev := range c.Evidence {
				if ev.Status != "shipped" {
					continue
				}
				for _, x := range ev.Files {
					files[x] = true
				}
				for _, x := range ev.Actions {
					actions[x] = true
				}
			}
		}
	}
	require.NotEmpty(t, files, "controls.json has shipped rows")
	return files, actions
}

// packRecordFiles are the files of the pack, as its manifest lists them,
// that hold at least one record: the .ndjson files (admission.yaml holds
// the chart's policies, not kardinal records; manifest.json, SHA256SUMS and
// controls.json describe the pack).
func packRecordFiles(t *testing.T, m map[string]interface{}) []string {
	t.Helper()
	listed, _ := m["files"].([]interface{})
	require.NotEmpty(t, listed, "the manifest lists the pack's files")
	var out []string
	for _, f := range listed {
		file := f.(map[string]interface{})
		name := fmt.Sprint(file["name"])
		if n, _ := file["records"].(float64); strings.HasSuffix(name, ".ndjson") && n > 0 {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// flipPayloadByte changes one byte of a Sigstore bundle's signed DSSE
// payload, so its signature no longer covers it.
func flipPayloadByte(t *testing.T, envelope string) []byte {
	t.Helper()
	var b map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(envelope), &b))
	dsse := b["dsseEnvelope"].(map[string]interface{})
	payload := []byte(fmt.Sprint(dsse["payload"]))
	i := len(payload) / 2
	if payload[i] == 'A' {
		payload[i] = 'B'
	} else {
		payload[i] = 'A'
	}
	dsse["payload"] = string(payload)
	out, err := json.Marshal(b)
	require.NoError(t, err)
	return out
}

// sortedKeys are m's keys, sorted.
func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
