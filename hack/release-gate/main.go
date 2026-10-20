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
	"strconv"
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
		lock      = flag.String("lock", os.Getenv("RELEASE_GATE_LOCK"), "the host's heavy-load lock directory, made with mkdir (default $RELEASE_GATE_LOCK); heavy steps need it or -no-lock")
		noLock    = flag.Bool("no-lock", false, "run heavy steps without a heavy-load lock (a host of your own)")
		owner     = flag.String("owner", "release-gate", "who holds the heavy-load lock, written into it")
		count     = flag.Int("count", 2, "live e2e go test -count (the upgrade and scale suites run once)")
		fuzzTime  = flag.String("fuzztime", "5m", "go test -fuzztime for each fuzz target")
		scale     = flag.String("scale", "full,soak", "scale suite profiles to run (ci, full, soak), comma-separated")
		imgBuild  = flag.String("image-build", "docker", "images for trivy: docker (the repo Dockerfiles) or host (host-built binaries)")
		only      = flag.String("only", "", "run only the steps whose id matches this regexp; the others keep their cached result")
		fresh     = flag.Bool("fresh", false, "run every step again, ignoring the passes cached for this commit")
		list      = flag.Bool("list", false, "print the steps and exit")
		report    = flag.Bool("report", false, "write the report from the cached results and run nothing")
		dirtyOK   = flag.Bool("allow-dirty", false, "run on a tree with uncommitted changes (the report says so)")
		wait      = flag.Duration("wait", 30*time.Second, "how often to check for a free slot or a ready host")
		lockRetry = flag.Duration("lock-retry", 10*time.Second, "how often a waiter for the heavy-load lock checks the queue, at the latest (it also wakes on the queue's changes)")
		priority  = flag.String("priority", "normal", "the heavy-load lock queue class: release (a release gate: served before every other waiter) or normal")
		slots     = flag.String("slots", os.Getenv("RELEASE_GATE_SLOTS"), "the host's shared cluster slots: a cluster step takes DIR/1..-slot-max with mkdir (default $RELEASE_GATE_SLOTS); cluster steps need it or -no-slots")
		noSlots   = flag.Bool("no-slots", false, "start clusters without taking the host's cluster slots (a host of your own)")
		slotMax   = flag.Int("slot-max", 0, "how many cluster slots the host has (default $SLOT_MAX, else SLOT_MAX=N in the host's host.env next to the -slots directory, else 2, as hack/e2e/local-live.sh): no cluster starts while that many slot directories are held, one above it too. More than the host's (e.g. -slot-max 4 for the two-cluster scale profile, #2107) is for an exclusive heavy run only: under the heavy-load lock held alone, with the host drained and its load under 16")
		live      = flag.String("live", "local", "where the live suites run: local (kind on this host) or ci (one e2e-live.yml dispatch; scale stays local)")
		ref       = flag.String("ref", "", "with -live ci, the pushed branch at this commit (default the current branch)")
		ghRepo    = flag.String("repo", "pnz1990/kardinal-promoter", "with -live ci, the GitHub repository")
	)
	flag.Parse()
	if *priority != "release" && *priority != "normal" {
		return fmt.Errorf("-priority %q: want release or normal", *priority)
	}

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
	if *live != "local" && *live != "ci" {
		return fmt.Errorf("-live %q: want local or ci", *live)
	}
	// The oldest supported and the newest Kubernetes: hack/tool-versions.env.
	floor, newest := repo.KubeMinors()
	if floor == 0 {
		return fmt.Errorf("hack/tool-versions.env has no KIND_NODE_1_<minor>")
	}
	opts := Options{Count: *count, FuzzTime: *fuzzTime, ImageBuild: *imgBuild, GitHubToken: ghFile || ghToken,
		LiveCI: *live == "ci", Floor: floor, Newest: newest}
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
	// The host rules are on unless turned off by name: heavy steps take the
	// heavy-load lock, clusters the host's slots.
	if !*report {
		if err := hostRulesProblem(selected, *lock, *slots, *noLock, *noSlots); err != nil {
			return err
		}
	}
	if *noLock {
		*lock = ""
	}
	if *noSlots {
		*slots = ""
	}
	hostSlots, err := slotMaxOr(*slotMax, *slots)
	if err != nil {
		return err
	}
	if *priority == "release" {
		if err := releaseTokenProblem(*lock, *slots); err != nil {
			return err
		}
	}
	tools, err := os.ReadFile(filepath.Join(root, "hack", "tool-versions.env"))
	if err != nil {
		return fmt.Errorf("read tool versions: %w", err)
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
			suites: ciSuitesOf(selected), minors: minorsInput(opts), poll: time.Minute, jobWait: 30 * time.Minute, log: logf,
		}}
	}
	g := &Gate{
		Out: *out, SHA: sha, Dirty: dirty, Clusters: max(1, *clusters), Fresh: *fresh, Wait: *wait, Log: logf,
		// What every step's result depends on besides its own scripts.
		Inputs: fmt.Sprintf("%s\x00count=%d fuzztime=%s image-build=%s live=%s minors=%s",
			tools, *count, *fuzzTime, *imgBuild, *live, minorsInput(opts)),
		Host: &machine{maxLoad: *maxLoad, minMemGB: *minMem, slotsDir: *slots, slotMax: hostSlots, lockDir: *lock,
			owner: fmt.Sprintf("%s pid %d %s", *owner, os.Getpid(), time.Now().UTC().Format(time.RFC3339)), lockRetry: *lockRetry, lockClass: lockClassOf(*priority), log: logf},
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
	proofErr := proveCoverage(root, *out, steps, results)
	cov, err := readCoverage(root, filepath.Join(*out, "coverage-proof.json"))
	if err != nil {
		return err
	}
	if cov.Allowed, err = readAllowlist(filepath.Join(root, "hack", "release-gate", "coverage-allow.txt")); err != nil {
		return err
	}
	if proofErr != nil {
		cov.ProofError = proofErr.Error()
	}
	p := provenance(root, sha, dirty, repo, *out)
	p.RanImages = readRanImages(*out, steps, results)
	for i, st := range steps {
		if st.ID == "images" && results[i].Status != statusPass {
			p.Images = nil // the scan did not pass on this commit with these inputs
		}
	}
	f, err := os.Create(filepath.Join(*out, "report.md"))
	if err != nil {
		return fmt.Errorf("create report: %w", err)
	}
	if err := Render(f, steps, results, *out, cov, repo.NotRunItems, p, time.Since(start)); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close report: %w", err)
	}
	v := verdictOf(results, cov)
	logf("verdict %s: %s", v, filepath.Join(*out, "report.md"))
	if runErr != nil {
		return fmt.Errorf("interrupted: %w", runErr)
	}
	if v != "GO" {
		return fmt.Errorf("verdict %s", v)
	}
	return nil
}

