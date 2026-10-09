// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Package renderjob is the render of a layout: branch environment as the
// kardinal-render Job runs it: clone the rendered branch and the DRY source,
// set the Bundle's images in the DRY checkout, render it (kustomize build or
// helm template, in process), commit the plain manifests with the DRY commit
// in the trailers and push. The RenderRun reconciler passes the Config in the
// Job's environment and reads the Result back from the Pod's termination
// message. Rendering never runs in the controller.
package renderjob

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/steps"
	_ "github.com/kardinal-promoter/kardinal-promoter/pkg/steps/steps" // registers the steps
)

// Paths and variables of the render Job's Pod.
const (
	// ConfigEnv holds the JSON Config.
	ConfigEnv = "KARDINAL_RENDER_CONFIG"
	// TokenFile is where the git Secret's token key is mounted.
	TokenFile = "/var/run/kardinal/git/token"
	// WorkDir is the emptyDir the render works in.
	WorkDir = "/work"
	// TerminationMessagePath is where the Result is written.
	TerminationMessagePath = "/dev/termination-log"
	// maxTerminationMessage is the kubelet's limit on a termination message.
	maxTerminationMessage = 4096
	// ExitPermanent is the exit code of a render that failed for good (a
	// template error, drift, a refused input): the Job fails at once
	// (podFailurePolicy). Other failures exit 1 and are retried.
	ExitPermanent = 2
)

// Config is everything the render Job needs, written by the RenderRun
// reconciler from the RenderRun.
type Config struct {
	Namespace   string                 `json:"namespace"`
	Pipeline    string                 `json:"pipeline"`
	Environment string                 `json:"environment"`
	Path        string                 `json:"path"`
	BundleName  string                 `json:"bundleName"`
	Bundle      v1alpha1.BundleSpec    `json:"bundle"`
	Git         v1alpha1.RenderRunGit  `json:"git"`
	Update      v1alpha1.UpdateConfig  `json:"update"`
	Render      *v1alpha1.RenderConfig `json:"render,omitempty"`
	AuthorName  string                 `json:"authorName,omitempty"`
	AuthorEmail string                 `json:"authorEmail,omitempty"`
	// KnownMarkerDigests are the marker digests of earlier renders of this
	// Pipeline environment.
	KnownMarkerDigests []string `json:"knownMarkerDigests,omitempty"`
	// UnconfirmedBundles: see RenderRunStatus.UnconfirmedBundles.
	UnconfirmedBundles []string `json:"unconfirmedBundles,omitempty"`
}

// Result is the termination message of the render Job.
type Result struct {
	v1alpha1.RenderRunResult `json:",inline"`
	// Error is why the render failed ("" on success).
	Error string `json:"error,omitempty"`
}

// ConfigFromRun is the Config of RenderRun run.
func ConfigFromRun(run *v1alpha1.RenderRun, authorName, authorEmail string) Config {
	b := run.Spec.Bundle
	bundle := v1alpha1.BundleSpec{Type: b.Type, Images: b.Images, ConfigRef: b.ConfigRef}
	if b.RollbackOf != "" {
		bundle.Provenance = &v1alpha1.BundleProvenance{RollbackOf: b.RollbackOf}
	}
	return Config{
		Namespace: run.Namespace, Pipeline: run.Spec.PipelineName, Environment: run.Spec.Environment,
		Path: run.Spec.Path, BundleName: run.Spec.BundleName, Bundle: bundle, Git: run.Spec.Git,
		Update: run.Spec.Update, Render: run.Spec.Render, AuthorName: authorName, AuthorEmail: authorEmail,
		KnownMarkerDigests: run.Status.KnownMarkerDigests, UnconfirmedBundles: run.Status.UnconfirmedBundles,
	}
}

