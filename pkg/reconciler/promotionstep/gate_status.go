// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/kubeevent"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// Step outputs of the gate mirror (#1452).
const (
	// OutputGatesStatus records the kardinal/gates commit status last posted,
	// "<state>:<hash>@<commit>" of state, description, context and commit,
	// so a reconcile with nothing new posts nothing. After a failed post it
	// is "error:<key>@<retry time>#<failures>". The key is the status hash,
	// so a new gate result or commit is tried at once; after a permanent
	// error it is "token-<TokenID>", so only the retry time or a rotated
	// SCM token tries again.
	OutputGatesStatus = "gatesStatus"
	// OutputPRHeadSHA is the commit kardinal pushed to the PR branch, which
	// the kardinal/gates status is set on. A head someone else pushed gets no
	// status from kardinal.
	OutputPRHeadSHA = "prHeadSHA"
	// OutputMergedWhileBlocked names the gate that was failing when the PR
	// was merged anyway.
	OutputMergedWhileBlocked = "mergedWhileBlocked"
)

// Retry backoff after a failed kardinal/gates post: doubling from
// gatesStatusRetryMin, at most gatesStatusRetryMax. A permanent error (401,
// 403 that is not a rate limit, 404, 410, or a commit the PR does not have)
// waits gatesStatusRetryMax at once: a token without the commit-status
// permission does not fix itself, whatever the gates say, and polling it
// would spend the rate limit; a rotated token is tried at once.
const (
	gatesStatusRetryMin = 30 * time.Second
	gatesStatusRetryMax = time.Hour
)

// gatesStatus is the kardinal/gates commit status for ps, from its own spec:
// spec.live.gates, which the Graph's GateMirror patch writes (graph
// mirror.go). Before the first mirror value it is pending, so a stale success
// is never left in place; a step with no gates is success.
func gatesStatus(ps *v1alpha1.PromotionStep) scm.CommitStatus {
	s := scm.CommitStatus{Context: scm.GatesStatusContext}
	switch {
	case ps.Spec.Live == nil && len(ps.Spec.RequiredGates) == 0:
		s.State, s.Description = scm.CommitStatusSuccess, "no kardinal gates on "+ps.Spec.Environment
	case ps.Spec.Live == nil:
		// The Graph's GateMirror patch writes spec.live as soon as the step
		// exists, long before its PR opens; a waiting step without it has a
		// Graph from before the mirror (an upgrade). Say so instead of
		// pending forever; it is never success.
		s.State = scm.CommitStatusError
		s.Description = "gate results unknown: the Graph does not mirror them to this step (created before the upgrade?)"
	default:
		if g := firstBlocking(ps.Spec.Live.Gates); g != nil {
			s.State = scm.CommitStatusFailure
			s.Description = gateLabel(g.Name) + ": " + g.Reason
			if n := blockingCount(ps.Spec.Live.Gates); n > 1 {
				s.Description = fmt.Sprintf("%d gates block; %s", n, s.Description)
			}
		} else {
			s.State = scm.CommitStatusSuccess
			s.Description = fmt.Sprintf("all %d gates pass", len(ps.Spec.Live.Gates))
		}
	}
	return s
}

func firstBlocking(gates []v1alpha1.LiveGate) *v1alpha1.LiveGate {
	for i := range gates {
		if !gates[i].Ready {
			return &gates[i]
		}
	}
	return nil
}

func blockingCount(gates []v1alpha1.LiveGate) int {
	n := 0
	for _, g := range gates {
		if !g.Ready {
			n++
		}
	}
	return n
}

// gateLabel shortens a gate instance name (<bundle>-<gate>-<env>...) for a
// one-line status: the description has room for about 140 characters.
func gateLabel(name string) string {
	if len(name) > 60 {
		return name[:57] + "..."
	}
	return name
}

