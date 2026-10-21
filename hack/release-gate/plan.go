// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// The areas of the report, in its order.
const (
	areaE2E     = "Live e2e"
	areaUpgrade = "Upgrade"
	areaScale   = "Scale and soak"
	areaUnit    = "Unit, fuzz and scenarios"
	areaStatic  = "Static checks"
	areaChart   = "Chart"
)

var areas = []string{areaE2E, areaUpgrade, areaScale, areaUnit, areaStatic, areaChart}

// Step is one piece of evidence: a bash script run from the repository root,
// whose result is cached per commit.
type Step struct {
	ID    string
	Area  string
	Title string
	// Script runs under bash -euo pipefail in the repository root, with
	// STEP_DIR (the step's directory, for its outputs) and KUBECONFIG
	// (STEP_DIR/kubeconfig for a step with clusters, /dev/null otherwise).
	Script string
	// Cleanup runs after Script, also when the gate is interrupted: it
	// deletes the step's clusters.
	Cleanup string
	// Clusters is how many cluster slots of the budget the step takes
	// (0: none; a step needs at least one kind cluster to take one).
	Clusters int
	// Heavy steps hold the host's heavy-load lock while they run.
	Heavy bool
	// Exclusive heavy steps hold it alone (the scale suite): with the
	// counting lock, no second holder runs beside them.
	Exclusive bool
	// Tests is the go test -json file the step writes, relative to its
	// directory, for the totals.
	Tests string
	// NotRun, when set, is why the step cannot run here: it is reported,
	// never run, and the gate is incomplete.
	NotRun string
	// Pinned, when set, is why the suite cannot run on this minor:
	// hack/e2e/matrix.txt pins it to its own minors. The step is reported
	// with the reason and never run; the verdict does not wait for it, as no
	// host could run it.
	Pinned string
	// Missing, when set, is evidence the release needs that this commit has
	// no test for: the gate fails.
	Missing string
	// MinTests is how many tests (and subtests, per run) its Tests file must
	// record for a pass: a step that ran no test proves nothing. MaxSkips
	// is how many of them may skip (they are listed in the report).
	MinTests, MaxSkips int
	// CIJob, when set, is the e2e-live.yml job that runs the step (-live ci).
	CIJob *ciJob
	// ciRetry: the step failed in an earlier gate on this commit, so its CI
	// job runs again.
	ciRetry bool
}

// Options are the plan's knobs (flags).
type Options struct {
	Count         int      // live e2e go test -count
	FuzzTime      string   // go test -fuzztime per fuzz target
	ScaleProfiles []string // scale suite profiles: ci, full, soak
	ImageBuild    string   // docker (the repo Dockerfiles) or host (host-built binaries)
	GitHubToken   bool     // a GitHub token for the github suite is set
	LiveCI        bool     // the live suites run in e2e-live.yml, not on this host
	// Floor and Newest are the oldest supported and the newest Kubernetes
	// minors (35, 37): every live suite runs on exactly these two, and
	// envtest installs the CRDs on them.
	Floor, Newest int
}

// Minors are the floor and the newest minor, once each.
func (o Options) Minors() []int {
	if o.Newest == o.Floor {
		return []int{o.Floor}
	}
	return []int{o.Floor, o.Newest}
}

// KubeMinors are the oldest and newest KIND_NODE_1_<minor> of
// hack/tool-versions.env: the supported floor and the newest Kubernetes.
func (r *Repo) KubeMinors() (floor, newest int) {
	for k := range r.ToolVersions {
		m := kindNode.FindStringSubmatch(k)
		if m == nil || m[1] != "" {
			continue
		}
		n, _ := strconv.Atoi(m[2])
		if floor == 0 || n < floor {
			floor = n
		}
		newest = max(newest, n)
	}
	return floor, newest
}

// fuzzHeavy: a fuzz run longer than a minute takes the heavy-load lock
// (fuzzing uses every core it is given).
func fuzzHeavy(fuzzTime string) bool {
	d, err := time.ParseDuration(fuzzTime)
	return err != nil || d > time.Minute
}

// Repo is what the plan reads from the checkout.
type Repo struct {
	Root   string
	Matrix []MatrixLine // hack/e2e/matrix.txt
	// Pinned are the suites matrix.txt runs only on the minors their lines
	// list (a fifth column, pinned), each with the comment above its first
	// line: why.
	Pinned       map[string]string
	ToolVersions map[string]string // hack/tool-versions.env
	FuzzTargets  []FuzzTarget
	PrevRelease  string // the release before this commit, e.g. v0.11.0
	Enterprise   bool   // test/scenario/enterprise exists
	// NotRunItems are what the gate does not run (hack/release-gate/not-run.txt).
	NotRunItems []NotRunItem
	// ReleaseTargets are the binaries release.yml ships, for every platform.
	ReleaseTargets []BuildTarget
}

// MatrixLine is a line of hack/e2e/matrix.txt.
type MatrixLine struct{ Suite, Minor, Shard string }

// FuzzTarget is a Fuzz function and its package directory.
type FuzzTarget struct{ Name, Dir string }

