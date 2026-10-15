// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Command release-gate gathers a release's evidence on this host and writes
// one markdown report (make release-gate):
//
//   - every live e2e suite of hack/e2e/matrix.txt on the oldest and newest
//     Kubernetes minor hack/tool-versions.env has for it, and the upgrade
//     from the previous release;
//   - the scale suite's profiles (full and soak by default);
//   - go test -race -count=3, every fuzz target, the scenario matrix and the
//     enterprise scenario;
//   - govulncheck, golangci-lint, make hygiene, trivy on the images, the
//     strict docs build with check-site-offline, the bundle size budget;
//   - helm lint and template of every values profile, and make envtest (every
//     CRD and admission policy on the oldest and newest API servers).
//
// Results go to test/e2e/results/release-gate/<commit>/: a directory per
// step (log.txt, result.json, its outputs) and report.md. A step that passed
// is not run again for the same commit, so a second run re-runs only what
// failed (-fresh runs everything).
//
// Kind clusters: at most -clusters at once, each started only when the
// 1-minute load is at most -max-load and -min-mem GB are free. Heavy steps
// (the scale profiles, the -race -count=3 run, the enterprise scenario) take
// the host's heavy-load lock (-lock, a directory made with mkdir) one at a
// time.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "release-gate:", err)
		os.Exit(1)
	}
}

func logf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "[release-gate %s] %s\n", time.Now().UTC().Format("15:04:05"), fmt.Sprintf(format, args...))
}

func git(root string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out)), nil
}

