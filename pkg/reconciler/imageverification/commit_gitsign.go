// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package imageverification

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/sigstore/sigstore-go/pkg/verify"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// How a commit was verified (status.commit.method, commit.method in CEL).
const (
	CommitMethodSCM     = "scm"
	CommitMethodGitsign = "gitsign"
)

// commitAuthorities are the policy's commits.authorities.
func commitAuthorities(iv *v1alpha1.ImageVerification) []string {
	if iv.Spec.Policy.Commits == nil {
		return nil
	}
	return iv.Spec.Policy.Commits.Authorities
}

// checkGitsignCommit verifies the config commit's gitsign signature against
// the keyless authorities names (docs/design/20-attestations.md §3.5): the
// signed payload and signature come from the SCM API; the commit SHA is
// recomputed from them, so the SCM cannot answer for another commit; the
// certificate, the signature and the embedded Rekor entry are verified
// offline with the authority's trusted root, as for keyless images. A
// signature that does not verify fails at once: a commit's signature
// cannot change. Errors reading the SCM or loading an authority are
// retried until the deadline.
func (r *Reconciler) checkGitsignCommit(ctx context.Context, iv *v1alpha1.ImageVerification,
	provider scm.SCMProvider, repo string, names []string) outcome {
	c := iv.Spec.Commit
	failed := func(msg string) outcome {
		iv.Status.Commit = &v1alpha1.CommitVerificationResult{Method: CommitMethodGitsign, Message: truncate(msg)}
		return outcome{failed: "commit " + c.SHA + ": " + msg, reason: ReasonCommitNotVerified}
	}
	waiting := func(msg, reason string) outcome {
		iv.Status.Commit = &v1alpha1.CommitVerificationResult{Method: CommitMethodGitsign, Message: truncate(msg)}
		return outcome{waiting: "commit " + c.SHA + ": " + msg, reason: reason}
	}
	pr, ok := provider.(scm.CommitPayloadReader)
	if !ok {
		return failed(scm.ErrCommitPayloadUnsupported.Error())
	}
	p, err := pr.CommitPayload(ctx, repo, c.SHA)
	switch {
	case err != nil && (scm.IsPermanentError(err) || errors.Is(err, scm.ErrCommitPayloadUnsupported)):
		return failed(err.Error())
	case err != nil:
		return waiting("SCM: "+err.Error(), "")
	case !strings.EqualFold(p.SHA, c.SHA):
		return failed(fmt.Sprintf("the SCM answered for commit %q, not %s", p.SHA, c.SHA))
	case p.Signature == "":
		return failed(fmt.Sprintf("commit %s of %s is not signed", c.SHA, repo))
	}
	// The signature's kind first (a GPG or SSH signature is named as
	// such), then the commit SHA from payload and signature.
	gc, err := parseGitsign(ctx, []byte(p.Payload), []byte(p.Signature))
	if err != nil {
		return failed(err.Error())
	}
	id, err := commitObjectID([]byte(p.Payload), []byte(p.Signature), len(c.SHA) == 64)
	if err != nil {
		return failed("the signed payload: " + err.Error())
	}
	if !strings.EqualFold(id, c.SHA) {
		return failed(fmt.Sprintf("the signed payload and signature the SCM returned are commit %s, not %s", id, c.SHA))
	}
	all, err := r.authorities(ctx, iv)
	if err != nil {
		return waiting(err.Error(), reasonOf(err))
	}
	auths := namedAuthorities(all, names)
	if len(auths) == 0 {
		return failed(fmt.Sprintf("commits.authorities %s name no keyless authority", strings.Join(names, ", ")))
	}
	var reasons []string
	for _, a := range auths {
		if a.identity == nil {
			reasons = append(reasons, a.name+": not a keyless authority")
			continue
		}
		res, signer, err := a.verifyBundle(gc.bundle, gc.digest)
		if err != nil {
			reasons = append(reasons, a.name+": "+err.Error())
			continue
		}
		issuer := certIssuer(res)
		agents, err := r.signingAgents(ctx)
		if err != nil {
			return waiting(err.Error(), "")
		}
		agent, note := signingAgent(agents, issuer, signer)
		iv.Status.Commit = &v1alpha1.CommitVerificationResult{Verified: true, Method: CommitMethodGitsign,
			Signer: signer, Issuer: issuer, Authority: a.name, Agent: agent, Message: truncate(note)}
		return outcome{}
	}
	return failed(fmt.Sprintf("the gitsign signature of commit %s of %s does not verify against commits.authorities: %s",
		c.SHA, repo, strings.Join(reasons, "; ")))
}

// certIssuer is the OIDC issuer of a verified keyless signature's
// certificate, or "".
func certIssuer(res *verify.VerificationResult) string {
	if res == nil || res.Signature == nil || res.Signature.Certificate == nil {
		return ""
	}
	return res.Signature.Certificate.Issuer
}

// signingAgents are the AgentIdentities a verified signer can map to:
// ready for their current generation and not suspended.
func (r *Reconciler) signingAgents(ctx context.Context) ([]v1alpha1.AgentIdentity, error) {
	var list v1alpha1.AgentIdentityList
	if err := r.List(ctx, &list); err != nil {
		return nil, fmt.Errorf("list agentidentities: %w", err)
	}
	out := list.Items[:0]
	for _, ai := range list.Items {
		if ai.Status.Ready && !ai.Spec.Suspended && ai.Status.ObservedGeneration == ai.Generation && len(ai.Spec.SigningIdentities) > 0 {
			out = append(out, ai)
		}
	}
	return out, nil
}

// signingAgent is the AgentIdentity of agents whose spec.signingIdentities
// match a verified signer (issuer and certificate subject), when exactly
// one does (docs/design/20-attestations.md §3.6). None or several give "",
// the second with a note naming them. The mapping is made once, when the
// signature verifies; a gate reads the agent's state now through agent.*.
func signingAgent(agents []v1alpha1.AgentIdentity, issuer, subject string) (agent, note string) {
	if issuer == "" || subject == "" {
		return "", ""
	}
	var matches []string
	for i := range agents {
		if slices.ContainsFunc(agents[i].Spec.SigningIdentities, func(id v1alpha1.AgentSigningIdentity) bool {
			return SigningIdentityMatches(id, issuer, subject)
		}) {
			matches = append(matches, agents[i].Name)
		}
	}
	switch len(matches) {
	case 0:
		return "", ""
	case 1:
		return matches[0], ""
	}
	slices.Sort(matches)
	return "", fmt.Sprintf("the signer matches the signing identities of %d AgentIdentities (%s); no agent recorded",
		len(matches), strings.Join(matches, ", "))
}

// SigningIdentityMatches reports whether a verified signer (issuer and
// certificate subject) is identity: the issuer exactly, and the subject
// exactly or matched in whole by subjectRegExp. An expression that does not
// compile matches nothing (the AgentIdentity reconciler reports it).
func SigningIdentityMatches(identity v1alpha1.AgentSigningIdentity, issuer, subject string) bool {
	if identity.Issuer != issuer {
		return false
	}
	if identity.SubjectRegExp == "" {
		return identity.Subject != "" && identity.Subject == subject
	}
	re, err := regexp.Compile("^(?:" + identity.SubjectRegExp + ")$")
	return err == nil && re.MatchString(subject)
}