var (
	kindNode   = regexp.MustCompile(`^KIND_NODE_(?:([A-Z][A-Z0-9_]*)_)?1_(\d+)$`)
	fuzzFunc   = regexp.MustCompile(`(?m)^func (Fuzz\w+)\(\w+ \*testing\.F\)`)
	releaseTag = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)
)

// LoadRepo reads the matrix, the tool versions, the fuzz targets, the
// previous release, the not-run list and the release targets of the
// checkout at root: a broken not-run list stops the gate before it runs
// anything.
func LoadRepo(root string) (*Repo, error) {
	r := &Repo{Root: root}
	var err error
	if r.Matrix, r.Pinned, err = readMatrix(filepath.Join(root, "hack", "e2e", "matrix.txt")); err != nil {
		return nil, err
	}
	if r.ToolVersions, err = readEnv(filepath.Join(root, "hack", "tool-versions.env")); err != nil {
		return nil, err
	}
	if err := r.checkPins(); err != nil {
		return nil, err
	}
	if r.FuzzTargets, err = findFuzzTargets(root); err != nil {
		return nil, err
	}
	if r.PrevRelease, err = previousRelease(root); err != nil {
		return nil, err
	}
	if r.NotRunItems, err = readNotRun(root); err != nil {
		return nil, err
	}
	if r.ReleaseTargets, err = releaseTargets(root); err != nil {
		return nil, err
	}
	if st, err := os.Stat(filepath.Join(root, "test", "scenario", "enterprise")); err == nil && st.IsDir() {
		r.Enterprise = true
	}
	return r, nil
}

// readMatrix reads matrix.txt's lines, and its pinned suites: the suites
// whose lines say pinned (all of them), each with the comment right above
// its first line, which must say why.
func readMatrix(path string) ([]MatrixLine, map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("read matrix: %w", err)
	}
	defer func() { _ = f.Close() }()
	var out []MatrixLine
	pinned := map[string]bool{}    // suite -> its lines say pinned
	comment := map[string]string{} // suite -> the comment above its first line
	var above []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 0 {
			above = nil
			continue
		}
		if strings.HasPrefix(fields[0], "#") {
			above = append(above, strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(sc.Text()), "#")))
			continue
		}
		if (len(fields) != 4 && len(fields) != 5) || (fields[3] != "heavy" && fields[3] != "-") || (len(fields) == 5 && fields[4] != "pinned") {
			return nil, nil, fmt.Errorf("%s: %q: want suite, Kubernetes minor, shard, host (heavy or -) and, for a suite that runs only on the minors it lists, pinned", path, sc.Text())
		}
		suite, pin := fields[0], len(fields) == 5
		if was, seen := pinned[suite]; seen && was != pin {
			return nil, nil, fmt.Errorf("%s: %q: every line of %s says pinned, or none", path, sc.Text(), suite)
		}
		if _, seen := pinned[suite]; !seen {
			comment[suite] = strings.Join(above, " ")
		}
		pinned[suite] = pin
		above = nil
		out = append(out, MatrixLine{suite, fields[1], fields[2]})
	}
	if err := sc.Err(); err != nil {
		return nil, nil, fmt.Errorf("read matrix: %w", err)
	}
	pins := map[string]string{}
	for suite, pin := range pinned {
		if !pin {
			continue
		}
		if comment[suite] == "" {
			return nil, nil, fmt.Errorf("%s: %s is pinned to its minors without a reason: say why in a comment right above its first line", path, suite)
		}
		if !strings.ContainsFunc(comment[suite], unicode.IsLetter) {
			return nil, nil, fmt.Errorf("%s: %s is pinned with the reason %q, which names no cause: say why in words", path, suite, comment[suite])
		}
		pins[suite] = comment[suite]
	}
	return out, pins, nil
}

func readEnv(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read tool versions: %w", err)
	}
	out := map[string]string{}
	for _, l := range strings.Split(string(data), "\n") {
		l = strings.TrimSpace(l)
		if k, v, ok := strings.Cut(l, "="); ok && !strings.HasPrefix(l, "#") {
			out[k] = v
		}
	}
	return out, nil
}

func findFuzzTargets(root string) ([]FuzzTarget, error) {
	var out []FuzzTarget
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor", "results", "bin":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, "_test.go") {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return fmt.Errorf("read %s: %w", p, err)
		}
		rel, err := filepath.Rel(root, filepath.Dir(p))
		if err != nil {
			return fmt.Errorf("rel %s: %w", p, err)
		}
		for _, m := range fuzzFunc.FindAllStringSubmatch(string(data), -1) {
			out = append(out, FuzzTarget{Name: m[1], Dir: "./" + filepath.ToSlash(rel)})
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("find fuzz targets: %w", err)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Dir+out[i].Name < out[j].Dir+out[j].Name })
	return out, nil
}