// releaseTokenProblem refuses -priority release unless
// $RELEASE_GATE_PRIORITY_TOKEN matches the token file the coordinator keeps
// next to the heavy-load lock (or, with no lock, the slots directory):
// DIR/.release-token, this user's, mode 600 (#2282 QA: no one else jumps the
// queues by saying so). hack/loadlock.sh and hack/e2e/local-live.sh check the
// same.
func releaseTokenProblem(lock, slots string) error {
	dir := lock
	if dir == "" {
		dir = slots
	}
	if dir == "" {
		return nil // no shared queue to jump
	}
	path := filepath.Join(filepath.Dir(filepath.Clean(dir)), ".release-token")
	refuse := fmt.Errorf("-priority release needs RELEASE_GATE_PRIORITY_TOKEN matching %s (mode 600): refusing to jump the queue", path)
	st, err := os.Stat(path)
	if err != nil || st.Mode().Perm() != 0o600 {
		return refuse
	}
	if sys, ok := st.Sys().(*syscall.Stat_t); !ok || int(sys.Uid) != os.Getuid() {
		return refuse
	}
	b, err := os.ReadFile(path)
	want := strings.TrimSpace(strings.SplitN(string(b), "\n", 2)[0]) // its first line, as the shell's read
	if err != nil || want == "" || os.Getenv("RELEASE_GATE_PRIORITY_TOKEN") != want {
		return refuse
	}
	return nil
}

// maxSlots is the most cluster slots a host may have: the two-cluster scale
// profile's -slot-max 4 (#2107); hack/e2e/local-live.sh's MAX_SLOTS.
const maxSlots = 4

// slotMaxOr is -slot-max when it was given (above 0), else slotMaxDefault;
// out of 1..maxSlots it is refused (#2244 QA: never more room than that).
func slotMaxOr(flagged int, slotsDir string) (int, error) {
	if flagged < 0 || flagged > maxSlots {
		return 0, fmt.Errorf("-slot-max %d: the host has 1 to %d cluster slots", flagged, maxSlots)
	}
	if flagged > 0 {
		return flagged, nil
	}
	return slotMaxDefault(slotsDir)
}

