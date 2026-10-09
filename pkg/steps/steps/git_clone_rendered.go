// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package steps

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
	parentsteps "github.com/kardinal-promoter/kardinal-promoter/pkg/steps"
)

// Outputs of layout: branch.
const (
	// outputDryCommit is the DRY source commit that was rendered.
	outputDryCommit = "dryCommit"
	// outputRenderedBranch is the branch the render is committed to.
	outputRenderedBranch = "renderedBranch"
	// outputRenderedBranchCreated is "true" when git-clone created the
	// rendered branch in this run.
	outputRenderedBranchCreated = "renderedBranchCreated"
)

// Commit trailers of a rendered commit (git-commit adds them).
const (
	trailerDryCommit = "Kardinal-Dry-Commit"
	trailerDryPath   = "Kardinal-Dry-Path"
	trailerBundle    = "Kardinal-Bundle"
)

// rollbackHistoryDepth is how many commits of the rendered branch a rollback
// looks through for the render of the Bundle it restores.
const rollbackHistoryDepth = 500

// cloneRendered is git-clone for layout: branch:
//
//  1. The rendered branch (Git.Branch, env/<name> by default) is cloned into
//     WorkDir. When it does not exist it is created on the remote with one
//     commit holding only the render marker (renderMarkerPath), so a
//     pr-review promotion has a branch to open its PR against and its diff
//     shows every rendered file as added.
//  2. The DRY source is checked out into DrySourceDir(WorkDir) at the commit
//     to render (dryCommitToRender): the Bundle's configRef.commitSHA, for a
//     rollback the DRY commit of the render it restores, otherwise the head
//     of Git.SourceBranch.
func cloneRendered(ctx context.Context, state *parentsteps.StepState) (parentsteps.StepResult, error) {
	fail := func(msg string, err error) (parentsteps.StepResult, error) {
		return parentsteps.StepResult{Status: parentsteps.StepFailed, Message: msg}, err
	}
	bc, okBC := state.GitClient.(scm.BranchCloner)
	hr, okHR := state.GitClient.(scm.HeadCommitReader)
	if !okBC || !okHR {
		err := parentsteps.Permanent(errors.New("layout: branch needs a git client that can create branches"))
		return fail(err.Error(), err)
	}
	if state.Git.SourceBranch == "" || state.Git.Branch == state.Git.SourceBranch {
		err := parentsteps.Permanent(fmt.Errorf("layout: branch: the rendered branch %q must differ from the source branch %q",
			state.Git.Branch, state.Git.SourceBranch))
		return fail(err.Error(), err)
	}
	if ref := state.Bundle.ConfigRef; ref != nil && ref.GitRepo != "" && !scm.SameRepo(ref.GitRepo, state.Git.URL) {
		err := parentsteps.Permanent(fmt.Errorf("layout: branch renders the Pipeline's repository: configRef.gitRepo %s "+
			"must be empty or the Pipeline's spec.git.url", scm.RedactURL(ref.GitRepo)))
		return fail(err.Error(), err)
	}
	for _, d := range []string{state.WorkDir, parentsteps.DrySourceDir(state.WorkDir)} {
		if err := os.RemoveAll(d); err != nil {
			return fail(fmt.Sprintf("clean work dir: %v", err), fmt.Errorf("clean work dir: %w", err))
		}
	}

	// 1. The rendered branch. A rollback needs its history.
	depth := 1
	if state.Bundle.Provenance != nil && state.Bundle.Provenance.RollbackOf != "" {
		depth = rollbackHistoryDepth
	}
	created, err := bc.CloneOrInit(ctx, state.Git.URL, state.Git.Branch, state.WorkDir, state.Git.Token, depth)
	if err != nil {
		msg := scm.RedactText(err.Error())
		return fail(msg, errors.New(msg))
	}
	outputs := map[string]string{outputRenderedBranch: state.Git.Branch}
	if created {
		if err := createRenderedBranch(ctx, state); err != nil {
			msg := scm.RedactText(err.Error())
			return fail(msg, errors.New(msg))
		}
		outputs[outputRenderedBranchCreated] = "true"
	}

	// 2. The DRY source.
	dryDir := parentsteps.DrySourceDir(state.WorkDir)
	commit, rollback, err := dryCommitToRender(ctx, state)
	if err != nil {
		return fail(err.Error(), err)
	}
	if commit != "" {
		err = state.GitClient.CloneAt(ctx, state.Git.URL, commit, dryDir, state.Git.Token)
	} else {
		err = state.GitClient.Clone(ctx, state.Git.URL, state.Git.SourceBranch, dryDir, state.Git.Token)
	}
	if err != nil {
		msg := "DRY source: " + scm.RedactText(err.Error())
		return fail(msg, errors.New(msg))
	}
	if rollback {
		// A commit named in a rendered branch's history is only a claim:
		// render it only if it is part of the DRY source branch, so a push
		// to the rendered branch cannot make a rollback render any commit.
		ac, ok := state.GitClient.(scm.AncestryChecker)
		if !ok {
			err := parentsteps.Permanent(errors.New("layout: branch rollback needs a git client that can walk history"))
			return fail(err.Error(), err)
		}
		reachable, err := ac.ReachableFrom(ctx, dryDir, commit, state.Git.SourceBranch)
		if err != nil {
			return fail("DRY source: "+scm.RedactText(err.Error()), err)
		}
		if !reachable {
			err := parentsteps.Permanent(fmt.Errorf("rollback to %s: its DRY commit %s is not on %s, so it is not rendered",
				state.Bundle.Provenance.RollbackOf, commit, state.Git.SourceBranch))
			return fail(err.Error(), err)
		}
	}
	head, err := hr.HeadCommit(ctx, dryDir)
	if err != nil {
		return fail(fmt.Sprintf("DRY source: %v", err), err)
	}
	outputs[outputDryCommit] = head
	return parentsteps.StepResult{
		Status: parentsteps.StepSuccess,
		Message: fmt.Sprintf("cloned %s (rendered branch %s) and the DRY source %s@%s",
			scm.RedactURL(state.Git.URL), state.Git.Branch, state.Git.SourceBranch, shortSHA(head)),
		Outputs: outputs,
	}, nil
}