// previousRelease is the release users upgrade from to the one this commit
// becomes: the highest vX.Y.Z tag (no release candidates) below it, from
// every tag, not only the ones merged into HEAD. Patch releases are tagged
// on release/vX.Y, so they are not ancestors of main: v0.12.3, cut there,
// is what a v0.12 user upgrades from to v0.13.0.
//
// The newest final tag merged into HEAD that does not point at HEAD names
// the minor line (v0.12.0 on main, v0.12.2 on release/v0.12 before
// v0.12.3); the previous release is the highest final tag of that line
// that does not contain HEAD. A tag on HEAD, or after it, is this commit's
// own release or a later one, never the one before. A higher minor's tags
// (v0.13.0 seen from release/v0.12) are not of the line.
func previousRelease(root string) (string, error) {
	merged, err := gitTags(root, "--merged", "HEAD", "--no-contains", "HEAD")
	if err != nil {
		return "", err
	}
	if len(merged) == 0 {
		return "", nil
	}
	line := minorLine(merged[0])
	all, err := gitTags(root, "--no-contains", "HEAD")
	if err != nil {
		return "", err
	}
	for _, t := range all {
		if minorLine(t) == line {
			return t, nil
		}
	}
	return merged[0], nil
}

// gitTags lists the vX.Y.Z tags (no release candidates) that git tag with
// args lists, highest version first.
func gitTags(root string, args ...string) ([]string, error) {
	cmd := exec.Command("git", append(append([]string{"tag"}, args...), "--list", "v*", "--sort=-v:refname")...)
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git tag: %w", err)
	}
	var tags []string
	for _, t := range strings.Fields(string(out)) {
		if releaseTag.MatchString(t) {
			tags = append(tags, t)
		}
	}
	return tags, nil
}

// minorLine is the vX.Y of release tag t.
func minorLine(t string) string {
	return t[:strings.LastIndex(t, ".")]
}

// upgradeSuite is the live suite that upgrades from release tag:
// v0.11.0 -> upgrade-v0110, as hack/e2e/up.sh names them.
func upgradeSuite(tag string) string {
	return "upgrade-v" + strings.ReplaceAll(strings.TrimPrefix(tag, "v"), ".", "")
}

// nodeImage is the node image for suite on minor (KIND_NODE_1_<m>, then the
// suite's own), or "".
func (r *Repo) nodeImage(suite string, minor int) string {
	if v := r.ToolVersions[fmt.Sprintf("KIND_NODE_1_%d", minor)]; v != "" {
		return v
	}
	key := strings.ToUpper(strings.ReplaceAll(suite, "-", "_"))
	return r.ToolVersions[fmt.Sprintf("KIND_NODE_%s_1_%d", key, minor)]
}

// clusterWeight is how much of the cluster budget a suite's job takes:
// gitlab needs about 7 GB, multi-cluster boots two clusters, scale a 24 GB node.
var clusterWeight = map[string]int{"gitlab": 2, "multi-cluster": 2, "scale": 2}

func isUpgrade(suite string) bool { return suite == "upgrade" || strings.HasPrefix(suite, "upgrade-") }

// Plan is every step of the gate for the checkout.
func Plan(r *Repo, o Options) []Step {
	var steps []Step
	// Quick checks first, so a broken tree fails in minutes; then the
	// suites, then scale.
	steps = append(steps, chartSteps(r, o)...)
	steps = append(steps, staticSteps(o)...)
	steps = append(steps, crossCompileStep(r))
	steps = append(steps, unitSteps(r, o)...)
	steps = append(steps, e2eSteps(r, o)...)
	steps = append(steps, scaleSteps(r, o)...)
	return steps
}

// checkPins refuses a pin that would keep a suite out of the gate: every
// minor a pinned suite lists must be a KIND_NODE_1_<minor> of
// hack/tool-versions.env, and one of them the floor or the newest minor the
// gate runs, so the suite runs at least once. core, which the gate runs on
// both, cannot be pinned, and an upgrade suite cannot be pinned off the
// floor, the oldest Kubernetes an upgrade must work on.
func (r *Repo) checkPins() error {
	floor, newest := r.KubeMinors()
	known := map[string]bool{}
	for k := range r.ToolVersions {
		if m := kindNode.FindStringSubmatch(k); m != nil && m[1] == "" {
			known["1."+m[2]] = true
		}
	}
	gate := []string{fmt.Sprintf("1.%d", floor), fmt.Sprintf("1.%d", newest)}
	suites := make([]string, 0, len(r.Pinned))
	for s := range r.Pinned {
		suites = append(suites, s)
	}
	sort.Strings(suites)
	for _, suite := range suites {
		if suite == "core" {
			return fmt.Errorf("hack/e2e/matrix.txt: core cannot be pinned: the gate runs it on the floor (%s) and the newest (%s) minor", gate[0], gate[1])
		}
		var minors []string
		for _, l := range r.Matrix {
			if l.Suite == suite && !slices.Contains(minors, l.Minor) {
				minors = append(minors, l.Minor)
			}
		}
		for _, m := range minors {
			if !known[m] {
				return fmt.Errorf("hack/e2e/matrix.txt: %s is pinned to %s, which hack/tool-versions.env has no KIND_NODE_1_%s for",
					suite, m, strings.TrimPrefix(m, "1."))
			}
		}
		if !slices.Contains(minors, gate[0]) && !slices.Contains(minors, gate[1]) {
			return fmt.Errorf("hack/e2e/matrix.txt: %s is pinned to %s, none of the minors the gate runs (%s and %s): it would run nowhere",
				suite, strings.Join(minors, ", "), gate[0], gate[1])
		}
		if isUpgrade(suite) && !slices.Contains(minors, gate[0]) {
			return fmt.Errorf("hack/e2e/matrix.txt: %s is pinned off the floor minor %s: an upgrade must be proven on the oldest supported Kubernetes",
				suite, gate[0])
		}
	}
	return nil
}