// syncGatesStatus posts the kardinal/gates commit status on the commit
// kardinal pushed to the step's PR (status.outputs.prHeadSHA) when it differs
// from the one last posted (status.outputs.gatesStatus). It is called while
// the step waits for its open PR to merge. Nothing is posted when the
// feature is off (--gates-commit-status=false), the provider has no commit
// statuses, or the step has no pushed commit recorded (a PR opened before the
// upgrade). A failed post is recorded with a retry time (see
// gatesStatusRetryMax) and one Warning Event; the step does not fail on it.
//
// Graph-first: the gate results come from the step's own spec (the Graph's
// mirror patch); the SCM write stays in the reconciler (ledger G8).
func (r *Reconciler) syncGatesStatus(ctx context.Context, ps *v1alpha1.PromotionStep, repo string, prNumber int) error {
	if r.GatesStatusDisabled {
		return nil
	}
	// The provider the step's PR was opened on (spec.scmProvider, #1517),
	// not always the controller's default one.
	provider, err := r.scmFor(ctx, ps)
	if err != nil {
		return fmt.Errorf("gates commit status: %w", err)
	}
	setter, ok := provider.(scm.CommitStatusSetter)
	// The commit kardinal last pushed to the PR branch: git-push's
	// pushedSHA, which a rebuild on a moved base branch (#1504) replaces,
	// else the prHeadSHA recorded when the PR opened.
	sha := ps.Status.Outputs[outputPushedSHA]
	if sha == "" {
		sha = ps.Status.Outputs[OutputPRHeadSHA]
	}
	if !ok || repo == "" || prNumber <= 0 || sha == "" {
		return nil
	}
	s := gatesStatus(ps)
	s.Context = r.gatesContext()
	hash := statusHash(s, sha)
	want := s.State + ":" + hash
	prev := ps.Status.Outputs[OutputGatesStatus]
	if prev == want+"@"+shortSHA(sha) {
		return nil
	}
	// A failed post is retried at its retry time. A permanent error is keyed
	// on the token (token-<TokenID>): a new gate result does not retry it,
	// a rotated token does. Any other error is keyed on the status (hash,
	// which includes the commit): a new result or a new kardinal push
	// retries at once.
	token := "token-" + scmTokenID(provider)
	failures := 0
	if key, retryAt, n, ok := failedPost(prev); ok && (key == hash || key == token) {
		if r.now().Before(retryAt) {
			return nil
		}
		failures = n
	}
	err = setter.SetPRCommitStatus(ctx, repo, prNumber, sha, s)
	if errors.Is(err, scm.ErrCommitStatusUnsupported) {
		return nil
	}
	record := want + "@" + shortSHA(sha)
	if err != nil {
		wait := min(gatesStatusRetryMin<<min(failures, 10), gatesStatusRetryMax)
		key := hash
		switch {
		case errors.Is(err, scm.ErrCommitNotInPR):
			wait = gatesStatusRetryMax
		case scm.IsPermanentError(err):
			wait, key = gatesStatusRetryMax, token
		}
		record = fmt.Sprintf("error:%s@%s#%d", key, r.now().Add(wait).UTC().Format(time.RFC3339), failures+1)
		if failures == 0 {
			kubeevent.Emit(r.Recorder, ps, corev1.EventTypeWarning, "GatesStatusFailed", "Promote",
				fmt.Sprintf("env %s: could not set the %s commit status on PR #%d (retrying in %s): %v",
					ps.Spec.Environment, s.Context, prNumber, wait, err))
		}
	}
	base := ps.DeepCopy()
	if ps.Status.Outputs == nil {
		ps.Status.Outputs = map[string]string{}
	}
	ps.Status.Outputs[OutputGatesStatus] = record
	if perr := r.Status().Patch(ctx, ps, client.MergeFrom(base)); perr != nil {
		return fmt.Errorf("record %s commit status: %w", s.Context, perr)
	}
	if err != nil {
		return fmt.Errorf("set %s commit status on PR #%d: %w", s.Context, prNumber, err)
	}
	zerolog.Ctx(ctx).Info().Str("step", ps.Name).Int("pr", prNumber).Str("sha", shortSHA(sha)).
		Str("state", s.State).Str("description", s.Description).Msg("gates commit status posted")
	return nil
}