// createRenderedBranch commits an empty render marker in the new rendered
// branch checkout and pushes it, so the branch exists.
func createRenderedBranch(ctx context.Context, state *parentsteps.StepState) error {
	root, err := os.OpenRoot(state.WorkDir)
	if err != nil {
		return fmt.Errorf("open rendered branch checkout: %w", err)
	}
	defer func() { _ = root.Close() }()
	if err := writeMarker(root, renderMarker{Pipeline: state.PipelineName, Namespace: renderNamespace(state),
		Environment: state.Environment.Name, Files: map[string]string{}}); err != nil {
		return err
	}
	msg := fmt.Sprintf("[kardinal] Create rendered branch %s\n\nPipeline: %s\nEnvironment: %s",
		state.Git.Branch, state.PipelineName, state.Environment.Name)
	if err := state.GitClient.CommitAll(ctx, state.WorkDir, msg, authorName(state), authorEmail(state)); err != nil {
		return fmt.Errorf("create rendered branch %s: %w", state.Git.Branch, err)
	}
	if err := state.GitClient.Push(ctx, state.WorkDir, "origin", state.Git.Branch, state.Git.Token, false); err != nil &&
		!errors.Is(err, scm.ErrNonFastForward) {
		return fmt.Errorf("create rendered branch %s: %w", state.Git.Branch, err)
	}
	return nil
}

// dryCommitRE is a full git commit id. A rollback trusts only a full id in a
// rendered commit's trailer: a short one could name another commit.
var dryCommitRE = regexp.MustCompile(`^[0-9a-f]{40}$`)

// maxRenderedCommitBytes bounds the files read from one rendered commit
// while a rollback looks for the render it restores.
const maxRenderedCommitBytes = 64 << 20