// pinnedOff is why suite cannot run on minor, or "": matrix.txt pins it to
// the minors its lines list, and minor is not one of them.
func (r *Repo) pinnedOff(suite string, minor int) string {
	reason, ok := r.Pinned[suite]
	if !ok {
		return ""
	}
	var on []string
	for _, l := range r.Matrix {
		if l.Suite != suite {
			continue
		}
		if l.Minor == fmt.Sprintf("1.%d", minor) {
			return ""
		}
		if len(on) == 0 || on[len(on)-1] != l.Minor {
			on = append(on, l.Minor)
		}
	}
	return fmt.Sprintf("hack/e2e/matrix.txt runs %s on %s only: %s", suite, strings.Join(on, ", "), reason)
}

// suiteShards lists the live suites of matrix.txt but scale, in order, with
// the shards each runs in ("-" for the whole suite).
func suiteShards(r *Repo) ([]string, map[string][]string) {
	var suites []string
	shards := map[string][]string{}
	for _, l := range r.Matrix {
		if l.Suite == "scale" {
			continue
		}
		if _, ok := shards[l.Suite]; !ok {
			suites = append(suites, l.Suite)
		}
		known := false
		for _, s := range shards[l.Suite] {
			known = known || s == l.Shard
		}
		if !known {
			shards[l.Suite] = append(shards[l.Suite], l.Shard)
		}
	}
	return suites, shards
}

// ciSteps are the e2e-live.yml jobs the gate dispatches: every suite but
// scale on the floor and the newest minor (e2e-live.yml's minors input,
// hack/e2e/all.sh MINORS), with its matrix.txt shards. A pinned suite's
// other minor is a pinned step: all.sh MINORS runs no job for it.
func ciSteps(r *Repo, o Options) []Step {
	suites, shards := suiteShards(r)
	var steps []Step
	for _, suite := range suites {
		for _, m := range o.Minors() {
			for _, sh := range shards[suite] {
				l := MatrixLine{Suite: suite, Minor: fmt.Sprintf("1.%d", m), Shard: sh}
				j := &ciJob{ID: jobID(l), Suite: suite, Minor: l.Minor, Shard: sh}
				area := areaE2E
				if isUpgrade(suite) {
					area = areaUpgrade
				}
				st := Step{ID: "ci-" + j.ID, Area: area, Title: j.Name() + " (CI)", Tests: "e2e/test.json", MinTests: 1, CIJob: j}
				if st.Pinned = r.pinnedOff(suite, m); st.Pinned != "" {
					st.CIJob, st.Tests, st.MinTests = nil, "", 0
				}
				steps = append(steps, st)
			}
		}
	}
	return steps
}

func e2eSteps(r *Repo, o Options) []Step {
	if o.LiveCI {
		return append(ciSteps(r, o), prevUpgrade(r)...)
	}
	suites, _ := suiteShards(r)
	var steps []Step
	for _, s := range suites {
		for _, m := range o.Minors() {
			steps = append(steps, e2eStep(r, o, s, m))
		}
	}
	return append(steps, prevUpgrade(r)...)
}

// prevUpgrade is a missing step when no suite upgrades from the previous release.
func prevUpgrade(r *Repo) []Step {
	if r.PrevRelease == "" {
		return nil
	}
	want := upgradeSuite(r.PrevRelease)
	for _, l := range r.Matrix {
		if l.Suite == want {
			return nil
		}
	}
	return []Step{{
		ID: want, Area: areaUpgrade, Title: "upgrade from " + r.PrevRelease,
		Missing: fmt.Sprintf("no %s suite in hack/e2e/matrix.txt: nothing tests the upgrade from %s", want, r.PrevRelease),
	}}
}

func e2eStep(r *Repo, o Options, suite string, minor int) Step {
	id := fmt.Sprintf("e2e-%s-1%d", suite, minor)
	area := areaE2E
	count := o.Count
	if isUpgrade(suite) {
		area, count = areaUpgrade, 1 // run.sh: the upgrade test upgrades its cluster, once
	}
	st := Step{
		ID: id, Area: area, Title: fmt.Sprintf("%s on Kubernetes 1.%d", suite, minor),
		Clusters: max(1, clusterWeight[suite]), Tests: "e2e/test.json", MinTests: 1,
	}
	if st.Pinned = r.pinnedOff(suite, minor); st.Pinned != "" {
		st.Clusters, st.Tests, st.MinTests = 0, "", 0
		return st
	}
	if suite == "github" && !o.GitHubToken {
		st.NotRun = "no KARDINAL_E2E_GITHUB_TOKEN_FILE or DEMO_GITHUB_TOKEN (the gate never uses your gh login)"
		return st
	}
	if r.nodeImage(suite, minor) == "" {
		st.Missing = fmt.Sprintf("no node image for Kubernetes 1.%d in hack/tool-versions.env", minor)
		return st
	}
	cluster := "kp-rg-" + strings.TrimPrefix(id, "e2e-")
	st.Script = liveScript(suite, cluster, fmt.Sprintf("1.%d", minor), count, nil)
	st.Cleanup = deleteClusters(cluster)
	return st
}