func run() error {
	var (
		out       = flag.String("out", "", "results directory (default test/e2e/results/release-gate/<commit>)")
		clusters  = flag.Int("clusters", 1, "kind cluster budget: gitlab, multi-cluster and scale jobs take 2")
		maxLoad   = flag.Float64("max-load", 64, "start a cluster only while the 1-minute load is at most this")
		minMem    = flag.Float64("min-mem", 20, "start a cluster only while this many GB of memory are available")
		lock      = flag.String("lock", "", "heavy-load lock directory, made with mkdir (empty: no lock)")
		owner     = flag.String("owner", "release-gate", "who holds the heavy-load lock, written into it")
		count     = flag.Int("count", 1, "live e2e go test -count (the upgrade and scale suites run once)")
		fuzzTime  = flag.String("fuzztime", "5m", "go test -fuzztime for each fuzz target")
		scale     = flag.String("scale", "full,soak", "scale suite profiles to run (ci, full, soak), comma-separated")
		imgBuild  = flag.String("image-build", "docker", "images for trivy: docker (the repo Dockerfiles) or host (host-built binaries)")
		only      = flag.String("only", "", "run only the steps whose id matches this regexp; the others keep their cached result")
		fresh     = flag.Bool("fresh", false, "run every step again, ignoring the passes cached for this commit")
		list      = flag.Bool("list", false, "print the steps and exit")
		report    = flag.Bool("report", false, "write the report from the cached results and run nothing")
		dirtyOK   = flag.Bool("allow-dirty", false, "run on a tree with uncommitted changes (the report says so)")
		wait      = flag.Duration("wait", 30*time.Second, "how often to check for a free slot or a ready host")
		lockRetry = flag.Duration("lock-retry", 5*time.Minute, "how often to retry a heavy-load lock someone else holds")
		slots     = flag.String("slots", "", "the host's shared cluster slots: a cluster step takes DIR/1..-slot-max with mkdir (empty: none)")
		slotMax   = flag.Int("slot-max", 4, "how many cluster slots the host has")
		live      = flag.String("live", "local", "where the live suites run: local (kind on this host) or ci (one e2e-live.yml dispatch; scale stays local)")
		ref       = flag.String("ref", "", "with -live ci, the pushed branch at this commit (default the current branch)")
		ghRepo    = flag.String("repo", "pnz1990/kardinal-promoter", "with -live ci, the GitHub repository")
		floor     = flag.String("k8s-floor", "1.35", "the oldest supported Kubernetes minor: each suite runs on the oldest minor at or above it and the newest")
	)
	flag.Parse()

	root, err := git(".", "rev-parse", "--show-toplevel")
	if err != nil {
		return err
	}
	sha, err := git(root, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	status, err := git(root, "status", "--porcelain", "--untracked-files=no")
	if err != nil {
		return err
	}
	dirty := status != ""
	if dirty && !*dirtyOK && !*list && !*report {
		return fmt.Errorf("the tree has uncommitted changes; commit them, or pass -allow-dirty")
	}
	repo, err := LoadRepo(root)
	if err != nil {
		return err
	}
	_, ghFile := os.LookupEnv("KARDINAL_E2E_GITHUB_TOKEN_FILE")
	_, ghToken := os.LookupEnv("DEMO_GITHUB_TOKEN")
	var floorMinor int
	if _, err := fmt.Sscanf(*floor, "1.%d", &floorMinor); err != nil {
		return fmt.Errorf("-k8s-floor %q: want 1.<minor>", *floor)
	}
	if *live != "local" && *live != "ci" {
		return fmt.Errorf("-live %q: want local or ci", *live)
	}
	opts := Options{Count: *count, FuzzTime: *fuzzTime, ImageBuild: *imgBuild, GitHubToken: ghFile || ghToken,
		LiveCI: *live == "ci", Floor: floorMinor}
	for _, p := range strings.Split(*scale, ",") {
		if p = strings.TrimSpace(p); p != "" {
			opts.ScaleProfiles = append(opts.ScaleProfiles, p)
		}
	}
	steps := Plan(repo, opts)
	if *list {
		for _, st := range steps {
			note := ""
			switch {
			case st.Missing != "":
				note = "  MISSING: " + st.Missing
			case st.NotRun != "":
				note = "  not run: " + st.NotRun
			case st.CIJob != nil:
				note = "  CI job: " + st.CIJob.Name()
			}
			fmt.Printf("%-28s %-26s clusters=%d heavy=%v%s\n", st.ID, st.Area, st.Clusters, st.Heavy, note)
		}
		return nil
	}

	if *out == "" {
		*out = filepath.Join(root, "test", "e2e", "results", "release-gate", sha[:12])
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", *out, err)
	}
	selected := steps
	if *only != "" {
		re, err := regexp.Compile(*only)
		if err != nil {
			return fmt.Errorf("-only: %w", err)
		}
		selected = nil
		for _, st := range steps {
			if re.MatchString(st.ID) {
				selected = append(selected, st)
			}
		}
	}
	var runner Runner = bashRunner{root: root}
	if opts.LiveCI {
		if *ref == "" {
			if *ref, err = git(root, "rev-parse", "--abbrev-ref", "HEAD"); err != nil {
				return err
			}
		}
		runner = routeRunner{local: runner, ci: &ciRunner{
			gh: ghCLI, repo: *ghRepo, ref: *ref, sha: sha, out: *out, count: *count,
			suites: ciSuitesOf(selected), poll: time.Minute, log: logf,
		}}
	}
	g := &Gate{
		Out: *out, SHA: sha, Clusters: max(1, *clusters), Fresh: *fresh, Wait: *wait, Log: logf,
		Host: &machine{maxLoad: *maxLoad, minMemGB: *minMem, slotsDir: *slots, slotMax: *slotMax, lockDir: *lock,
			owner: fmt.Sprintf("%s pid %d %s", *owner, os.Getpid(), time.Now().UTC().Format(time.RFC3339)), lockRetry: *lockRetry, log: logf},
		Runner: runner,
	}

	start := time.Now()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	runErr := error(nil)
	ran := map[string]Result{}
	if !*report {
		logf("%d steps (%d selected), up to %d cluster slots; results in %s", len(steps), len(selected), g.Clusters, *out)
		// tools.sh once, so the steps find the pinned kind, kubectl and helm.
		cmd := exec.Command("bash", "hack/e2e/tools.sh")
		cmd.Dir, cmd.Stdout, cmd.Stderr = root, os.Stderr, os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("hack/e2e/tools.sh: %w", err)
		}
		var rs []Result
		rs, runErr = g.Run(ctx, selected)
		for _, r := range rs {
			ran[r.ID] = r
		}
	}
	// Every step's result: this run's, or the one saved for the commit.
	results := make([]Result, len(steps))
	for i, st := range steps {
		if r, ok := ran[st.ID]; ok {
			results[i] = r
		} else {
			results[i] = resultOf(g, st)
		}
	}
	if err := proveCoverage(root, *out, steps); err != nil {
		logf("coverage proof: %v", err)
	}
	cov, err := readCoverage(root, filepath.Join(*out, "coverage-proof.json"))
	if err != nil {
		return err
	}
	p := provenance(root, sha, dirty, repo, *out)
	f, err := os.Create(filepath.Join(*out, "report.md"))
	if err != nil {
		return fmt.Errorf("create report: %w", err)
	}
	if err := Render(f, steps, results, *out, cov, p, time.Since(start)); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close report: %w", err)
	}
	v := verdict(results)
	logf("verdict %s: %s", v, filepath.Join(*out, "report.md"))
	if runErr != nil {
		return fmt.Errorf("interrupted: %w", runErr)
	}
	if v != "GO" {
		return fmt.Errorf("verdict %s", v)
	}
	return nil
}

