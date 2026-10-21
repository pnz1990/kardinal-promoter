// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"context"
	"crypto/sha256"
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
	// adoptWait is how long a run is looked for after a dispatch that
	// failed with anything but a rate limit: GitHub may have started it.
	adoptWait time.Duration
	log       func(format string, args ...any)

	mu         sync.Mutex
	run        *ciRun
	runErr     error // why ensureRun failed: kept, so the gate dispatches once, not once per CI step
	imagesDone bool

	// The shared look at the run (view): one gh call per poll interval for
	// every CI step, not one per step (#2096).
	viewMu  sync.Mutex
	viewAt  time.Time
	viewRun int64
	last    runView

	// wait sleeps between retries; tests make it instant.
	wait func(ctx context.Context, d time.Duration) error
}

const workflow = "e2e-live.yml"

// ensureRun dispatches e2e-live.yml once for the gate's commit and returns
// the run. Its error is every CI step's: a later step does not dispatch
// again, since a second dispatch after one that GitHub took would start a
// second run.
func (c *ciRunner) ensureRun(ctx context.Context) (*ciRun, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.run != nil || c.runErr != nil {
		return c.run, c.runErr
	}
	r, err := c.findOrDispatch(ctx)
	c.run, c.runErr = r, err
	return r, err
}

// gateTitle is the name e2e-live.yml gives a run dispatched with the gate
// input (its run-name): the gate finds its run by it.
const gateTitle = "E2E live: release gate %s"

// key is the gate's key, its commit and the run's inputs: a run dispatched
// with it is this gate's, whichever attempt dispatched it. e2e-live.yml
// gives such a run a concurrency group of its own.
func (c *ciRunner) key() string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%d", strings.Join(c.suites, " "), c.minors, c.count)))
	return fmt.Sprintf("%.12s-%x", c.sha, sum[:4])
}

// findOrDispatch returns the gate's run: the one ci-run.json saved, else
// one of this gate's runs still going (an earlier attempt whose dispatch
// answer was lost, or whose results directory is gone), else a new
// dispatch's. A dispatch that fails with anything but a rate limit may
// still have started a run: it is looked for, for adoptWait, before the
// error stands.
func (c *ciRunner) findOrDispatch(ctx context.Context) (*ciRun, error) {
	path := filepath.Join(c.out, "ci-run.json")
	suites := strings.Join(c.suites, " ")
	if data, err := os.ReadFile(path); err == nil {
		var r ciRun
		if json.Unmarshal(data, &r) == nil && r.SHA == c.sha && r.Suites == suites && r.Minors == c.minors && r.Count == c.count && r.ID != 0 {
			return &r, nil
		}
	}
	head, err := c.call(ctx, "api", fmt.Sprintf("repos/%s/commits/%s", c.repo, c.ref), "--jq", ".sha")
	if err != nil {
		return nil, fmt.Errorf("read %s on GitHub: %w", c.ref, err)
	}
	if got := strings.TrimSpace(string(head)); got != c.sha {
		return nil, fmt.Errorf("branch %s on GitHub is at %.12s, not the gate's commit %.12s: push it, or pass -ref", c.ref, got, c.sha)
	}
	key := c.key()
	save := func(id int64) (*ciRun, error) {
		run := &ciRun{ID: id, SHA: c.sha, Ref: c.ref, Suites: suites, Minors: c.minors, Count: c.count}
		data, _ := json.MarshalIndent(run, "", "  ")
		if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
			return nil, fmt.Errorf("save the run: %w", err)
		}
		c.log("CI run https://github.com/%s/actions/runs/%d", c.repo, id)
		return run, nil
	}
	if id, status, err := c.findRun(ctx, key, time.Time{}, true); err != nil {
		return nil, err
	} else if id != 0 {
		c.log("adopting this gate's CI run %d, still %s, instead of dispatching again", id, status)
		return save(id)
	}
	since := time.Now().UTC().Add(-time.Minute)
	if err := c.dispatch(ctx, "workflow", "run", workflow, "--repo", c.repo, "--ref", c.ref,
		"-f", "suites="+suites, "-f", fmt.Sprintf("count=%d", c.count), "-f", "minors="+c.minors, "-f", "gate="+key); err != nil {
		if !rateLimited(err) {
			// GitHub may have taken the dispatch and lost the answer.
			for deadline := time.Now().Add(c.adoptWait); ; {
				if id, _, ferr := c.findRun(ctx, key, since, false); ferr == nil && id != 0 {
					c.log("dispatch %s: %v; GitHub started the run anyway", workflow, err)
					return save(id)
				}
				if !time.Now().Before(deadline) {
					break
				}
				if werr := sleep(ctx, c.poll); werr != nil {
					return nil, werr
				}
			}
		}
		return nil, fmt.Errorf("dispatch %s: %w", workflow, err)
	}
	c.log("dispatched %s on %s (suites: %s; gate %s)", workflow, c.ref, suites, key)
	for {
		id, _, err := c.findRun(ctx, key, since, false)
		if err != nil {
			return nil, err
		}
		if id != 0 {
			return save(id)
		}
		if err := sleep(ctx, c.poll); err != nil {
			return nil, err
		}
	}
}

