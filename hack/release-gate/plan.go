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
	"sort"
	"strconv"
	"strings"
	"time"
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
	// Tests is the go test -json file the step writes, relative to its
	// directory, for the totals.
	Tests string
	// NotRun, when set, is why the step cannot run here: it is reported,
	// never run, and the gate is incomplete.
	NotRun string
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
	Root         string
	Matrix       []MatrixLine      // hack/e2e/matrix.txt
	ToolVersions map[string]string // hack/tool-versions.env
	FuzzTargets  []FuzzTarget
	PrevRelease  string // the release before this commit, e.g. v0.11.0
	Enterprise   bool   // test/scenario/enterprise exists
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

// LoadRepo reads the matrix, the tool versions, the fuzz targets and the
// previous release of the checkout at root.
func LoadRepo(root string) (*Repo, error) {
	r := &Repo{Root: root}
	var err error
	if r.Matrix, err = readMatrix(filepath.Join(root, "hack", "e2e", "matrix.txt")); err != nil {
		return nil, err
	}
	if r.ToolVersions, err = readEnv(filepath.Join(root, "hack", "tool-versions.env")); err != nil {
		return nil, err
	}
	if r.FuzzTargets, err = findFuzzTargets(root); err != nil {
		return nil, err
	}
	if r.PrevRelease, err = previousRelease(root); err != nil {
		return nil, err
	}
	if st, err := os.Stat(filepath.Join(root, "test", "scenario", "enterprise")); err == nil && st.IsDir() {
		r.Enterprise = true
	}
	return r, nil
}

func readMatrix(path string) ([]MatrixLine, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read matrix: %w", err)
	}
	defer func() { _ = f.Close() }()
	var out []MatrixLine
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 0 || strings.HasPrefix(fields[0], "#") {
			continue
		}
		if len(fields) != 3 {
			return nil, fmt.Errorf("%s: %q: want suite, Kubernetes minor and shard", path, sc.Text())
		}
		out = append(out, MatrixLine{fields[0], fields[1], fields[2]})
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read matrix: %w", err)
	}
	return out, nil
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

// previousRelease is the newest vX.Y.Z tag (no release candidates) merged
// into HEAD that does not point at HEAD: for a release candidate the
// release before it.
func previousRelease(root string) (string, error) {
	cmd := exec.Command("git", "tag", "--merged", "HEAD", "--no-contains", "HEAD", "--list", "v*", "--sort=-v:refname")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git tag: %w", err)
	}
	for _, t := range strings.Fields(string(out)) {
		if releaseTag.MatchString(t) {
			return t, nil
		}
	}
	return "", nil
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
	steps = append(steps, unitSteps(r, o)...)
	steps = append(steps, e2eSteps(r, o)...)
	steps = append(steps, scaleSteps(r, o)...)
	return steps
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
// hack/e2e/all.sh MINORS), with its matrix.txt shards.
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
				steps = append(steps, Step{ID: "ci-" + j.ID, Area: area, Title: j.Name() + " (CI)",
					Tests: "e2e/test.json", MinTests: 1, CIJob: j})
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
			Clusters: clusterWeight["scale"], Heavy: true, Tests: "e2e/test.json", MinTests: 1,
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
	}
	return steps
}

func unitSteps(r *Repo, o Options) []Step {
	steps := []Step{{
		ID: "unit-race", Area: areaUnit, Title: "go test -race -count=3 ./...", Heavy: true, Tests: "tests.json",
		// A whole -count=3 run records over 30,000; a few tests skip where a
		// tool is missing (each is listed).
		MinTests: 20000, MaxSkips: 10,
		// -p 8: at most 8 test binaries at once, so the host keeps room for
		// others while the gate holds the heavy-load lock.
		Script: `go test -race -count=3 -p 8 -timeout 60m -json ./... > "$STEP_DIR/tests.json"`,
	}}
	for _, f := range r.FuzzTargets {
		steps = append(steps, Step{
			ID: "fuzz-" + f.Name, Area: areaUnit, Title: fmt.Sprintf("%s (%s) for %s", f.Name, f.Dir, o.FuzzTime),
			Tests: "tests.json", MinTests: 1, Heavy: fuzzHeavy(o.FuzzTime),
			Script: fmt.Sprintf(`go test -run '^$' -fuzz '^%s$' -fuzztime %s -parallel 4 -json %s > "$STEP_DIR/tests.json"`,
				f.Name, o.FuzzTime, f.Dir),
		})
	}
	steps = append(steps, Step{
		ID: "scenario-matrix", Area: areaUnit, Title: "scenario matrix (test/scenario)", Tests: "tests.json",
		MinTests: 40, // 48 pairwise combinations
		Script:   `go test -race -count=1 -timeout 30m -json -run '^TestScenarioMatrix$' ./test/scenario/ > "$STEP_DIR/tests.json"`,
	})
	ent := Step{ID: "scenario-enterprise", Area: areaUnit, Title: "enterprise scenario at full size", Heavy: true, Tests: "tests.json",
		MinTests: 2,
		Script:   `KARDINAL_SCENARIO_ENTERPRISE=full go test -count=1 -timeout 30m -json -run '^TestEnterprise_' ./test/scenario/enterprise/ > "$STEP_DIR/tests.json"`}
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