// ciSuitesOf is the suites of the selected CI steps, in plan order: the
// dispatch runs only those.
func ciSuitesOf(steps []Step) []string {
	seen := map[string]bool{}
	var out []string
	for _, st := range steps {
		if st.CIJob != nil && !seen[st.CIJob.Suite] {
			seen[st.CIJob.Suite] = true
			out = append(out, st.CIJob.Suite)
		}
	}
	return out
}

// ghCLI runs gh and returns its standard output.
func ghCLI(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "gh", args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return out, fmt.Errorf("gh %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// resultOf is the step's saved result, or what it is when it never ran.
func resultOf(g *Gate, st Step) Result {
	switch {
	case st.Missing != "":
		return Result{ID: st.ID, Status: statusMissing, Reason: st.Missing}
	case st.NotRun != "":
		return Result{ID: st.ID, Status: statusNotRun, Reason: st.NotRun}
	}
	data, err := os.ReadFile(filepath.Join(g.dir(st), "result.json"))
	if err == nil {
		var r Result
		if json.Unmarshal(data, &r) == nil && r.SHA == g.SHA {
			r.Cached = r.Status == statusPass
			return r
		}
	}
	return Result{ID: st.ID, Status: statusNotRun, Reason: "not run yet on this commit"}
}

// proveCoverage runs test/e2e/proof over the live suites' summaries.
func proveCoverage(root, out string, steps []Step) error {
	var files []string
	for _, st := range steps {
		if p := filepath.Join(out, st.ID, "e2e", "summary.json"); fileExists(p) {
			files = append(files, p)
		}
	}
	if len(files) == 0 {
		return nil
	}
	sort.Strings(files)
	cmd := exec.Command("go", append([]string{"run", "./test/e2e/proof", "-out", filepath.Join(out, "coverage-proof.json")}, files...)...)
	cmd.Dir = root
	log, err := os.Create(filepath.Join(out, "coverage-proof.log"))
	if err != nil {
		return fmt.Errorf("create proof log: %w", err)
	}
	defer func() { _ = log.Close() }()
	cmd.Stdout, cmd.Stderr = log, log
	// The proof fails when a row failed or did not run; the report counts them.
	_ = cmd.Run()
	return nil
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

var kroVersion = regexp.MustCompile(`KRO_VERSION="\$\{KRO_VERSION:-([^}]+)\}"`)

func provenance(root, sha string, dirty bool, repo *Repo, out string) Provenance {
	p := Provenance{SHA: sha, Dirty: dirty, PrevRelease: repo.PrevRelease}
	p.Describe, _ = git(root, "describe", "--tags", "--always", sha)
	if v, err := exec.Command("go", "env", "GOVERSION").Output(); err == nil {
		p.GoVersion = strings.TrimSpace(string(v))
	}
	if data, err := os.ReadFile(filepath.Join(root, "hack", "install-kro.sh")); err == nil {
		if m := kroVersion.FindSubmatch(data); m != nil {
			p.Tools = append(p.Tools, [2]string{"kro", "v" + string(m[1])})
		}
	}
	for _, k := range []string{"KIND_VERSION", "KUBECTL_VERSION", "HELM_VERSION", "TRIVY_VERSION"} {
		if v := repo.ToolVersions[k]; v != "" {
			p.Tools = append(p.Tools, [2]string{strings.ToLower(strings.TrimSuffix(k, "_VERSION")), v})
		}
	}
	var nodes []string
	for k := range repo.ToolVersions {
		if kindNode.MatchString(k) {
			nodes = append(nodes, k)
		}
	}
	sort.Strings(nodes)
	for _, k := range nodes {
		p.Tools = append(p.Tools, [2]string{k, repo.ToolVersions[k]})
	}
	p.Images = readImages(out)
	return p
}
