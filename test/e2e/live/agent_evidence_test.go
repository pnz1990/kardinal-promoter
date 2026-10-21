//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"encoding/base64"
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
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

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
//  8. From the pack's raw records alone, V's Verdict traces to V: its
//     spec.run names an AgentRun of agentruns.ndjson whose status names V
//     and its sponsor, its spec.by is that run's ServiceAccount, and the
//     receipt verified offline in step 6 names the same agent and sponsor.
//
// Covers AGENT-EVIDENCE-01, AGENT-EVIDENCE-02, AGENT-EVIDENCE-03, AGENT-EVIDENCE-04, AGENT-EVIDENCE-05.
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
	runs := waitRuns(t, e, a.ns, bundle, 1)
	require.Equal(t, verifier, runs[0].Spec.Template, "the AgentRun runs V")
	e.WaitGateReady(t, a.ns, bundle, "prod", journeyVerify, true, "verified: 1 of 1", gateTimeout)

	crName := "prod-change-prod--" + bundle
	inst := waitChangeRequest(t, e, a.ns, crName, "the prod change request opened its ticket", func(cr *v1alpha1.ChangeRequest) (bool, string) {
		return cr.Status.Ticket != nil && cr.Status.State == "pending", fmt.Sprintf("state %q conditions %v", cr.Status.State, cr.Status.Conditions)
	})
	ticket := inst.Status.Ticket

	r = author.Exec(framework.CLIOptions{}, author.Args(a.ns, "approve", bundle, "--env", "prod", "--field", "ticketID="+ticket.Key)...)
	require.Zero(t, r.Code, r.Output())
	own := approvalRecord(t, e, a.ns, bundle, "prod", journeyCAB, authorUser)
	assert.False(t, own.Counted, "the agent's approval of its own version does not count")
	assert.Contains(t, own.Reason, "own party")

	// alice approves while the ticket is still pending: the gate holds on
	// the change request alone, and prod gets no step, through a poll of
	// the ticket (pollInterval 30s) that reads it pending after her approval.
	const approver = "alice@example.com"
	alice := userKubeconfig(t, e, a.ns, approver, "release-managers")
	r = c.Exec(framework.CLIOptions{Kubeconfig: alice}, c.Args(a.ns, "approve", bundle, "--env", "prod", "--field", "ticketID="+ticket.Key)...)
	require.Zero(t, r.Code, r.Output())
	require.True(t, approvalRecord(t, e, a.ns, bundle, "prod", journeyCAB, approver).Counted, "alice's approval counts")
	approvedAt := time.Now()
	held := holdOnChangeRequest(t, e, a.ns, bundle, crName, approvedAt)
	assert.Contains(t, held.Status.Reason, "change request prod-change: ticket "+ticket.Key+" is pending",
		"the gate names what it waits for")

	e.SetFakeSnowTicket(t, ticket.Key, map[string]interface{}{"state": "-2", "approval": "approved",
		"approvers": []map[string]string{{"name": "Jane Smith", "state": "approved"}}})
	e.WaitGateReady(t, a.ns, bundle, "prod", journeyCAB, true, "", gateTimeout)

	// 3. The version promotes.
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "WaitingForMerge", promoteTimeout)
	pr := a.openPR(t, bundle, "prod")
	a.merge(t, pr)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)

	// The order of events: prod's promotion started only after the change
	// request was approved.
	crApproved := auditEventAt(t, e, a.ns, bundle, "ChangeRequestApproved", "prod")
	prodStarted := auditEventAt(t, e, a.ns, bundle, "PromotionStarted", "prod")
	assert.True(t, crApproved.Before(prodStarted), "ChangeRequestApproved (%s) before prod's PromotionStarted (%s)",
		crApproved.Format(time.RFC3339Nano), prodStarted.Format(time.RFC3339Nano))
	assert.True(t, approvedAt.Before(crApproved), "the ticket was approved after alice's approval")

	// 4. The signed receipt. Its content is checked on the statement the
	// envelope signs (base64 DSSE payload), which is the CR's byte for byte.
	rc := promotionReceipt(t, e, a.ns, bundle, "prod", v1alpha1.ReceiptStored)
	signed := signedPayload(t, rc.Status.Bundle)
	require.Equal(t, rc.Spec.Statement, string(signed), "the envelope signs the CR's statement")
	st, err := receipt.Parse(signed)
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
	assert.ElementsMatch(t, []string{receipt.LevelGatesPassed, receipt.LevelApproved, receipt.LevelVerifiedByAgent,
		receipt.LevelHealthy, "SLSA_BUILD_LEVEL_3"}, st.Predicate.VerifiedLevels, "exactly these levels")
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
	verdicts := ours("verdicts.ndjson")
	require.Len(t, verdicts, 1, "V's verdict")
	vspec := verdicts[0]["spec"].(map[string]interface{})
	assert.Equal(t, bundle, vspec["bundle"])
	assert.Equal(t, "pass", vspec["result"])
	// V's AgentRun wrote it: admission binds it to that run (spec.run), which
	// is how it counts as V's; such a Verdict has no actor stamp.
	if run, _ := vspec["run"].(map[string]interface{}); assert.NotNil(t, run, "the verdict's run") {
		assert.Equal(t, runs[0].Name, run["name"], "the verdict is bound to V's AgentRun")
	}
	crs := ours("changerequests.ndjson")
	require.Len(t, crs, 1, "the prod change request instance")
	assert.Equal(t, ticket.Key, crs[0]["status"].(map[string]interface{})["ticket"].(map[string]interface{})["key"])
	instances := map[string]bool{}
	for _, g := range ours("policygates.ndjson") {
		md := g["metadata"].(map[string]interface{})
		labels, _ := md["labels"].(map[string]interface{})
		if labels["kardinal.io/bundle"] == bundle && labels["kardinal.io/environment"] == "prod" {
			instances[fmt.Sprint(labels["kardinal.io/gate-template"])] = true
		}
	}
	for _, name := range []string{attestGate, journeyVerify, journeyCAB} {
		assert.True(t, instances[name], "the pack holds prod's %s gate instance (has %v)", name, instances)
	}
	var prodEnvelope string
	for _, rr := range ours("receipts.ndjson") {
		md := rr["metadata"].(map[string]interface{})
		if md["name"] == rc.Name {
			prodEnvelope, _ = rr["status"].(map[string]interface{})["bundle"].(string)
		}
	}
	require.NotEmpty(t, prodEnvelope, "prod's receipt is in the pack with its signed envelope")
	actions := map[string]bool{}
	// auditevents.ndjson is kardinal audit export's format: namespace at the
	// top level, not under metadata.
	for _, ev := range ndjson(t, dir, "auditevents.ndjson") {
		if spec := ev["spec"].(map[string]interface{}); ev["namespace"] == a.ns && spec["bundleName"] == bundle {
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
	assert.Contains(t, vout, "  approved by: "+approver+"\n", "alice once, and not the agent's uncounted approval")
	assert.Contains(t, vout, "  verdicts by: agent "+verifier+" for bob@example.com\n")
	verifiedOut := vout
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

	// 8. The pack's raw records name V without the cluster (#2295): V's
	// Verdict has no actor stamp, its run does. The receipt said the same in
	// step 6, offline: one line, built from the pack's AgentRun.
	run, _ := vspec["run"].(map[string]interface{})
	require.NotNil(t, run, "V's verdict names its run")
	var packedRun map[string]interface{}
	for _, ar := range ours("agentruns.ndjson") {
		if ar["metadata"].(map[string]interface{})["name"] == run["name"] {
			packedRun = ar
		}
	}
	require.NotNil(t, packedRun, "agentruns.ndjson holds the AgentRun V's verdict names (%v)", run["name"])
	rstatus := packedRun["status"].(map[string]interface{})
	assert.Equal(t, verifier, rstatus["agent"], "the pack's AgentRun names V")
	assert.Equal(t, "bob@example.com", rstatus["sponsor"], "and V's sponsor")
	assert.Equal(t, "system:serviceaccount:"+a.ns+":"+fmt.Sprint(rstatus["serviceAccount"]), vspec["by"],
		"the verdict was written by the run's own ServiceAccount")
	assert.Contains(t, verifiedOut, fmt.Sprintf("  verdicts by: agent %s for %s\n", rstatus["agent"], rstatus["sponsor"]),
		"the offline receipt names the agent and sponsor the pack's AgentRun names")
}

// TestAgentEvidence_RecreatedRun (#2345): V's AgentRun is deleted after its
// Verdict was recorded, while prod still waits on a person's approval, and
// kro creates it again, with spec.recorded (it does not run again). The run
// created again names V and its sponsor, as the first did and as the
// Verdict's VerdictRecorded AuditEvent says, so V's Verdict still counts:
// the verify gate passes on it again, as V's, and stays passed. When V's
// StepTemplate names another agent (W) instead, the run created again names
// none, and the Verdict stops counting: V's run's Verdict is never counted
// as W's. With V back, it counts again; prod promotes with it counted in
// its receipt, and the evidence pack traces the Verdict to V through the
// run created again.
//
// Covers AGENT-EVIDENCE-06.
func TestAgentEvidence_RecreatedRun(t *testing.T) {
	e := framework.New(t)
	c := e.CLI(t)
	ctx := context.Background()
	a := newArgoApp(t, e, "uat", "prod")
	const sponsor = "bob@example.com"
	verifier := registerAgent(t, e, a.ns, "qa", sponsor, nil)
	e.CreateGate(t, verifyGate(a.ns, v1alpha1.GateVerifyPolicy{Required: 1, AllowedAgents: []string{verifier},
		Run: &v1alpha1.GateVerifyRun{Template: verifier, Count: 1, Namespace: a.ns}}))
	people := framework.Gate(a.ns, "people", "prod", "true", recheck)
	people.Spec.Approval = &v1alpha1.GateApprovalPolicy{Required: 1}
	e.CreateGate(t, people)
	a.apply(t, a.pipeline(nil))
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "uat", "Verified", promoteTimeout)
	first := waitRuns(t, e, a.ns, bundle, 1)[0]
	require.Equal(t, verifier, first.Status.Agent)
	e.WaitGateReady(t, a.ns, bundle, "prod", journeyVerify, true, "verified: 1 of 1", gateTimeout)
	e.WaitGateReady(t, a.ns, bundle, "prod", "people", false, "", gateTimeout)

	// Mid-gate, the run is deleted as kro deletes one (only kardinal may:
	// the graph-objects admission policy), as when a Graph is applied again
	// without it.
	cfg := rest.CopyConfig(e.Config)
	cfg.Impersonate = rest.ImpersonationConfig{UserName: framework.ServiceAccountUser(a.ns, "kardinal-graph")}
	asKro, err := client.New(cfg, client.Options{Scheme: e.Client.Scheme()})
	require.NoError(t, err)
	recreate := func(prev v1alpha1.AgentRun) v1alpha1.AgentRun {
		t.Helper()
		require.NoError(t, asKro.Delete(ctx, &prev))
		var again v1alpha1.AgentRun
		framework.Eventually(t, agentRunTimeout, "kro creates "+prev.Name+" again, and it finishes", func(ctx context.Context) (bool, string) {
			if err := e.Client.Get(ctx, client.ObjectKeyFromObject(&prev), &again); err != nil {
				return false, err.Error()
			}
			return again.UID != prev.UID && again.Status.Phase != "",
				fmt.Sprintf("uid %s (before %s), phase %q: %s", again.UID, prev.UID, again.Status.Phase, again.Status.Message)
		})
		return again
	}
	again := recreate(first)
	assert.Equal(t, v1alpha1.AgentRunSucceeded, again.Status.Phase)
	assert.Equal(t, "pass", again.Spec.Recorded, "the Graph renders its Verdict's result")
	assert.Contains(t, again.Status.Message, "verdict already recorded (pass); not run again")
	assert.Equal(t, verifier, again.Status.Agent, "the run created again names V")
	assert.Equal(t, sponsor, again.Status.Sponsor, "and V's sponsor")
	assert.Equal(t, "kr-"+first.Name, again.Status.ServiceAccount, "and the ServiceAccount that wrote the Verdict")
	assert.Empty(t, again.Status.JobName, "it does not run again")

	// V's Verdict counts again, as V's, and keeps counting while prod waits
	// on the approval.
	counted := func(ctx context.Context) (bool, string) {
		g, ok, err := e.GateInstance(ctx, a.ns, bundle, "prod", journeyVerify)
		if err != nil || !ok {
			return false, fmt.Sprintf("gate instance: found=%v %v", ok, err)
		}
		v := g.Status.Verdicts
		return g.Status.Ready && len(v) == 1 && v[0].Counted && v[0].Verifier == "agent "+verifier+" for "+sponsor,
			fmt.Sprintf("ready=%v (%s), verdicts %+v", g.Status.Ready, g.Status.Reason, v)
	}
	framework.Eventually(t, gateTimeout, "the verify gate passes on V's Verdict, through the run created again", counted)
	framework.Consistently(t, 30*time.Second, "the verify gate keeps passing on V's Verdict", counted)

	// V's StepTemplate names agent W now (also allowed): the run created
	// again names no agent, since the Verdict's record names V, and the
	// Verdict stops counting rather than count as W's.
	other := registerAgent(t, e, a.ns, "qa2", sponsor, nil)
	pointAt := func(agent string) {
		t.Helper()
		var tmpl v1alpha1.StepTemplate
		require.NoError(t, e.Client.Get(ctx, client.ObjectKey{Name: verifier}, &tmpl))
		tmpl.Spec.Agent = agent
		require.NoError(t, e.Client.Update(ctx, &tmpl))
	}
	var gate v1alpha1.PolicyGate
	require.NoError(t, e.Client.Get(ctx, client.ObjectKey{Namespace: a.ns, Name: journeyVerify}, &gate))
	gate.Spec.Verify.AllowedAgents = append(gate.Spec.Verify.AllowedAgents, other)
	require.NoError(t, e.Client.Update(ctx, &gate))
	pointAt(other)
	changed := recreate(again)
	assert.Equal(t, v1alpha1.AgentRunSucceeded, changed.Status.Phase)
	assert.Contains(t, changed.Status.Message, "it names no agent: agent changed since the run, or unknown")
	assert.Empty(t, changed.Status.Agent, "neither V, whose run it is not, nor W, whose Verdict it is not")
	assert.Empty(t, changed.Status.Sponsor)
	e.WaitGateReady(t, a.ns, bundle, "prod", journeyVerify, false, "0 of 1", gateTimeout)

	// V again: the run created again names V, as the record does.
	pointAt(verifier)
	again = recreate(changed)
	assert.Equal(t, verifier, again.Status.Agent)
	assert.Equal(t, sponsor, again.Status.Sponsor)
	framework.Eventually(t, gateTimeout, "the verify gate passes on V's Verdict again", counted)

	// prod promotes on it.
	r := c.Exec(framework.CLIOptions{Kubeconfig: userKubeconfig(t, e, a.ns, "carol@example.com")}, c.Args(a.ns, "approve", bundle, "--env", "prod")...)
	require.Zero(t, r.Code, r.Output())
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)
	rc := promotionReceipt(t, e, a.ns, bundle, "prod", v1alpha1.ReceiptStored)
	st, err := receipt.Parse(signedPayload(t, rc.Status.Bundle))
	require.NoError(t, err)
	var verdicts []receipt.Verdict
	for _, g := range st.Predicate.Kardinal.Gates {
		if strings.HasSuffix(g.Template, "/"+journeyVerify) {
			verdicts = g.Verdicts
		}
	}
	require.Len(t, verdicts, 1, "prod's receipt records V's verdict")
	assert.True(t, verdicts[0].Counted, verdicts[0].Reason)
	assert.Equal(t, "agent "+verifier+" for "+sponsor, verdicts[0].Verifier)

	// The pack traces the Verdict to V through the run created again.
	pack := filepath.Join(t.TempDir(), "pack.tar.gz")
	r = c.Exec(framework.CLIOptions{Kubeconfig: auditorKubeconfig(t, e, "", "auditor-"+a.ns+"@example.com")},
		c.Args(a.ns, "audit", "evidence-pack", "--since", "1h", "-o", pack)...)
	require.Zero(t, r.Code, r.Output())
	dir, _ := packOf(t, pack)
	var vspec map[string]interface{}
	for _, v := range ndjson(t, dir, "verdicts.ndjson") {
		if md := v["metadata"].(map[string]interface{}); md["namespace"] == a.ns && md["name"] == first.Name {
			vspec = v["spec"].(map[string]interface{})
		}
	}
	require.NotNil(t, vspec, "the pack holds V's Verdict")
	run, _ := vspec["run"].(map[string]interface{})
	require.NotNil(t, run, "bound to its run")
	var packed map[string]interface{}
	for _, ar := range ndjson(t, dir, "agentruns.ndjson") {
		if md := ar["metadata"].(map[string]interface{}); md["namespace"] == a.ns && md["name"] == run["name"] {
			packed = ar
		}
	}
	require.NotNil(t, packed, "agentruns.ndjson holds the run V's Verdict names (%v)", run["name"])
	assert.Equal(t, string(again.UID), packed["metadata"].(map[string]interface{})["uid"], "the run created again")
	pst := packed["status"].(map[string]interface{})
	assert.Equal(t, verifier, pst["agent"], "the pack's run names V")
	assert.Equal(t, sponsor, pst["sponsor"], "and V's sponsor")
	assert.Equal(t, framework.ServiceAccountUser(a.ns, fmt.Sprint(pst["serviceAccount"])), vspec["by"],
		"the Verdict was written by the run's own ServiceAccount")
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

