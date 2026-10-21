// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package steps

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	parentsteps "github.com/kardinal-promoter/kardinal-promoter/pkg/steps"
)

const (
	// outputWriteFailed is the request whose failed write await-write has
	// already reported: the step's next run (a retry) asks again.
	outputWriteFailed = "writeFailed"
	// OutputWrittenBy is the BranchWrite and round that wrote the
	// environment's commit (status.outputs.writtenBy, "<name>/<round>").
	OutputWrittenBy = "writtenBy"
)

// awaitWriteRequeue is the fallback poll while the BranchWrite writes; the
// Graph's mirror of its status onto the step (spec.live.writes) wakes the
// step first.
const awaitWriteRequeue = 30 * time.Second

// awaitWriteStep is a batched environment's write (spec.git.writes: batched,
// docs/design/30-batched-branch-writes.md). The Bundle's BranchWrite clones,
// commits the environment's change with its own commit and pushes, together
// with the other environments that asked at the same time; the step asks
// and reads the answer the Graph mirrors onto it (spec.live.writes):
//
//   - no request yet: it asks (OutputWriteRequest; the reconciler counts the
//     request in status.writeRequest) and waits;
//   - no answer to its latest request: it waits;
//   - the environment's commit, or no change needed: its outputs are those
//     git-push and recordPushedCommit would leave (commitSHA, branch,
//     noChanges), and health is checked as usual;
//   - an error: the step fails, retryably; its retry asks again, which is
//     written in a later round;
//   - cancelled: the push guard's ends, as git-push's (a superseded or
//     rejected Bundle, or a newer one that already pushed).
type awaitWriteStep struct{}

func init() {
	parentsteps.Register(&awaitWriteStep{})
}

func (s *awaitWriteStep) Name() string { return parentsteps.AwaitWriteStepName }

func (s *awaitWriteStep) Execute(_ context.Context, state *parentsteps.StepState) (parentsteps.StepResult, error) {
	if state.Write == "" {
		err := errors.New("await-write needs the step's BranchWrite (spec.write), which the Graph sets for a batched environment")
		return parentsteps.StepResult{Status: parentsteps.StepFailed, Message: err.Error()}, parentsteps.Permanent(err)
	}
	ask := parentsteps.StepResult{
		Status:  parentsteps.StepPending,
		Message: fmt.Sprintf("asking BranchWrite %s to write %s", state.Write, state.Environment.Name),
		Outputs: map[string]string{parentsteps.OutputWriteRequest: "new"},
	}
	if state.WriteRequest == 0 {
		return ask, nil
	}
	res := writeResult(state)
	if res == nil {
		return parentsteps.StepResult{
			Status:       parentsteps.StepPending,
			Message:      fmt.Sprintf("waiting for BranchWrite %s to write %s (request %d)", state.Write, state.Environment.Name, state.WriteRequest),
			RequeueAfter: awaitWriteRequeue,
		}, nil
	}
	req := strconv.FormatInt(state.WriteRequest, 10)
	switch {
	case res.Cancelled && res.NewerBundle != "":
		err := fmt.Errorf("%w: newer bundle %s already pushed to %s; this promotion is superseded and does not push",
			parentsteps.ErrNewerPushed, res.NewerBundle, state.Environment.Name)
		return parentsteps.StepResult{Status: parentsteps.StepFailed, Message: err.Error()}, parentsteps.Permanent(err)
	case res.Cancelled:
		err := fmt.Errorf("%w: %s", parentsteps.ErrStalePush, res.Error)
		return parentsteps.StepResult{Status: parentsteps.StepFailed, Message: err.Error()}, parentsteps.Permanent(err)
	case res.Error != "" && state.Outputs[outputWriteFailed] == req:
		// This run is the retry of the failed write: ask again.
		return ask, nil
	case res.Error != "":
		msg := fmt.Sprintf("BranchWrite %s could not write %s (round %d): %s", state.Write, state.Environment.Name, res.Round, res.Error)
		return parentsteps.StepResult{Status: parentsteps.StepFailed, Message: msg,
			Outputs: map[string]string{outputWriteFailed: req}}, errors.New(msg)
	}
	outputs := map[string]string{
		"branch":        state.Git.Branch,
		"commitSHA":     res.CommitSHA,
		"noChanges":     strconv.FormatBool(res.NoChanges),
		OutputWrittenBy: fmt.Sprintf("%s/%d", state.Write, res.Round),
	}
	msg := fmt.Sprintf("BranchWrite %s pushed %s's commit %s (round %d)", state.Write, state.Environment.Name, short(res.CommitSHA), res.Round)
	if res.NoChanges {
		msg = fmt.Sprintf("%s already has the Bundle's version (BranchWrite %s, round %d)", state.Environment.Name, state.Write, res.Round)
	}
	return parentsteps.StepResult{Status: parentsteps.StepSuccess, Message: msg, Outputs: outputs}, nil
}

// writeResult is the BranchWrite's answer to the step's latest request, or
// nil when it has none yet: an answer to an earlier request, or to another
// step of the environment (a step deleted and made again), is not.
func writeResult(state *parentsteps.StepState) *v1alpha1.BranchWriteResult {
	for _, w := range state.LiveWrites {
		r := w.Result
		if w.Name != state.Write || r == nil || r.Request != state.WriteRequest {
			continue
		}
		if r.UID != "" && state.StepUID != "" && r.UID != state.StepUID {
			continue
		}
		return r
	}
	return nil
}

// short is a commit's first 12 characters.
func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