// dryCommitToRender is the DRY commit to check out, or "" for the head of the
// source branch, and whether it is a rollback's (which the caller must find
// on the source branch):
//   - the Bundle's configRef.commitSHA (a config or mixed Bundle, or an image
//     Bundle that pins its DRY commit);
//   - for a rollback Bundle without one, the DRY commit of the render of the
//     restored Bundle (provenance.rollbackOf) on the rendered branch. Only a
//     render kardinal made counts (trustedRender): a commit whose
//     Kardinal-Bundle trailer names the Bundle, whose Kardinal-Dry-Commit is
//     a full commit id, and whose tree holds a render marker for this
//     Pipeline environment, that Bundle and that DRY commit, with every file
//     it lists unchanged. A rollback whose render is not in the history fails
//     rather than render other DRY content.
func dryCommitToRender(ctx context.Context, state *parentsteps.StepState) (string, bool, error) {
	if ref := state.Bundle.ConfigRef; ref != nil && ref.CommitSHA != "" {
		return ref.CommitSHA, false, nil
	}
	prov := state.Bundle.Provenance
	if prov == nil || prov.RollbackOf == "" {
		return "", false, nil
	}
	hr, okHR := state.GitClient.(scm.HistoryReader)
	tr, okTR := state.GitClient.(scm.TreeReader)
	if !okHR || !okTR {
		return "", false, parentsteps.Permanent(errors.New("layout: branch rollback needs a git client that can read history"))
	}
	commits, err := hr.CommitMessages(ctx, state.WorkDir, rollbackHistoryDepth)
	if err != nil {
		return "", false, fmt.Errorf("read the history of %s: %w", state.Git.Branch, err)
	}
	var refused []string
	for _, c := range commits {
		t := trailers(c.Message)
		if t[trailerBundle] != prov.RollbackOf {
			continue
		}
		dry := t[trailerDryCommit]
		if why := trustedRender(ctx, tr, state, c.SHA, prov.RollbackOf, dry); why != "" {
			refused = append(refused, shortSHA(c.SHA)+": "+why)
			continue
		}
		return dry, true, nil
	}
	msg := fmt.Sprintf("rollback to %s: no render of it in the last %d commits of %s, so its DRY commit is unknown; "+
		"pin it with configRef.commitSHA", prov.RollbackOf, rollbackHistoryDepth, state.Git.Branch)
	if len(refused) > 0 {
		msg += " (commits that name it but are not a render kardinal made: " + strings.Join(firstN(refused, 3), "; ") + ")"
	}
	return "", false, parentsteps.Permanent(errors.New(msg))
}

// trustedRender returns "" when rendered commit sha is a render kardinal
// made of bundle from DRY commit dry for this Pipeline environment, or why
// not.
func trustedRender(ctx context.Context, tr scm.TreeReader, state *parentsteps.StepState, sha, bundle, dry string) string {
	if !dryCommitRE.MatchString(dry) {
		return fmt.Sprintf("%s %q is not a full commit id", trailerDryCommit, dry)
	}
	files, err := tr.CommitFiles(ctx, state.WorkDir, sha, maxRenderedCommitBytes)
	if err != nil {
		return err.Error()
	}
	raw, ok := files[renderMarkerPath]
	if !ok {
		return "no " + renderMarkerPath
	}
	m, err := parseMarker(raw)
	if err != nil {
		return err.Error()
	}
	if why := markerOwner(m, state); why != "" {
		return why
	}
	if m.Bundle != bundle || m.DryCommit != dry {
		return fmt.Sprintf("its marker records Bundle %q and DRY commit %q", m.Bundle, m.DryCommit)
	}
	for p, want := range m.Files {
		if got, ok := files[p]; !ok || digest(got) != want {
			return p + " does not match the marker"
		}
	}
	return ""
}

// trailers parses "Key: value" lines of a commit message's last paragraph.
func trailers(msg string) map[string]string {
	out := map[string]string{}
	paras := strings.Split(strings.TrimSpace(msg), "\n\n")
	for _, line := range strings.Split(paras[len(paras)-1], "\n") {
		k, v, ok := strings.Cut(line, ": ")
		if ok && !strings.Contains(k, " ") {
			out[k] = strings.TrimSpace(v)
		}
	}
	return out
}