// Run renders cfg in workDir and returns what it did. token is the git
// token ("" for none).
func Run(ctx context.Context, cfg Config, workDir, token string, git scm.GitClient) (Result, error) {
	env := v1alpha1.EnvironmentSpec{Name: cfg.Environment, Path: cfg.Path, Update: cfg.Update, Render: cfg.Render,
		Layout: "branch"}
	seq := steps.RenderJobSequence(cfg.Bundle.Type, cfg.Update.Strategy)
	// git-push pushes to the promotion branch when the step list opens a PR.
	stateSeq := append([]string(nil), seq...)
	if cfg.Git.PullRequest {
		stateSeq = append(stateSeq, steps.OpenPRStepName)
	}
	state := &steps.StepState{
		Namespace:    cfg.Namespace,
		PipelineName: cfg.Pipeline,
		Environment:  env,
		Bundle:       cfg.Bundle,
		BundleName:   cfg.BundleName,
		WorkDir:      filepath.Join(workDir, "rendered"),
		Outputs:      map[string]string{},
		Git: steps.GitConfig{URL: cfg.Git.URL, Branch: cfg.Git.RenderedBranch, SourceBranch: cfg.Git.SourceBranch,
			Token: token, AuthorName: cfg.AuthorName, AuthorEmail: cfg.AuthorEmail},
		GitClient: git,
		Sequence:  stateSeq,
		Render: &steps.RenderContext{Namespace: cfg.Namespace, KnownMarkerDigests: cfg.KnownMarkerDigests,
			UnconfirmedBundles: cfg.UnconfirmedBundles},
	}
	// The engine restarts the sequence from a fresh clone when the rendered
	// branch moved while rendering (git-push).
	next, res, err := steps.NewEngine(seq).ExecuteFrom(ctx, state, 0)
	if err != nil {
		return Result{}, err
	}
	if res.Status != steps.StepSuccess || next < len(seq) {
		return Result{}, fmt.Errorf("%s: %s", seq[min(next, len(seq)-1)], res.Message)
	}
	out := Result{RenderRunResult: v1alpha1.RenderRunResult{
		DryCommit:        state.Outputs["dryCommit"],
		Renderer:         state.Outputs["renderer"],
		MarkerDigest:     state.Outputs["markerDigest"],
		NoChanges:        state.Outputs["noChanges"] == "true",
		DriftOverwritten: state.Outputs["driftOverwritten"],
		Branch:           state.Outputs["branch"],
	}}
	out.Objects, _ = strconv.Atoi(state.Outputs["renderedObjects"])
	// The commit the rendered branch is at: the one pushed, or, when the
	// branch already held this render (noChanges), its head, which the
	// controller checks against the remote too.
	{
		if hr, ok := git.(scm.HeadCommitReader); ok {
			sha, err := hr.HeadCommit(ctx, state.WorkDir)
			if err != nil {
				return Result{}, fmt.Errorf("read the rendered commit: %w", err)
			}
			out.CommitSHA = sha
		}
	}
	return out, nil
}

// Message encodes r as a termination message: JSON within the kubelet's
// 4096 bytes, the error cut to fit.
func Message(r Result) []byte {
	b, _ := json.Marshal(r)
	for _, field := range []*string{&r.DriftOverwritten, &r.Error} {
		for len(b) > maxTerminationMessage && *field != "" {
			cut := len(*field) - (len(b) - maxTerminationMessage) - 16
			if cut < 0 {
				cut = 0
			}
			for cut > 0 && !utf8.RuneStart((*field)[cut]) {
				cut--
			}
			*field = (*field)[:cut] + " (cut)"
			b, _ = json.Marshal(r)
		}
	}
	return b
}

// ParseMessage decodes a termination message.
func ParseMessage(msg string) (Result, error) {
	var r Result
	if strings.TrimSpace(msg) == "" {
		return r, errors.New("the render Job wrote no result")
	}
	if err := json.Unmarshal([]byte(msg), &r); err != nil {
		return r, fmt.Errorf("the render Job's result is not JSON: %w", err)
	}
	return r, nil
}

// The process seams Main uses; tests replace them.
var (
	lockNetwork = LockNetwork
	runRender   = Run
	newGit      = func() scm.GitClient { return scm.NewGoGitClient() }
	messagePath = TerminationMessagePath
	tokenPath   = TokenFile
	workDir     = WorkDir
)

// Main is the kardinal-render entry point: it reads the Config and the
// token, locks the process's network to the Pipeline's git host, renders,
// writes the termination message and returns the exit code: 0, ExitPermanent
// for a render that failed for good (the Job does not retry it), 1 otherwise.
func Main(ctx context.Context) int {
	write := func(r Result) {
		_ = os.WriteFile(messagePath, Message(r), 0o600)
	}
	var cfg Config
	if err := json.Unmarshal([]byte(os.Getenv(ConfigEnv)), &cfg); err != nil {
		write(Result{Error: fmt.Sprintf("read %s: %v", ConfigEnv, err)})
		return 1
	}
	token := ""
	if b, err := os.ReadFile(tokenPath); err == nil {
		token = strings.TrimSpace(string(b))
	}
	if err := lockNetwork(cfg.Git.URL); err != nil {
		write(Result{Error: err.Error()})
		return 1
	}
	if err := os.Setenv(steps.RenderJobEnv, "1"); err != nil {
		write(Result{Error: err.Error()})
		return 1
	}
	res, err := runRender(ctx, cfg, workDir, token, newGit())
	if err != nil {
		write(Result{Error: scm.RedactText(err.Error())})
		if errors.Is(err, steps.ErrPermanent) {
			return ExitPermanent
		}
		return 1
	}
	write(res)
	return 0
}

// Digest is the sha256 of b, hex.
func Digest(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
