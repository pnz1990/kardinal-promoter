// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// The live suites can run in CI instead of on this host (-live ci): one
// dispatch of e2e-live.yml on the gate's branch runs every matrix.txt job but
// scale's, each on a runner of its own. Each job is a step; it waits for its
// job, takes its result from the job's conclusion and its tests from the
// job's artifact (summary.json, test.json). A step that failed in an earlier
// gate re-runs its job in the same workflow run.

// ciJob is a matrix.txt line as e2e-live.yml runs it (hack/e2e/all.sh -matrix).
type ciJob struct{ ID, Suite, Minor, Shard string }

// jobID is all.sh's id: core-135-1of3.
func jobID(l MatrixLine) string {
	id := l.Suite + "-" + strings.ReplaceAll(l.Minor, ".", "")
	if l.Shard != "-" {
		i, n, _ := strings.Cut(l.Shard, "/")
		id += "-" + i + "of" + n
	}
	return id
}

// Name is the job's name in e2e-live.yml: "core 1/3 on Kubernetes 1.35".
func (j ciJob) Name() string {
	n := j.Suite
	if j.Shard != "-" {
		n += " " + j.Shard
	}
	return n + " on Kubernetes " + j.Minor
}

// ciRun is the dispatched run, saved in the results directory so a later
// gate on the same commit waits for it rather than dispatching again.
type ciRun struct {
	ID     int64  `json:"id"`
	SHA    string `json:"sha"`
	Ref    string `json:"ref"`
	Suites string `json:"suites"`
	Minors string `json:"minors"`
	Count  int    `json:"count"`
}

// GH runs the gh CLI; tests fake it.
type GH func(ctx context.Context, args ...string) ([]byte, error)

type ciRunner struct {
	gh     GH
	repo   string // owner/name
	ref    string // the branch e2e-live.yml runs on
	sha    string
	out    string
	count  int
	suites []string
	minors string // e2e-live.yml's minors input: "1.35 1.37"
	poll   time.Duration
	// jobWait is how long a job the run does not list is waited for.
	jobWait time.Duration
	log     func(format string, args ...any)

	mu         sync.Mutex
	run        *ciRun
	imagesDone bool
}

const workflow = "e2e-live.yml"

// ensureRun dispatches e2e-live.yml once for the gate's commit and returns
// the run.
func (c *ciRunner) ensureRun(ctx context.Context) (*ciRun, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.run != nil {
		return c.run, nil
	}
	path := filepath.Join(c.out, "ci-run.json")
	suites := strings.Join(c.suites, " ")
	if data, err := os.ReadFile(path); err == nil {
		var r ciRun
		if json.Unmarshal(data, &r) == nil && r.SHA == c.sha && r.Suites == suites && r.Minors == c.minors && r.Count == c.count && r.ID != 0 {
			c.run = &r
			return c.run, nil
		}
	}
	head, err := c.gh(ctx, "api", fmt.Sprintf("repos/%s/commits/%s", c.repo, c.ref), "--jq", ".sha")
	if err != nil {
		return nil, fmt.Errorf("read %s on GitHub: %w", c.ref, err)
	}
	if got := strings.TrimSpace(string(head)); got != c.sha {
		return nil, fmt.Errorf("branch %s on GitHub is at %.12s, not the gate's commit %.12s: push it, or pass -ref", c.ref, got, c.sha)
	}
	since := time.Now().UTC().Add(-time.Minute)
	if _, err := c.gh(ctx, "workflow", "run", workflow, "--repo", c.repo, "--ref", c.ref,
		"-f", "suites="+suites, "-f", fmt.Sprintf("count=%d", c.count), "-f", "minors="+c.minors); err != nil {
		return nil, fmt.Errorf("dispatch %s: %w", workflow, err)
	}
	c.log("dispatched %s on %s (suites: %s)", workflow, c.ref, suites)
	for {
		out, err := c.gh(ctx, "run", "list", "--repo", c.repo, "--workflow", workflow, "--branch", c.ref,
			"--event", "workflow_dispatch", "--limit", "10", "--json", "databaseId,headSha,createdAt")
		if err != nil {
			return nil, fmt.Errorf("list %s runs: %w", workflow, err)
		}
		var runs []struct {
			DatabaseID int64     `json:"databaseId"`
			HeadSha    string    `json:"headSha"`
			CreatedAt  time.Time `json:"createdAt"`
		}
		if err := json.Unmarshal(out, &runs); err != nil {
			return nil, fmt.Errorf("parse runs: %w", err)
		}
		sort.Slice(runs, func(i, j int) bool { return runs[i].CreatedAt.After(runs[j].CreatedAt) })
		for _, r := range runs {
			if r.HeadSha == c.sha && !r.CreatedAt.Before(since) {
				c.run = &ciRun{ID: r.DatabaseID, SHA: c.sha, Ref: c.ref, Suites: suites, Minors: c.minors, Count: c.count}
				data, _ := json.MarshalIndent(c.run, "", "  ")
				if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
					return nil, fmt.Errorf("save the run: %w", err)
				}
				c.log("CI run https://github.com/%s/actions/runs/%d", c.repo, r.DatabaseID)
				return c.run, nil
			}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(c.poll):
		}
	}
}