// liveScript sets up suite on its own kind cluster and runs it, as
// hack/e2e/all.sh's jobs do. Only the github suite sees a GitHub token.
func liveScript(suite, cluster, minor string, count int, env []string) string {
	var b strings.Builder
	if suite != "github" {
		b.WriteString("unset KARDINAL_E2E_GITHUB_TOKEN_FILE DEMO_GITHUB_TOKEN\n")
	}
	fmt.Fprintf(&b, "export KIND_CLUSTER=%s KIND_K8S=%s COUNT=%d\n", cluster, minor, count)
	b.WriteString("export E2E_OUT=\"$STEP_DIR/e2e\" KUBECONFIG=\"$STEP_DIR/e2e/kubeconfig\"\n")
	b.WriteString("export KARDINAL_E2E_BUILD=${KARDINAL_E2E_BUILD:-host}\n")
	for _, e := range env {
		fmt.Fprintf(&b, "export %s\n", e)
	}
	b.WriteString("unset RUN SHARD\nmkdir -p \"$E2E_OUT\"\n")
	fmt.Fprintf(&b, "bash hack/e2e/up.sh %s\n", suite)
	// The images this suite runs, as up.sh built and loaded them.
	b.WriteString(`for i in "${KARDINAL_E2E_IMAGE:-ghcr.io/pnz1990/kardinal-promoter/controller:e2e-$KIND_CLUSTER}" \
  "${KARDINAL_E2E_RENDER_IMAGE:-ghcr.io/pnz1990/kardinal-promoter/render:e2e-$KIND_CLUSTER}"; do
  echo "$i $(docker image inspect -f '{{.Id}}' "$i") (e2e $KIND_CLUSTER)"
done > "$STEP_DIR/images.txt"
`)
	fmt.Fprintf(&b, "bash hack/e2e/run.sh %s\n", suite)
	return b.String()
}

// deleteClusters deletes the kind cluster and a multi-cluster spoke, if they exist.
func deleteClusters(cluster string) string {
	return fmt.Sprintf(`PATH="$PWD/bin/e2e:$PATH"
for c in %[1]s %[1]s-spoke; do
  if kind get clusters 2>/dev/null | grep -x "$c" >/dev/null; then
    kind delete cluster --name "$c" --kubeconfig "$STEP_DIR/cleanup.kubeconfig"
  fi
done
`, cluster)
}

func scaleSteps(r *Repo, o Options) []Step {
	var steps []Step
	for _, p := range o.ScaleProfiles {
		st := Step{
			ID: "scale-" + p, Area: areaScale, Title: "scale suite, " + p + " profile",
			Clusters: clusterWeight["scale"], Heavy: true, Exclusive: true, Tests: "e2e/test.json", MinTests: 1,
		}
		if r.nodeImage("scale", o.Newest) == "" {
			st.Missing = fmt.Sprintf("no node image for Kubernetes 1.%d", o.Newest)
			steps = append(steps, st)
			continue
		}
		minor := fmt.Sprintf("1.%d", o.Newest)
		env := []string{"KARDINAL_E2E_NODE_MEMORY=24g", "KARDINAL_E2E_KRO_MEMORY=8Gi", "KARDINAL_E2E_SCALE_PROFILE=" + p}
		switch p {
		case "full":
			env = append(env, "KARDINAL_E2E_TIMEOUT=4h")
		case "soak":
			// test/e2e/README.md#scale-suite: the soak profile is the sustained load.
			env = append(env, "KARDINAL_E2E_TIMEOUT=3h")
		}
		cluster := "kp-rg-scale-" + p
		script := liveScript("scale", cluster, minor, 1, env)
		if p == "soak" {
			script = strings.Replace(script, "unset RUN SHARD\n", "unset SHARD\nexport RUN='^TestScale_LoadSustained$'\n", 1)
		}
		st.Script, st.Cleanup = script, deleteClusters(cluster)
		steps = append(steps, st)
		if p == "soak" {
			steps = append(steps, receiptBurstStep(r))
		}
	}
	return steps
}

// Receipt burst at full size (#2062): the soak set's one run of
// TestReceiptsKeyless_Burst with the plan's 500 receipts against a Fulcio
// rate-limited to 10 requests a second. The nightly receipts-keyless suite
// runs it with 100 receipts at 1 a second; it is never a per-PR run.
const (
	burstFullReceipts = 500
	burstFullRPS      = 10
)

// receiptBurstStep runs the receipts-keyless suite's burst test at full
// size, on the suite's minor in matrix.txt.
func receiptBurstStep(r *Repo) Step {
	st := Step{
		ID: "receipts-burst-full", Area: areaScale,
		Title:    fmt.Sprintf("receipt burst, %d receipts against Fulcio at %d rps", burstFullReceipts, burstFullRPS),
		Clusters: 1, Heavy: true, Tests: "e2e/test.json", MinTests: 1,
	}
	minor := ""
	for _, l := range r.Matrix {
		if l.Suite == "receipts-keyless" {
			minor = l.Minor
			break
		}
	}
	if minor == "" {
		st.Missing = "no receipts-keyless suite in hack/e2e/matrix.txt"
		return st
	}
	cluster := "kp-rg-receipts-burst"
	env := []string{fmt.Sprintf("KARDINAL_E2E_RECEIPT_BURST=%d", burstFullReceipts),
		fmt.Sprintf("KARDINAL_E2E_RECEIPT_BURST_RPS=%d", burstFullRPS), "KARDINAL_E2E_TIMEOUT=2h"}
	script := liveScript("receipts-keyless", cluster, minor, 1, env)
	st.Script = strings.Replace(script, "unset RUN SHARD\n", "unset SHARD\nexport RUN='^TestReceiptsKeyless_Burst$'\n", 1)
	st.Cleanup = deleteClusters(cluster)
	return st
}

