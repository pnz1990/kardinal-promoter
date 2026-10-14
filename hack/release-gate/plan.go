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
}

// Options are the plan's knobs (flags).
type Options struct {
	Count         int      // live e2e go test -count
	FuzzTime      string   // go test -fuzztime per fuzz target
	ScaleProfiles []string // scale suite profiles: ci, full, soak
	ImageBuild    string   // docker (the repo Dockerfiles) or host (host-built binaries)
	GitHubToken   bool     // a GitHub token for the github suite is set
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

// minors returns the Kubernetes minors a suite can boot: KIND_NODE_1_<m>
// for every suite, KIND_NODE_<SUITE>_1_<m> for that suite.
func (r *Repo) minors(suite string) []int {
	key := strings.ToUpper(strings.ReplaceAll(suite, "-", "_"))
	set := map[int]bool{}
	for k := range r.ToolVersions {
		m := kindNode.FindStringSubmatch(k)
		if m == nil || (m[1] != "" && m[1] != key) {
			continue
		}
		n, _ := strconv.Atoi(m[2])
		set[n] = true
	}
	var out []int
	for n := range set {
		out = append(out, n)
	}
	sort.Ints(out)
	return out
}

// allMinors is every minor any suite boots: the chart's floor and the newest.
func (r *Repo) allMinors() []int {
	set := map[int]bool{}
	for k := range r.ToolVersions {
		if m := kindNode.FindStringSubmatch(k); m != nil {
			n, _ := strconv.Atoi(m[2])
			set[n] = true
		}
	}
	var out []int
	for n := range set {
		out = append(out, n)
	}
	sort.Ints(out)
	return out
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

// fixedMinor lists the suites whose matrix.txt minor is the only one they
// run on: the multi-cluster spoke boots test/e2e/kind-config.yaml's node
// image, and the hub must run the same minor.
var fixedMinor = map[string]bool{"multi-cluster": true}

// clusterWeight is how much of the cluster budget a suite's job takes:
// gitlab needs about 7 GB, multi-cluster boots two clusters, scale a 24 GB node.
var clusterWeight = map[string]int{"gitlab": 2, "multi-cluster": 2, "scale": 2}

func isUpgrade(suite string) bool { return suite == "upgrade" || strings.HasPrefix(suite, "upgrade-") }

// Plan is every step of the gate for the checkout.
func Plan(r *Repo, o Options) []Step {
	var steps []Step
	// Quick checks first, so a broken tree fails in minutes; the cluster
	// steps start with the CRD checks, then the suites, then scale.
	steps = append(steps, chartSteps(r)...)
	steps = append(steps, staticSteps(o)...)
	steps = append(steps, unitSteps(r, o)...)
	steps = append(steps, e2eSteps(r, o)...)
	steps = append(steps, scaleSteps(r, o)...)
	return steps
}

func e2eSteps(r *Repo, o Options) []Step {
	var suites []string
	matrixMinors := map[string][]int{}
	for _, l := range r.Matrix {
		if l.Suite == "scale" {
			continue
		}
		if _, ok := matrixMinors[l.Suite]; !ok {
			suites = append(suites, l.Suite)
		}
		n, _ := strconv.Atoi(strings.TrimPrefix(l.Minor, "1."))
		matrixMinors[l.Suite] = append(matrixMinors[l.Suite], n)
	}
	var steps []Step
	for _, s := range suites {
		minors := r.minors(s)
		if fixedMinor[s] || len(minors) == 0 {
			minors = matrixMinors[s]
		}
		sort.Ints(minors)
		pick := []int{minors[0]}
		if last := minors[len(minors)-1]; last != minors[0] {
			pick = append(pick, last)
		}
		for _, m := range pick {
			steps = append(steps, e2eStep(r, o, s, m))
		}
	}
	if r.PrevRelease != "" {
		want := upgradeSuite(r.PrevRelease)
		if _, ok := matrixMinors[want]; !ok {
			steps = append(steps, Step{
				ID: want, Area: areaUpgrade, Title: "upgrade from " + r.PrevRelease,
				Missing: fmt.Sprintf("no %s suite in hack/e2e/matrix.txt: nothing tests the upgrade from %s", want, r.PrevRelease),
			})
		}
	}
	return steps
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
		Clusters: max(1, clusterWeight[suite]), Tests: "e2e/test.json",
	}
	if suite == "github" && !o.GitHubToken {
		st.NotRun = "no KARDINAL_E2E_GITHUB_TOKEN_FILE or DEMO_GITHUB_TOKEN (the gate never uses your gh login)"
		return st
	}
	if r.nodeImage(suite, minor) == "" {
		st.NotRun = fmt.Sprintf("no node image for 1.%d in hack/tool-versions.env", minor)
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
	fmt.Fprintf(&b, "bash hack/e2e/up.sh %s\nbash hack/e2e/run.sh %s\n", suite, suite)
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
	minors := r.minors("scale")
	var steps []Step
	for _, p := range o.ScaleProfiles {
		st := Step{
			ID: "scale-" + p, Area: areaScale, Title: "scale suite, " + p + " profile",
			Clusters: clusterWeight["scale"], Heavy: true, Tests: "e2e/test.json",
		}
		if len(minors) == 0 {
			st.NotRun = "no node image for the scale suite"
			steps = append(steps, st)
			continue
		}
		minor := fmt.Sprintf("1.%d", minors[len(minors)-1])
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
		Script: `go test -race -count=3 -timeout 60m -json ./... > "$STEP_DIR/tests.json"`,
	}}
	for _, f := range r.FuzzTargets {
		steps = append(steps, Step{
			ID: "fuzz-" + f.Name, Area: areaUnit, Title: fmt.Sprintf("%s (%s) for %s", f.Name, f.Dir, o.FuzzTime),
			Tests: "tests.json",
			Script: fmt.Sprintf(`go test -run '^$' -fuzz '^%s$' -fuzztime %s -parallel 4 -json %s > "$STEP_DIR/tests.json"`,
				f.Name, o.FuzzTime, f.Dir),
		})
	}
	steps = append(steps, Step{
		ID: "scenario-matrix", Area: areaUnit, Title: "scenario matrix (test/scenario)", Tests: "tests.json",
		Script: `go test -race -count=1 -timeout 30m -json -run '^TestScenarioMatrix$' ./test/scenario/ > "$STEP_DIR/tests.json"`,
	})
	ent := Step{ID: "scenario-enterprise", Area: areaUnit, Title: "enterprise scenario at full size", Heavy: true, Tests: "tests.json",
		Script: `KARDINAL_SCENARIO_ENTERPRISE=full go test -count=1 -timeout 30m -json -run '^TestEnterprise_' ./test/scenario/enterprise/ > "$STEP_DIR/tests.json"`}
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
		{ID: "golangci-lint", Area: areaStatic, Title: "golangci-lint (make lint)", Script: "make lint"},
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

// admittedProfiles are the profiles the CRD steps apply with a server-side
// dry run: observability renders ServiceMonitor and PrometheusRule objects,
// whose CRDs (the Prometheus operator's) a bare cluster does not serve.
var admittedProfiles = []string{"default", "ha", "secure", "gitlab"}

func chartSteps(r *Repo) []Step {
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
	steps := []Step{{ID: "chart-profiles", Area: areaChart, Title: "helm lint and template, every values profile", Script: lint.String()}}
	minors := r.allMinors()
	if len(minors) == 0 {
		return steps
	}
	pick := []int{minors[0]}
	if last := minors[len(minors)-1]; last != minors[0] {
		pick = append(pick, last)
	}
	for _, m := range pick {
		imageVar := ""
		for k := range r.ToolVersions {
			if mm := kindNode.FindStringSubmatch(k); mm != nil && mm[2] == strconv.Itoa(m) && (imageVar == "" || k < imageVar) {
				imageVar = k
			}
		}
		cluster := fmt.Sprintf("kp-rg-crd-1%d", m)
		steps = append(steps, Step{
			ID: fmt.Sprintf("chart-crds-1%d", m), Area: areaChart, Clusters: 1,
			Title: fmt.Sprintf("CRDs and chart objects admitted by Kubernetes 1.%d (server-side)", m),
			Script: fmt.Sprintf(`export KIND_CLUSTER=%s E2E_OUT="$STEP_DIR/e2e" KUBECONFIG="$STEP_DIR/e2e/kubeconfig"
mkdir -p "$E2E_OUT"
bash hack/e2e/tools.sh
source hack/e2e/lib.sh
NODE_IMAGE=$(source hack/tool-versions.env && echo "$%s")
kind_cluster "$KIND_CLUSTER"
target_cluster
"${KUBECTL[@]}" apply --server-side -f chart/kardinal-promoter/crds/
"${KUBECTL[@]}" wait --for condition=Established crd --all --timeout 2m
"${KUBECTL[@]}" create namespace kardinal-system
for p in %s; do
  values=
  [ "$p" = default ] || values="-f hack/release-gate/profiles/$p.yaml"
  # shellcheck disable=SC2086
  "${HELM[@]}" template kardinal-promoter chart/kardinal-promoter -n kardinal-system $values > "$STEP_DIR/$p.yaml"
  "${KUBECTL[@]}" apply --dry-run=server -f "$STEP_DIR/$p.yaml" > /dev/null
  echo "$p: admitted"
done
`, cluster, imageVar, strings.Join(admittedProfiles, " ")),
			Cleanup: deleteClusters(cluster),
		})
	}
	return steps
}