// packRecordFiles are the files of records the pack writes, as its manifest
// lists them, empty or not: the .ndjson files (admission.yaml holds the
// chart's policies, not kardinal records; manifest.json, SHA256SUMS and
// controls.json describe the pack).
func packRecordFiles(t *testing.T, m map[string]interface{}) []string {
	t.Helper()
	listed, _ := m["files"].([]interface{})
	require.NotEmpty(t, listed, "the manifest lists the pack's files")
	var out []string
	for _, f := range listed {
		if name := fmt.Sprint(f.(map[string]interface{})["name"]); strings.HasSuffix(name, ".ndjson") {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// holdOnChangeRequest checks, for changeRequestHold, that prod's cab gate
// stays not ready and prod gets no step while the change request instance
// crName reads its ticket pending, and that the instance read it at least
// once after since (its observedAt): the gate held on the change request
// alone. It returns the gate as last seen.
func holdOnChangeRequest(t *testing.T, e *framework.Env, ns, bundle, crName string, since time.Time) *v1alpha1.PolicyGate {
	t.Helper()
	ctx := context.Background()
	var gate *v1alpha1.PolicyGate
	read := false
	for start := time.Now(); time.Since(start) < changeRequestHold || !read; time.Sleep(2 * time.Second) {
		require.Less(t, time.Since(start), 2*changeRequestHold, "the change request was not read again while it held")
		g, ok, err := e.GateInstance(ctx, ns, bundle, "prod", journeyCAB)
		require.NoError(t, err)
		require.True(t, ok, "prod's cab gate instance")
		require.False(t, g.Status.Ready, "cab became ready while the change request was pending: %s", g.Status.Reason)
		_, has, err := e.Step(ctx, ns, pipelineName, bundle, "prod")
		require.NoError(t, err)
		require.False(t, has, "prod got a step while the change request was pending")
		var cr v1alpha1.ChangeRequest
		require.NoError(t, e.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: crName}, &cr))
		require.Equal(t, "pending", cr.Status.State, "the ticket is still pending in the fake")
		if cr.Status.ObservedAt != nil && cr.Status.ObservedAt.After(since) {
			read = true
		}
		gate = g
	}
	return gate
}

// changeRequestHold is how long holdOnChangeRequest watches: more than the
// template's pollInterval (30s), so the instance reads the pending ticket
// after the approval.
const changeRequestHold = 40 * time.Second

// auditEventAt is when the AuditEvent action of bundle in env happened: its
// kardinal.io/created-at annotation (nanoseconds; spec.timestamp has
// seconds).
func auditEventAt(t *testing.T, e *framework.Env, ns, bundle, action, env string) time.Time {
	t.Helper()
	var at time.Time
	framework.Eventually(t, time.Minute, "the "+action+" AuditEvent of "+bundle+" in "+env, func(ctx context.Context) (bool, string) {
		var list v1alpha1.AuditEventList
		if err := e.Client.List(ctx, &list, client.InNamespace(ns)); err != nil {
			return false, err.Error()
		}
		for _, ev := range list.Items {
			if ev.Spec.BundleName != bundle || ev.Spec.Action != action || ev.Spec.Environment != env {
				continue
			}
			ts, err := time.Parse(time.RFC3339Nano, ev.Annotations["kardinal.io/created-at"])
			if err != nil {
				return false, fmt.Sprintf("%s: created-at %q: %v", ev.Name, ev.Annotations["kardinal.io/created-at"], err)
			}
			at = ts
			return true, ""
		}
		return false, fmt.Sprintf("%d events", len(list.Items))
	})
	return at
}

// signedPayload is the statement a Sigstore bundle's DSSE envelope signs.
func signedPayload(t *testing.T, envelope string) []byte {
	t.Helper()
	var b struct {
		DSSE struct {
			Payload string `json:"payload"`
		} `json:"dsseEnvelope"`
	}
	require.NoError(t, json.Unmarshal([]byte(envelope), &b))
	raw, err := base64.StdEncoding.DecodeString(b.DSSE.Payload)
	require.NoError(t, err)
	return raw
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