func unitSteps(r *Repo, o Options) []Step {
	steps := []Step{{
		ID: "unit-race", Area: areaUnit, Title: "go test -race -count=3 ./...", Heavy: true, Tests: "tests.json",
		// A whole -count=3 run records over 30,000; a few tests skip where a
		// tool is missing (each is listed).
		MinTests: 20000, MaxSkips: 10,
		// -p 8: at most 8 test binaries at once, so the host keeps room for
		// others while the gate holds the heavy-load lock. The pinned
		// promtool, and KARDINAL_REQUIRE_PROMTOOL, so test/helm's PromQL
		// tests fail rather than skip without it (#2037).
		Script: "bash hack/ci/install-promtool.sh\n" +
			`KARDINAL_REQUIRE_PROMTOOL=1 go test -race -count=3 -p 8 -timeout 60m -json ./... > "$STEP_DIR/tests.json"`,
	}}
	for _, f := range r.FuzzTargets {
		steps = append(steps, Step{
			ID: "fuzz-" + f.Name, Area: areaUnit, Title: fmt.Sprintf("%s (%s) for %s", f.Name, f.Dir, o.FuzzTime),
			Tests: "tests.json", MinTests: 1, Heavy: fuzzHeavy(o.FuzzTime),
			// hack/ci/fuzz.sh: go test -fuzz, and Go's fuzzing deadline
			// race (#2078) counted as the run with no finding it is.
			Script: fmt.Sprintf(`bash hack/ci/fuzz.sh %s %s %s "$STEP_DIR/tests.json"`, f.Dir, f.Name, o.FuzzTime),
		})
	}
	steps = append(steps, Step{
		ID: "scenario-matrix", Area: areaUnit, Title: "scenario matrix (test/scenario)", Tests: "tests.json",
		MinTests: 40, // 48 pairwise combinations
		// The matrix runs in packages of its own since #1960; before, in test/scenario.
		Script: `pkgs=./test/scenario/; if [ -d test/scenario/matrix ]; then pkgs=./test/scenario/matrix/...; fi; ` +
			`go test -race -count=1 -timeout 30m -json -run '^TestScenarioMatrix$' $pkgs > "$STEP_DIR/tests.json"`,
	})
	ent := Step{ID: "scenario-enterprise", Area: areaUnit, Title: "enterprise scenario at full size", Heavy: true, Tests: "tests.json",
		MinTests: 2,
		// KARDINAL_SCENARIO_SCALE=full: the agent scale scenario at its full
		// 5,000 AgentIdentities, which PR CI runs at 2,000.
		// KARDINAL_SCENARIO_CHAOS=full: the controller restarts with 32
		// seeds, which PR CI runs 16 of.
		Script: `KARDINAL_SCENARIO_ENTERPRISE=full KARDINAL_SCENARIO_SCALE=full KARDINAL_SCENARIO_CHAOS=full go test -count=1 -timeout 30m -json -run '^TestEnterprise_' ./test/scenario/enterprise/... > "$STEP_DIR/tests.json"`}
	if !r.Enterprise {
		ent.Script = ""
		ent.Missing = "no test/scenario/enterprise on this commit (#1860)"
	}
	return append(steps, ent)
}