// failedPost parses a gatesStatus output recorded after a failed post:
// its key (a status hash, or token-<TokenID> after a permanent error), retry
// time and failure count.
func failedPost(out string) (string, time.Time, int, bool) {
	rest, ok := strings.CutPrefix(out, "error:")
	if !ok {
		return "", time.Time{}, 0, false
	}
	key, rest, ok := strings.Cut(rest, "@")
	if !ok {
		return "", time.Time{}, 0, false
	}
	at, n, _ := strings.Cut(rest, "#")
	retryAt, err := time.Parse(time.RFC3339, at)
	if err != nil {
		return "", time.Time{}, 0, false
	}
	failures, _ := strconv.Atoi(n)
	return key, retryAt, failures, true
}

// scmTokenID identifies the SCM token provider uses (scm.TokenIdentifier),
// or "" when the provider cannot tell.
func scmTokenID(provider scm.SCMProvider) string {
	if ti, ok := provider.(scm.TokenIdentifier); ok {
		return ti.TokenID()
	}
	return ""
}

// statusHash identifies a status by state, description, context and commit.
func statusHash(s scm.CommitStatus, sha string) string {
	sum := sha256.Sum256([]byte(s.State + "\x00" + s.Description + "\x00" + s.Context + "\x00" + sha))
	return hex.EncodeToString(sum[:])[:12]
}

// recordPRHead stores, as outputs.prHeadSHA, the commit the git-push step
// pushed to the PR branch (its persisted output pushedSHA), which the
// kardinal/gates status is set on. A re-run that pushes again (a restart
// re-commits) records the new commit, so the status follows kardinal's
// pushes; a commit someone else pushes is never recorded. When the output is
// missing (the git client cannot report the commit) the PR gets no status,
// and a Warning Event says so.
func (r *Reconciler) recordPRHead(ps *v1alpha1.PromotionStep, outputs map[string]string) {
	if outputs["noChanges"] == "true" || outputs["branch"] == "" {
		return
	}
	sha := outputs[outputPushedSHA]
	if sha == "" {
		if !r.GatesStatusDisabled {
			kubeevent.Emit(r.Recorder, ps, corev1.EventTypeWarning, "GatesStatusNoCommit", "Promote",
				fmt.Sprintf("env %s: git-push recorded no pushed commit, so the PR gets no %s commit status",
					ps.Spec.Environment, r.gatesContext()))
		}
		return
	}
	outputs[OutputPRHeadSHA] = sha
}

// gatesContext is the commit status context the gate results are posted
// under.
func (r *Reconciler) gatesContext() string {
	if r.GatesStatusContext != "" {
		return r.GatesStatusContext
	}
	return scm.GatesStatusContext
}

func shortSHA(sha string) string {
	return sha[:min(12, len(sha))]
}

// noteMergedWhileBlocked records, on a step whose PR merged while one of its
// gates failed (branch protection did not require kardinal/gates, or someone
// bypassed it), which gate it was: status.outputs.mergedWhileBlocked and a
// Warning Event. The change is in the environment, so the step goes on to its
// health check; failing it would not revert anything (DESIGN §7). It only
// changes ps in memory; the caller's transition patches it.
func (r *Reconciler) noteMergedWhileBlocked(ps *v1alpha1.PromotionStep) {
	if ps.Spec.Live == nil {
		return
	}
	g := firstBlocking(ps.Spec.Live.Gates)
	if g == nil {
		return
	}
	if ps.Status.Outputs == nil {
		ps.Status.Outputs = map[string]string{}
	}
	ps.Status.Outputs[OutputMergedWhileBlocked] = g.Name + ": " + g.Reason
	kubeevent.Emit(r.Recorder, ps, corev1.EventTypeWarning, "MergedWhileBlocked", "Promote",
		fmt.Sprintf("env %s: the PR merged while gate %s blocked (%s); the change is live, so the health check runs",
			ps.Spec.Environment, g.Name, g.Reason))
}