// findRun returns the newest e2e-live run on the gate's branch that is this
// gate's (its commit, and its name from key) and was created at or after
// since; with going, only one not yet completed. 0 is none.
func (c *ciRunner) findRun(ctx context.Context, key string, since time.Time, going bool) (int64, string, error) {
	out, err := c.call(ctx, "run", "list", "--repo", c.repo, "--workflow", workflow, "--branch", c.ref,
		"--event", "workflow_dispatch", "--limit", "20", "--json", "databaseId,headSha,createdAt,displayTitle,status")
	if err != nil {
		return 0, "", fmt.Errorf("list %s runs: %w", workflow, err)
	}
	var runs []struct {
		DatabaseID   int64     `json:"databaseId"`
		HeadSha      string    `json:"headSha"`
		CreatedAt    time.Time `json:"createdAt"`
		DisplayTitle string    `json:"displayTitle"`
		Status       string    `json:"status"`
	}
	if err := json.Unmarshal(out, &runs); err != nil {
		return 0, "", fmt.Errorf("parse runs: %w", err)
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].CreatedAt.After(runs[j].CreatedAt) })
	title := fmt.Sprintf(gateTitle, key)
	for _, r := range runs {
		if r.HeadSha == c.sha && r.DisplayTitle == title && !r.CreatedAt.Before(since) && (!going || r.Status != "completed") {
			return r.DatabaseID, r.Status, nil
		}
	}
	return 0, "", nil
}