func staticSteps(o Options) []Step {
	images := `PATH="$PWD/bin/e2e:$PATH"
source hack/tool-versions.env
if ! command -v trivy >/dev/null; then
  mkdir -p bin/e2e
  tgz="$STEP_DIR/trivy.tar.gz"
  curl -fsSL -o "$tgz" "https://github.com/aquasecurity/trivy/releases/download/${TRIVY_VERSION}/trivy_${TRIVY_VERSION#v}_Linux-64bit.tar.gz"
  echo "${TRIVY_SHA256}  $tgz" | sha256sum -c -
  tar -xzf "$tgz" -C bin/e2e trivy
fi
sha=$(git rev-parse --short=12 HEAD)
controller="kardinal-release-gate/controller:$sha" render="kardinal-release-gate/render:$sha"
case "$IMAGE_BUILD" in
  docker)
    docker build -q -t "$controller" .
    docker build -q -t "$render" -f render.Dockerfile .
    ;;
  host)
    ctx="$STEP_DIR/ctx"
    mkdir -p "$ctx/controller" "$ctx/render"
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o "$ctx/controller/kardinal-controller" ./cmd/kardinal-controller
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o "$ctx/render/kardinal-render" ./cmd/kardinal-render
    chmod 0755 "$ctx/controller/kardinal-controller" "$ctx/render/kardinal-render"
    docker build -q -t "$controller" -f hack/e2e/controller.Dockerfile "$ctx/controller"
    docker build -q -t "$render" -f hack/e2e/render.Dockerfile "$ctx/render"
    ;;
  *) echo "image build $IMAGE_BUILD: want docker or host" >&2; exit 1 ;;
esac
: > "$STEP_DIR/images.txt"
for img in "$controller" "$render"; do
  echo "$img $(docker image inspect -f '{{.Id}}' "$img") ($IMAGE_BUILD build)" >> "$STEP_DIR/images.txt"
  trivy image --quiet --severity HIGH,CRITICAL --ignore-unfixed --exit-code 1 "$img"
done
`
	return []Step{
		{ID: "govulncheck", Area: areaStatic, Title: "govulncheck (make vuln)", Script: "make vuln"},
		// Other checkouts on the host may lint at the same time: golangci-lint
		// refuses to by default ("parallel golangci-lint is running").
		{ID: "golangci-lint", Area: areaStatic, Title: "golangci-lint", Script: `make "$PWD/bin/golangci-lint"
bin/golangci-lint run --allow-parallel-runners ./...`},
		{ID: "hygiene", Area: areaStatic, Title: "make hygiene", Script: "(cd web && npm ci --no-audit --no-fund)\nmake hygiene"},
		{ID: "images", Area: areaStatic, Title: "trivy on the controller and render images (" + o.ImageBuild + " build)",
			Script: "IMAGE_BUILD=" + o.ImageBuild + "\n" + images},
		{ID: "docs", Area: areaStatic, Title: "mkdocs build --strict and check-site-offline", Script: `python3 -m venv "$STEP_DIR/venv"
"$STEP_DIR/venv/bin/pip" install --quiet -r docs/requirements.txt
"$STEP_DIR/venv/bin/mkdocs" build --strict --site-dir "$STEP_DIR/site"
go run ./hack/check-site-offline "$STEP_DIR/site"
`},
		// A copy of web/ (and the scripts/ its tests import), so the build
		// does not rewrite the web/dist the e2e steps embed.
		{ID: "bundle-size", Area: areaStatic, Title: "web build and bundle size budget (npm run size)", Script: `mkdir -p "$STEP_DIR/tree"
tar -C . --exclude=web/node_modules --exclude=web/dist -cf - web scripts | tar -C "$STEP_DIR/tree" -xf -
cd "$STEP_DIR/tree/web"
npm ci --no-audit --no-fund
npm run build
npm run size
`},
	}
}

// profiles are the chart values profiles the gate renders, in
// hack/release-gate/profiles (default is the chart's values.yaml alone).
var profiles = []string{"default", "ha", "observability", "secure", "gitlab"}

func chartSteps(r *Repo, o Options) []Step {
	var lint strings.Builder
	lint.WriteString("PATH=\"$PWD/bin/e2e:$PATH\"\nbash hack/e2e/tools.sh\n")
	for _, p := range profiles {
		values := ""
		if p != "default" {
			values = " -f hack/release-gate/profiles/" + p + ".yaml"
		}
		fmt.Fprintf(&lint, "echo '== %s'\nhelm lint chart/kardinal-promoter%s\nhelm template kardinal-promoter chart/kardinal-promoter -n kardinal-system%s > \"$STEP_DIR/%s.yaml\"\n",
			p, values, values, p)
	}
	return []Step{
		{ID: "chart-profiles", Area: areaChart, Title: "helm lint and template, every values profile", Script: lint.String()},
		envtestStep(r, o),
	}
}

// envtestStep is make envtest (#1849) with go test -json: every CRD and the
// chart's admission policies installed on API servers of the floor and the
// newest minor (ENVTEST_K8S_MIN and _MAX of hack/tool-versions.env, which
// must be those minors).
func envtestStep(r *Repo, o Options) Step {
	st := Step{ID: "chart-envtest", Area: areaChart, Tests: "tests.json", MinTests: 2,
		Title: fmt.Sprintf("CRDs and admission policies on Kubernetes 1.%d and 1.%d (make envtest)", o.Floor, o.Newest)}
	lo, hi := r.ToolVersions["ENVTEST_K8S_MIN"], r.ToolVersions["ENVTEST_K8S_MAX"]
	want := func(v string, m int) bool { return strings.HasPrefix(v, fmt.Sprintf("1.%d.", m)) }
	if !want(lo, o.Floor) || !want(hi, o.Newest) {
		st.Missing = fmt.Sprintf("envtest runs %s and %s (hack/tool-versions.env ENVTEST_K8S_MIN, _MAX), not 1.%d and 1.%d", lo, hi, o.Floor, o.Newest)
		return st
	}
	st.Script = `source hack/tool-versions.env
GOBIN="$PWD/bin" go install "sigs.k8s.io/controller-runtime/tools/setup-envtest@$SETUP_ENVTEST_VERSION"
: > "$STEP_DIR/tests.json"
for v in "$ENVTEST_K8S_MIN" "$ENVTEST_K8S_MAX"; do
  assets=$(bin/setup-envtest use "$v" --bin-dir "$PWD/bin/envtest" -p path)
  echo "== Kubernetes $v"
  KUBEBUILDER_ASSETS=$assets go test -tags envtest ./test/helm/ -run TestInstallOnAPIServer -count=1 -json >> "$STEP_DIR/tests.json"
done
`
	return st
}