// slotMaxDefault is the host's cluster slots, as hack/e2e/local-live.sh
// reads them: $SLOT_MAX, else SLOT_MAX=N in host.env next to the slots
// directory (DIR/../host.env, where the host's capacity is tuned), else 2.
// A value out of 1..maxSlots, from either, is refused; a host.env that
// cannot be read leaves 2, the fewest.
func slotMaxDefault(slotsDir string) (int, error) {
	if v := os.Getenv("SLOT_MAX"); v != "" {
		return checkSlotMax(v, "$SLOT_MAX")
	}
	if slotsDir != "" {
		path := filepath.Join(filepath.Dir(filepath.Clean(slotsDir)), "host.env")
		if b, err := os.ReadFile(path); err == nil {
			v, found := "", false
			for _, l := range strings.Split(string(b), "\n") {
				if rest, ok := strings.CutPrefix(strings.TrimRight(l, "\r"), "SLOT_MAX="); ok {
					v, found = rest, true
				}
			}
			if found {
				return checkSlotMax(v, path)
			}
		}
	}
	return 2, nil
}

// checkSlotMax is v as the host's cluster slots, refused out of
// 1..maxSlots (a plain number: no sign, no leading zero or space).
func checkSlotMax(v, from string) (int, error) {
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || n > maxSlots || strconv.Itoa(n) != v {
		return 0, fmt.Errorf("SLOT_MAX=%s (%s): the host has 1 to %d cluster slots", v, from, maxSlots)
	}
	return n, nil
}

// hostRulesProblem refuses heavy steps without the heavy-load lock and
// cluster steps without the host's cluster slots, unless the run opts out by
// name (-no-lock, -no-slots): the host rules are on by default.
func hostRulesProblem(steps []Step, lock, slots string, noLock, noSlots bool) error {
	for _, st := range steps {
		if st.Script == "" {
			continue
		}
		if st.Heavy && lock == "" && !noLock {
			return fmt.Errorf("step %s is heavy: pass -lock DIR (the host's heavy-load lock, or $RELEASE_GATE_LOCK), or -no-lock on a host of your own", st.ID)
		}
		if st.Clusters > 0 && slots == "" && !noSlots {
			return fmt.Errorf("step %s starts kind clusters: pass -slots DIR (the host's cluster slots, or $RELEASE_GATE_SLOTS), or -no-slots on a host of your own", st.ID)
		}
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

// resultOf is the step's saved result for this commit and these inputs, or
// what it is when it never ran so.
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
		if json.Unmarshal(data, &r) == nil && r.SHA == g.SHA && r.Key == g.key(st) {
			switch {
			case r.Dirty && !g.Dirty:
				return Result{ID: st.ID, Status: statusNotRun, Reason: "ran only on a tree with uncommitted changes"}
			case r.Status == statusPass:
				if why := g.proven(st, g.dir(st)); why != "" {
					return Result{ID: st.ID, Status: statusFail, SHA: r.SHA, Key: r.Key, Dirty: r.Dirty,
						Reason: "not proven: " + why, Started: r.Started, Seconds: r.Seconds}
				}
				r.Cached = true
			}
			return r
		}
	}
	return Result{ID: st.ID, Status: statusNotRun, Reason: "not run yet on this commit with these inputs"}
}

// proveCoverage runs test/e2e/proof over the live summaries of the steps that
// ran on this commit with these inputs (passed or failed). Its exit code
// is not the verdict: the rows' results are (verdictOf). An error, or no live
// results at all, leaves no coverage-proof.json, and the gate is NO-GO.
func proveCoverage(root, out string, steps []Step, results []Result) error {
	proof := filepath.Join(out, "coverage-proof.json")
	if err := os.Remove(proof); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove the old proof: %w", err)
	}
	var files []string
	for i, st := range steps {
		if results[i].Status != statusPass && results[i].Status != statusFail {
			continue
		}
		if p := filepath.Join(out, st.ID, "e2e", "summary.json"); fileExists(p) {
			files = append(files, p)
		}
	}
	if len(files) == 0 {
		return fmt.Errorf("no live suite ran on this commit with these inputs")
	}
	sort.Strings(files)
	cmd := exec.Command("go", append([]string{"run", "./test/e2e/proof", "-out", proof}, files...)...)
	cmd.Dir = root
	log, err := os.Create(filepath.Join(out, "coverage-proof.log"))
	if err != nil {
		return fmt.Errorf("create proof log: %w", err)
	}
	defer func() { _ = log.Close() }()
	cmd.Stdout, cmd.Stderr = log, log
	// proof exits 1 when a row failed or did not run, which the rows say;
	// any other failure leaves no file.
	_ = cmd.Run()
	if !fileExists(proof) {
		return fmt.Errorf("test/e2e/proof wrote no result (coverage-proof.log)")
	}
	return nil
}

// minorsInput is e2e-live.yml's minors input: "1.35 1.37".
func minorsInput(o Options) string {
	var ms []string
	for _, m := range o.Minors() {
		ms = append(ms, fmt.Sprintf("1.%d", m))
	}
	return strings.Join(ms, " ")
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