type ghJob struct {
	DatabaseID int64  `json:"databaseId"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	URL        string `json:"url"`
}

// runView is one look at the run: its status, its latest attempt and its
// jobs.
type runView struct {
	Status  string  `json:"status"`
	Attempt int     `json:"attempt"`
	Jobs    []ghJob `json:"jobs"`
}

func (v runView) job(name string) ghJob {
	for _, j := range v.Jobs {
		if j.Name == name {
			return j
		}
	}
	return ghJob{}
}

// view returns the run as one gh call shared by every CI step saw it: a
// call at most every poll interval, whatever the number of steps waiting
// (32 steps polling on their own made 1,900 calls an hour, and the account's
// limit of 5,000 is shared by every agent: #2096). A call that fails is
// retried (poll errors never fail a step), honouring GitHub's rate limits.
func (c *ciRunner) view(ctx context.Context, run int64) (runView, error) {
	c.viewMu.Lock()
	defer c.viewMu.Unlock()
	if c.viewRun == run && !c.viewAt.IsZero() && time.Since(c.viewAt) < c.poll {
		return c.last, nil
	}
	for attempt := 1; ; attempt++ {
		out, err := c.gh(ctx, "run", "view", fmt.Sprint(run), "--repo", c.repo, "--json", "status,attempt,jobs")
		var v runView
		if err == nil {
			if err = json.Unmarshal(out, &v); err == nil {
				c.last, c.viewAt, c.viewRun = v, time.Now(), run
				return v, nil
			}
			err = fmt.Errorf("parse run %d: %w", run, err)
		}
		d := c.backoff(ctx, err, attempt)
		c.log("view CI run %d: %v; retrying in %s", run, err, d)
		if werr := c.sleepFor(ctx, d); werr != nil {
			return runView{}, werr
		}
	}
}

// forget makes the next view call GitHub (after a re-run).
func (c *ciRunner) forget() {
	c.viewMu.Lock()
	c.viewAt = time.Time{}
	c.viewMu.Unlock()
}

// call is a gh call retried, up to callAttempts times, while it fails with
// a transient error (a rate limit, a 5xx, a dropped connection).
func (c *ciRunner) call(ctx context.Context, args ...string) ([]byte, error) {
	for attempt := 1; ; attempt++ {
		out, err := c.gh(ctx, args...)
		if err == nil || !transient(err) || attempt >= callAttempts {
			return out, err
		}
		d := c.backoff(ctx, err, attempt)
		c.log("gh %s: %v; retrying in %s", strings.Join(args[:min(len(args), 3)], " "), err, d)
		if werr := c.sleepFor(ctx, d); werr != nil {
			return nil, werr
		}
	}
}

// callAttempts bounds the retries of a call that is not a poll.
const callAttempts = 8

// dispatch is the workflow dispatch, retried only while GitHub's rate limit
// refuses it: a refused request dispatched nothing. Any other error is
// returned at once: after a 5xx or a dropped connection GitHub may have taken
// the dispatch, and dispatching again would start a second run.
func (c *ciRunner) dispatch(ctx context.Context, args ...string) error {
	for attempt := 1; ; attempt++ {
		_, err := c.gh(ctx, args...)
		if err == nil || !rateLimited(err) || attempt >= callAttempts {
			return err
		}
		d := c.backoff(ctx, err, attempt)
		c.log("dispatch %s: %v; retrying in %s", workflow, err, d)
		if werr := c.sleepFor(ctx, d); werr != nil {
			return werr
		}
	}
}

// rateLimited says whether GitHub refused a request for a primary or
// secondary rate limit.
func rateLimited(err error) bool {
	m := strings.ToLower(err.Error())
	return strings.Contains(m, "rate limit") || strings.Contains(m, "http 429")
}

// transient says whether a gh error is worth retrying: GitHub's primary and
// secondary rate limits (403, 429), its 5xx, and dropped connections.
func transient(err error) bool {
	m := strings.ToLower(err.Error())
	for _, s := range []string{"rate limit", "http 429", "http 500", "http 502", "http 503", "http 504",
		"timeout", "connection reset", "eof", "tls handshake"} {
		if strings.Contains(m, s) {
			return true
		}
	}
	return false
}

// backoff is how long to wait before retrying after err, the attempt-th
// failure: until the primary rate limit resets (gh api rate_limit, which does
// not count against it), a minute and more for a secondary limit, else
// exponential from 15 seconds; at most maxBackoff.
func (c *ciRunner) backoff(ctx context.Context, err error, attempt int) time.Duration {
	m := strings.ToLower(err.Error())
	exp := func(base time.Duration) time.Duration {
		return min(base<<min(attempt-1, 6), maxBackoff)
	}
	switch {
	case strings.Contains(m, "secondary rate limit"):
		return exp(time.Minute)
	case strings.Contains(m, "rate limit"):
		if out, rerr := c.gh(ctx, "api", "rate_limit", "--jq", ".resources.core.reset"); rerr == nil {
			var reset int64
			if _, serr := fmt.Sscan(strings.TrimSpace(string(out)), &reset); serr == nil && reset > 0 {
				if d := time.Until(time.Unix(reset, 0)) + 5*time.Second; d > 0 {
					return min(d, maxBackoff)
				}
			}
		}
		return exp(time.Minute)
	}
	return exp(15 * time.Second)
}

// maxBackoff bounds one wait: GitHub's primary limit resets within the hour.
const maxBackoff = time.Hour

func (c *ciRunner) sleepFor(ctx context.Context, d time.Duration) error {
	if c.wait != nil {
		return c.wait(ctx, d)
	}
	return sleep(ctx, d)
}

// Run waits for the step's job, from the shared view of the run; a job that
// already failed (an earlier gate's result) is re-run first. Only the job's
// conclusion fails the step: a poll error is retried (#2096).
func (c *ciRunner) Run(ctx context.Context, st Step, dir string) error {
	run, err := c.ensureRun(ctx)
	if err != nil {
		return err
	}
	rerun := st.ciRetry
	missingSince := time.Time{}
	for {
		v, err := c.view(ctx, run.ID)
		if err != nil {
			return err // the context ended
		}
		j := v.job(st.CIJob.Name())
		if j.Name == "" {
			// A job the run does not have (renamed, or a matrix that lists
			// other minors) would be waited for for ever.
			if missingSince.IsZero() {
				missingSince = time.Now()
			}
			if v.Status == "completed" || time.Since(missingSince) > c.jobWait {
				return fmt.Errorf("the CI run has no job %q", st.CIJob.Name())
			}
			if err := sleep(ctx, c.poll); err != nil {
				return err
			}
			continue
		}
		if j.Status == "completed" && j.Conclusion != "success" && rerun {
			// GitHub re-runs a job only once its whole run has finished.
			if v.Status != "completed" {
				if err := sleep(ctx, c.poll); err != nil {
					return err
				}
				continue
			}
			rerun = false
			c.log("%s: re-running the failed CI job %s", st.ID, j.URL)
			if _, err := c.call(ctx, "run", "rerun", fmt.Sprint(run.ID), "--repo", c.repo, "--job", fmt.Sprint(j.DatabaseID)); err != nil {
				return fmt.Errorf("re-run job %d: %w", j.DatabaseID, err)
			}
			c.forget()
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
			return c.fetchResults(ctx, run.ID, v.Attempt, st, dir)
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
	out, err := c.call(ctx, "run", "view", fmt.Sprint(run), "--repo", c.repo, "--job", fmt.Sprint(j.DatabaseID), "--log")
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
	if _, err := c.call(ctx, "run", "download", fmt.Sprint(run), "--repo", c.repo, "-n", "e2e-images", "-D", dir); err != nil {
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
		_, err := c.call(ctx, "run", "download", fmt.Sprint(run), "--repo", c.repo, "-n", name, "-D", art)
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