// BuildTarget is a binary a release ships: a package of this module built
// for a platform, with its build tags.
type BuildTarget struct{ GOOS, GOARCH, Pkg, Tags string }

var (
	// A release.yml line that builds a release asset:
	// GOOS=darwin GOARCH=arm64 go build -ldflags=... -o dist/x ./cmd/kardinal/
	distBuild = regexp.MustCompile(`(?m)\bGOOS=(\w+)\s+GOARCH=(\w+)\s+go build\b([^\n]*?)\s(\./cmd/[\w-]+)/?[ \t]*$`)
	// An image's platforms (docker/build-push-action), and a Dockerfile other
	// than the root one it builds.
	imagePlatforms = regexp.MustCompile(`(?m)^\s+platforms:\s*(\S+)\s*$`)
	imageFile      = regexp.MustCompile(`(?m)^\s+file:\s*(\S*Dockerfile)\s*$`)
	// A Dockerfile's go build of a package, and its -tags.
	dockerBuild = regexp.MustCompile(`\bgo build\b.*?\s(\./cmd/[\w-]+)/?(?:\s|$)`)
	buildTags   = regexp.MustCompile(`\s-tags[= ](\S+)`)
)

// releaseTargets are what .github/workflows/release.yml ships: each
// GOOS/GOARCH go build of a release asset, and the go build of each image's
// Dockerfile on each of the images' platforms. Sorted, once each.
func releaseTargets(root string) ([]BuildTarget, error) {
	wf, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "release.yml"))
	if err != nil {
		return nil, fmt.Errorf("read release.yml: %w", err)
	}
	seen := map[BuildTarget]bool{}
	add := func(t BuildTarget) { seen[t] = true }
	for _, m := range distBuild.FindAllStringSubmatch(string(wf), -1) {
		t := BuildTarget{GOOS: m[1], GOARCH: m[2], Pkg: m[4]}
		if tm := buildTags.FindStringSubmatch(m[3]); tm != nil {
			t.Tags = strings.Trim(tm[1], `"'`)
		}
		add(t)
	}
	var platforms []string
	for _, m := range imagePlatforms.FindAllStringSubmatch(string(wf), -1) {
		platforms = append(platforms, strings.Split(m[1], ",")...)
	}
	if len(platforms) > 0 {
		files := []string{"Dockerfile"} // the build context's default
		for _, m := range imageFile.FindAllStringSubmatch(string(wf), -1) {
			files = append(files, m[1])
		}
		for _, file := range files {
			data, err := os.ReadFile(filepath.Join(root, file))
			if err != nil {
				return nil, fmt.Errorf("read %s, an image release.yml builds: %w", file, err)
			}
			// One line per command: RUN lines continue with a backslash.
			text := strings.ReplaceAll(string(data), "\\\n", " ")
			for _, l := range strings.Split(text, "\n") {
				m := dockerBuild.FindStringSubmatch(l)
				if m == nil {
					continue
				}
				tags := ""
				if tm := buildTags.FindStringSubmatch(l); tm != nil {
					tags = strings.Trim(tm[1], `"'`)
				}
				for _, p := range platforms {
					goos, goarch, ok := strings.Cut(strings.TrimSpace(p), "/")
					if !ok {
						return nil, fmt.Errorf("release.yml: image platform %q: want os/arch", p)
					}
					add(BuildTarget{GOOS: goos, GOARCH: goarch, Pkg: m[1], Tags: tags})
				}
			}
		}
	}
	out := make([]BuildTarget, 0, len(seen))
	for t := range seen {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		return a.Pkg+" "+a.GOOS+"/"+a.GOARCH+" "+a.Tags < b.Pkg+" "+b.GOOS+"/"+b.GOARCH+" "+b.Tags
	})
	return out, nil
}

// crossCompileStep builds every binary release.yml ships, for its platform
// (QA-2's v0.13 gate audit, #2304): a compile break on darwin, windows or
// arm64 alone fails here, not at tag time. It builds only; go vet per
// platform would vet the test files too, and some use unix-only syscalls.
func crossCompileStep(r *Repo) Step {
	st := Step{ID: "cross-compile", Area: areaStatic,
		Title: fmt.Sprintf("go build of the %d binaries release.yml ships, each for its platform", len(r.ReleaseTargets))}
	if len(r.ReleaseTargets) == 0 {
		st.Missing = "found no GOOS/GOARCH go build or multi-platform image in .github/workflows/release.yml"
		return st
	}
	var b strings.Builder
	b.WriteString("export CGO_ENABLED=0\n")
	for _, t := range r.ReleaseTargets {
		tags := ""
		if t.Tags != "" {
			tags = " -tags " + t.Tags
		}
		fmt.Fprintf(&b, "echo '== %s/%s %s%s'\nGOOS=%s GOARCH=%s go build%s -o /dev/null %s\n", t.GOOS, t.GOARCH, t.Pkg, tags, t.GOOS, t.GOARCH, tags, t.Pkg)
	}
	st.Script = b.String()
	return st
}