type ghJob struct {
	DatabaseID int64  `json:"databaseId"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	URL        string `json:"url"`
}

// runDone says whether every job of the run has finished.
func (c *ciRunner) runDone(ctx context.Context, run int64) (bool, error) {
	out, err := c.gh(ctx, "run", "view", fmt.Sprint(run), "--repo", c.repo, "--json", "status")
	if err != nil {
		return false, fmt.Errorf("view run %d: %w", run, err)
	}
	var v struct{ Status string }
	if err := json.Unmarshal(out, &v); err != nil {
		return false, fmt.Errorf("parse run %d: %w", run, err)
	}
	return v.Status == "completed", nil
}

func (c *ciRunner) job(ctx context.Context, run int64, name string) (ghJob, int, error) {
	out, err := c.gh(ctx, "run", "view", fmt.Sprint(run), "--repo", c.repo, "--json", "jobs,attempt")
	if err != nil {
		return ghJob{}, 0, fmt.Errorf("view run %d: %w", run, err)
	}
	var v struct {
		Attempt int     `json:"attempt"`
		Jobs    []ghJob `json:"jobs"`
	}
	if err := json.Unmarshal(out, &v); err != nil {
		return ghJob{}, 0, fmt.Errorf("parse run %d: %w", run, err)
	}
	for _, j := range v.Jobs {
		if j.Name == name {
			return j, v.Attempt, nil
		}
	}
	return ghJob{}, v.Attempt, nil
}

// Run waits for the step's job; a job that already failed (an earlier gate's
// result) is re-run first.
func (c *ciRunner) Run(ctx context.Context, st Step, dir string) error {
	run, err := c.ensureRun(ctx)
	if err != nil {
		return err
	}
	rerun := st.ciRetry
	missingSince := time.Time{}
	for {
		j, attempt, err := c.job(ctx, run.ID, st.CIJob.Name())
		if err != nil {
			return err
		}
		if j.Name == "" {
			// A job the run does not have (renamed, or a matrix that lists
			// other minors) would be waited for for ever.
			done, err := c.runDone(ctx, run.ID)
			if err != nil {
				return err
			}
			if missingSince.IsZero() {
				missingSince = time.Now()
			}
			if done || time.Since(missingSince) > c.jobWait {
				return fmt.Errorf("the CI run has no job %q", st.CIJob.Name())
			}
			if err := sleep(ctx, c.poll); err != nil {
				return err
			}
			continue
		}
		if j.Status == "completed" && j.Conclusion != "success" && rerun {
			// GitHub re-runs a job only once its whole run has finished.
			if done, err := c.runDone(ctx, run.ID); err != nil {
				return err
			} else if !done {
				if err := sleep(ctx, c.poll); err != nil {
					return err
				}
				continue
			}
			rerun = false
			c.log("%s: re-running the failed CI job %s", st.ID, j.URL)
			if _, err := c.gh(ctx, "run", "rerun", fmt.Sprint(run.ID), "--repo", c.repo, "--job", fmt.Sprint(j.DatabaseID)); err != nil {
				return fmt.Errorf("re-run job %d: %w", j.DatabaseID, err)
			}
			// The re-run shows as queued once GitHub takes it.
			if err := sleep(ctx, c.poll); err != nil {
				return err
			}
			continue
		}
		if j.Status == "completed" {
			if err := os.WriteFile(filepath.Join(dir, "ci-job.txt"), []byte(j.URL+"\n"), 0o644); err != nil {
				return fmt.Errorf("save the job URL: %w", err)
			}
			c.saveLog(ctx, run.ID, j, dir)
			c.fetchImages(ctx, run.ID)
			if j.Conclusion != "success" {
				return fmt.Errorf("CI job %s: %s", j.URL, j.Conclusion)
			}
			// A green job proves nothing without its results.
			return c.fetchResults(ctx, run.ID, attempt, st, dir)
		}
		if err := sleep(ctx, c.poll); err != nil {
			return err
		}
	}
}

func sleep(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

// saveLog writes the job's log, or at least its URL and conclusion, to
// dir/log.txt.
func (c *ciRunner) saveLog(ctx context.Context, run int64, j ghJob, dir string) {
	head := fmt.Sprintf("CI job %s: %s\n", j.URL, j.Conclusion)
	out, err := c.gh(ctx, "run", "view", fmt.Sprint(run), "--repo", c.repo, "--job", fmt.Sprint(j.DatabaseID), "--log")
	if err != nil {
		out = []byte(fmt.Sprintf("(the job log could not be read: %v)\n", err))
	}
	_ = os.WriteFile(filepath.Join(dir, "log.txt"), append([]byte(head), out...), 0o644)
}

// fetchImages downloads, once, the IDs of the images the run's suites ran
// (e2e-live.yml's e2e-images artifact) to the results directory.
func (c *ciRunner) fetchImages(ctx context.Context, run int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.imagesDone {
		return
	}
	dir := filepath.Join(c.out, "ci-images")
	if _, err := c.gh(ctx, "run", "download", fmt.Sprint(run), "--repo", c.repo, "-n", "e2e-images", "-D", dir); err != nil {
		c.log("the CI run left no e2e-images artifact: %v", err)
		return
	}
	c.imagesDone = true
}

// fetchResults downloads the job's artifact and keeps its test.json and
// summary.json in dir/e2e, where the report and the coverage proof read them.
// A job with no artifact, or no test results in it, is an error.
func (c *ciRunner) fetchResults(ctx context.Context, run int64, attempt int, st Step, dir string) error {
	art := filepath.Join(dir, "artifact")
	// A job not re-run in the latest attempt keeps the artifact of the
	// attempt it last ran in.
	found := false
	for a := attempt; a >= 1 && !found; a-- {
		name := fmt.Sprintf("e2e-live-%s-attempt-%d", st.CIJob.ID, a)
		_, err := c.gh(ctx, "run", "download", fmt.Sprint(run), "--repo", c.repo, "-n", name, "-D", art)
		found = err == nil
	}
	if !found {
		return fmt.Errorf("the CI job left no e2e-live-%s artifact", st.CIJob.ID)
	}
	if err := os.MkdirAll(filepath.Join(dir, "e2e"), 0o755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	for _, f := range []string{"test.json", "summary.json"} {
		matches, _ := filepath.Glob(filepath.Join(art, "*", f))
		if len(matches) == 0 {
			return fmt.Errorf("the CI job's artifact has no %s", f)
		}
		if err := os.Rename(matches[0], filepath.Join(dir, "e2e", f)); err != nil {
			return fmt.Errorf("keep %s: %w", f, err)
		}
	}
	return nil
}

// Cleanup has nothing to delete: the runner is gone with its job.
func (c *ciRunner) Cleanup(Step, string) {}

// routeRunner sends CI steps to the CI runner, the rest to bash.
type routeRunner struct{ local, ci Runner }

func (r routeRunner) Run(ctx context.Context, st Step, dir string) error {
	if st.CIJob != nil {
		return r.ci.Run(ctx, st, dir)
	}
	return r.local.Run(ctx, st, dir)
}

func (r routeRunner) Cleanup(st Step, dir string) {
	if st.CIJob != nil {
		r.ci.Cleanup(st, dir)
		return
	}
	r.local.Cleanup(st, dir)
}
