//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/yaml"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/gitserver"
)

// The provenance every CI API test sends, as docs/ci-integration.md shows it.
const (
	ciCommit = "abc123def4567890abc123def4567890abc12345"
	ciRunURL = "https://github.com/myorg/my-app/actions/runs/12345"
	ciAuthor = "engineer-name"
)

// actionDir is the create-bundle GitHub Action, from test/e2e/live.
const actionDir = "../../../.github/actions/create-bundle"

// ciBody is a POST /api/v1/bundles body in the shape of the curl example in
// docs/ci-integration.md: an image Bundle of fixtures.Image:tag with
// provenance, for the Pipeline in ns. Keys in extra are added or replaced; a
// nil value removes the key.
func ciBody(t *testing.T, ns, tag string, extra map[string]interface{}) []byte {
	t.Helper()
	body := map[string]interface{}{
		"pipeline":  pipelineName,
		"namespace": ns,
		"type":      "image",
		"images":    []map[string]string{{"repository": fixtures.Image, "tag": tag}},
		"provenance": map[string]string{
			"commitSHA": ciCommit, "ciRunURL": ciRunURL, "author": ciAuthor,
		},
	}
	for k, v := range extra {
		if v == nil {
			delete(body, k)
			continue
		}
		body[k] = v
	}
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	return raw
}

// created decodes a 201 response of the Bundle API.
func created(t *testing.T, res framework.HTTPResult) (name, namespace string) {
	t.Helper()
	require.Equal(t, http.StatusCreated, res.Status, "POST /api/v1/bundles: %s", res.Body)
	assert.Equal(t, "application/json", res.Header.Get("Content-Type"))
	var out struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	}
	require.NoError(t, json.Unmarshal([]byte(res.Body), &out), "the 201 body is {name, namespace}: %s", res.Body)
	require.NotEmpty(t, out.Name)
	return out.Name, out.Namespace
}

// assertCIProvenance checks that b carries the provenance ciBody sends.
func assertCIProvenance(t *testing.T, b *v1alpha1.Bundle) {
	t.Helper()
	require.NotNil(t, b.Spec.Provenance)
	assert.Equal(t, ciCommit, b.Spec.Provenance.CommitSHA)
	assert.Equal(t, ciRunURL, b.Spec.Provenance.CIRunURL)
	assert.Equal(t, ciAuthor, b.Spec.Provenance.Author)
}

// TestCIAPI_Off checks that the Bundle API is not mounted without a token: a
// controller started without KARDINAL_BUNDLE_TOKEN (the chart sets no
// --bundle-api-token) answers POST /api/v1/bundles with the mux's 404, even
// with the right token, while its webhook listener serves. The same request
// to the chart's controller, which has the token, creates the Bundle.
//
// Covers CIAPI-OFF-01.
func TestCIAPI_Off(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := pausedApp(t, e, e.Namespace(t), "test")

	var chart appsv1.Deployment
	require.NoError(t, e.Client.Get(context.Background(),
		types.NamespacedName{Namespace: framework.ControllerNamespace, Name: framework.ControllerDeployment}, &chart))
	for _, arg := range chart.Spec.Template.Spec.Containers[0].Args {
		require.NotContains(t, arg, "--bundle-api-token", "the token must come only from KARDINAL_BUNDLE_TOKEN")
	}
	v := e.ControllerVariant(t, a.ns, nil, "KARDINAL_BUNDLE_TOKEN")

	body := ciBody(t, a.ns, fixtures.V2, nil)
	res := framework.PostBundle(t, v.URL, "", body)
	assert.Equal(t, http.StatusNotFound, res.Status)
	assert.Equal(t, "404 page not found", strings.TrimSpace(res.Body),
		"the path is not mounted (the API's own 404 says the Pipeline is not found)")
	health := framework.HTTP(t, http.MethodGet, v.URL+"/webhook/scm/health", nil, nil)
	assert.Equal(t, http.StatusOK, health.Status, "the rest of the webhook listener serves")
	assert.Empty(t, bundles(t, e, a.ns))

	name, ns := created(t, framework.PostBundle(t, framework.ControllerURL(t), "", body))
	assert.Equal(t, a.ns, ns)
	list := bundles(t, e, a.ns)
	require.Len(t, list, 1, "only the controller with a token created a Bundle")
	assert.Equal(t, name, list[0].Name)
}

// TestCIAPI_Auth checks the Bundle API's authentication: a wrong bearer
// token, no Authorization header, Basic credentials and the bare token all
// get 401 "unauthorized"; GET, PUT and DELETE with the right token get 405.
// None creates a Bundle; the same body with the right bearer token does.
//
// Covers CIAPI-AUTH-01.
func TestCIAPI_Auth(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := pausedApp(t, e, e.Namespace(t), "test")
	base, token := framework.ControllerURL(t), framework.BundleToken(t)
	body := ciBody(t, a.ns, fixtures.V2, nil)

	for _, tc := range []struct{ name, authorization string }{
		{"wrong bearer token", "Bearer not-the-" + fmt.Sprint(len(token)) + "-char-token"},
		{"no Authorization header", "-"},
		{"Basic credentials", "Basic " + base64.StdEncoding.EncodeToString([]byte("kardinal:"+token))},
		{"token without the Bearer scheme", token},
	} {
		res := framework.PostBundle(t, base, tc.authorization, body)
		assert.Equal(t, http.StatusUnauthorized, res.Status, tc.name)
		assert.Equal(t, "unauthorized", strings.TrimSpace(res.Body), tc.name)
	}
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		res := framework.HTTP(t, method, base+"/api/v1/bundles",
			map[string]string{"Authorization": "Bearer " + token, "Content-Type": "application/json"}, body)
		assert.Equal(t, http.StatusMethodNotAllowed, res.Status, method)
	}
	assert.Empty(t, bundles(t, e, a.ns), "no rejected request created a Bundle")

	created(t, framework.PostBundle(t, base, "", body))
	assert.Len(t, bundles(t, e, a.ns), 1)
}

// TestCIAPI_CreatePromotes posts the documented curl payload (with a
// namespace) to the chart's controller: it answers 201 with the Bundle's
// name and namespace, the Bundle carries the provenance, and it promotes
// like any other. The prod PR body shows the provenance and merging it
// deploys the image.
//
// Covers CIAPI-CREATE-01.
func TestCIAPI_CreatePromotes(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "prod")
	a.apply(t, a.pipeline(map[string]string{"prod": "pr-review"}))

	before := time.Now().Add(-time.Minute)
	name, ns := created(t, framework.PostBundle(t, framework.ControllerURL(t), "", ciBody(t, a.ns, fixtures.V2, nil)))
	assert.Equal(t, a.ns, ns)
	assert.True(t, strings.HasPrefix(name, pipelineName+"-"), "the name is <pipeline>-<time>-<suffix>: %s", name)

	b := getBundle(t, e, a.ns, name)
	assert.Equal(t, pipelineName, b.Labels["kardinal.io/pipeline"])
	assert.Equal(t, "image", b.Spec.Type)
	assert.Equal(t, pipelineName, b.Spec.Pipeline)
	assert.Equal(t, []v1alpha1.ImageRef{{Repository: fixtures.Image, Tag: fixtures.V2}}, b.Spec.Images)
	assertCIProvenance(t, b)
	assert.True(t, b.Spec.Provenance.Timestamp.After(before), "an empty provenance.timestamp is set to now: %s", b.Spec.Provenance.Timestamp)

	e.WaitStepState(t, a.ns, pipelineName, name, "prod", "WaitingForMerge", promoteTimeout)
	pr := e.WaitPR(t, a.repo, time.Minute, "prod promotion PR", func(pr gitserver.PR) bool { return pr.State == "open" })
	for _, evidence := range []string{fixtures.V2, ciCommit[:7], ciAuthor, ciRunURL} {
		assert.Contains(t, pr.Body, evidence, "the PR body carries the provenance the API was given")
	}
	require.NoError(t, e.Git.MergePR(context.Background(), a.repo, pr.Number))
	e.WaitStepState(t, a.ns, pipelineName, name, "prod", "Verified", promoteTimeout)
	assert.Equal(t, fixtures.Image+":"+fixtures.V2, e.DeploymentImage(t, a.ns, fixtures.Workload("prod")))
	e.WaitBundlePhase(t, a.ns, name, "Verified", time.Minute)
}

// TestCIAPI_Intent checks that intent sent to the Bundle API is copied to the
// Bundle and shapes the promotion: targetEnvironment test promotes test and
// creates no prod step; skipEnvironments [test] promotes prod directly and
// leaves test alone. No org gate applies to test, so the skip needs no
// SkipPermission gate.
//
// Covers CIAPI-INTENT-01.
func TestCIAPI_Intent(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "test", "prod")
	a.apply(t, a.pipeline(nil))
	base := framework.ControllerURL(t)
	ctx := context.Background()

	target, _ := created(t, framework.PostBundle(t, base, "",
		ciBody(t, a.ns, fixtures.V2, map[string]interface{}{"intent": map[string]string{"targetEnvironment": "test"}})))
	b := getBundle(t, e, a.ns, target)
	require.NotNil(t, b.Spec.Intent)
	assert.Equal(t, "test", b.Spec.Intent.TargetEnvironment)
	e.WaitStepState(t, a.ns, pipelineName, target, "test", "Verified", promoteTimeout)
	e.WaitBundlePhase(t, a.ns, target, "Verified", time.Minute)
	_, found, err := e.Step(ctx, a.ns, pipelineName, target, "prod")
	require.NoError(t, err)
	assert.False(t, found, "targetEnvironment test: no prod step")
	assert.Equal(t, fixtures.Image+":"+fixtures.V2, e.DeploymentImage(t, a.ns, fixtures.Workload("test")))
	assert.Equal(t, fixtures.Image+":"+fixtures.V1, e.DeploymentImage(t, a.ns, fixtures.Workload("prod")))

	skip, _ := created(t, framework.PostBundle(t, base, "",
		ciBody(t, a.ns, fixtures.V3, map[string]interface{}{"intent": map[string][]string{"skipEnvironments": {"test"}}})))
	b = getBundle(t, e, a.ns, skip)
	require.NotNil(t, b.Spec.Intent)
	assert.Equal(t, []string{"test"}, b.Spec.Intent.SkipEnvironments)
	e.WaitStepState(t, a.ns, pipelineName, skip, "prod", "Verified", promoteTimeout)
	e.WaitBundlePhase(t, a.ns, skip, "Verified", time.Minute)
	_, found, err = e.Step(ctx, a.ns, pipelineName, skip, "test")
	require.NoError(t, err)
	assert.False(t, found, "skipEnvironments [test]: no test step")
	assert.Equal(t, fixtures.Image+":"+fixtures.V3, e.DeploymentImage(t, a.ns, fixtures.Workload("prod")))
	assert.Equal(t, fixtures.Image+":"+fixtures.V2, e.DeploymentImage(t, a.ns, fixtures.Workload("test")),
		"the skipped environment keeps the version it had")
}

// TestCIAPI_KubectlBundle applies the Bundle of the kubectl example in
// docs/ci-integration.md (its GitHub expressions filled in, without the
// digest line) and checks that it promotes like a Bundle from the API: the
// PR body shows its spec.provenance and merging deploys the image.
//
// Covers CIAPI-KUBECTL-01.
func TestCIAPI_KubectlBundle(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "prod")
	a.apply(t, a.pipeline(map[string]string{"prod": "pr-review"}))

	name := fmt.Sprintf("%s-%s-%d", pipelineName, ciCommit[:8], time.Now().Unix())
	manifest := fmt.Sprintf(`apiVersion: kardinal.io/v1alpha1
kind: Bundle
metadata:
  name: %s
  labels:
    kardinal.io/pipeline: %s
spec:
  type: image
  pipeline: %s
  images:
    - repository: %s
      tag: "%s"
  provenance:
    commitSHA: "%s"
    ciRunURL: "%s"
    author: "%s"
    timestamp: "%s"
`, name, pipelineName, pipelineName, fixtures.Image, fixtures.V2, ciCommit, ciRunURL, ciAuthor,
		time.Now().UTC().Format(time.RFC3339))
	e.Kubectl(t, a.ns, manifest, "apply", "-f", "-")

	b := getBundle(t, e, a.ns, name)
	assertCIProvenance(t, b)
	e.WaitStepState(t, a.ns, pipelineName, name, "prod", "WaitingForMerge", promoteTimeout)
	pr := e.WaitPR(t, a.repo, time.Minute, "prod promotion PR", func(pr gitserver.PR) bool { return pr.State == "open" })
	for _, evidence := range []string{fixtures.V2, ciCommit[:7], ciAuthor, ciRunURL} {
		assert.Contains(t, pr.Body, evidence, "the PR body carries the applied Bundle's provenance")
	}
	require.NoError(t, e.Git.MergePR(context.Background(), a.repo, pr.Number))
	e.WaitStepState(t, a.ns, pipelineName, name, "prod", "Verified", promoteTimeout)
	assert.Equal(t, fixtures.Image+":"+fixtures.V2, e.DeploymentImage(t, a.ns, fixtures.Workload("prod")))
	e.WaitBundlePhase(t, a.ns, name, "Verified", time.Minute)
}

// TestCIAPI_Validate checks the Bundle API's 400s: an unknown field, a Bundle
// that fails validation (image without images, image with a configRef it
// would ignore, config without configRef, an unknown type), a body over 1 MiB, and a ciRunURL with user info, one that
// does not parse and a relative one. The errors about ciRunURL do not echo the
// URL or its credentials. No request creates a Bundle; the Pipeline exists, as
// a valid request shows.
//
// Covers CIAPI-VALIDATE-01.
func TestCIAPI_Validate(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := pausedApp(t, e, e.Namespace(t), "test")
	base := framework.ControllerURL(t)
	const secret = "s3cr3t-e2e-value"
	withRunURL := func(u string) map[string]interface{} {
		return map[string]interface{}{"provenance": map[string]string{"commitSHA": ciCommit, "ciRunURL": u}}
	}

	for _, tc := range []struct {
		name  string
		body  []byte
		want  string
		leaks []string
	}{
		{"unknown field", ciBody(t, a.ns, fixtures.V2, map[string]interface{}{"intnet": map[string]string{"targetEnvironment": "test"}}),
			`invalid request body: json: unknown field "intnet"`, nil},
		{"image Bundle without images", ciBody(t, a.ns, fixtures.V2, map[string]interface{}{"images": nil}),
			`type "image" requires at least one entry in images`, nil},
		{"config Bundle without configRef", ciBody(t, a.ns, fixtures.V2, map[string]interface{}{"type": "config", "images": nil}),
			`type "config" requires configRef.commitSHA`, nil},
		{"image Bundle with configRef", ciBody(t, a.ns, fixtures.V2, map[string]interface{}{
			"configRef": map[string]string{"commitSHA": ciCommit}}),
			`type "image" does not use configRef; set type config or mixed with configRef.commitSHA, or drop configRef`, nil},
		{"unknown type", ciBody(t, a.ns, fixtures.V2, map[string]interface{}{"type": "helm"}),
			`type must be one of image, config, mixed, chart (got "helm")`, nil},
		{"body over 1 MiB", ciBody(t, a.ns, fixtures.V2, map[string]interface{}{
			"provenance": map[string]string{"author": strings.Repeat("a", 1<<20)}}),
			"request body too large or unreadable", nil},
		{"ciRunURL with user info", ciBody(t, a.ns, fixtures.V2, withRunURL("https://ci-bot:"+secret+"@ci.example.com/run/1")),
			"provenance.ciRunURL must not contain user info", []string{secret, "ci.example.com", "ci-bot"}},
		{"ciRunURL that does not parse", ciBody(t, a.ns, fixtures.V2, withRunURL("https://ci-bot:"+secret+"/run/1")),
			"provenance.ciRunURL is not a valid URL", []string{secret, "ci-bot"}},
		{"relative ciRunURL", ciBody(t, a.ns, fixtures.V2, withRunURL("ci.example.com/run/1")),
			"provenance.ciRunURL must be an absolute http or https URL", []string{"ci.example.com"}},
	} {
		res := framework.PostBundle(t, base, "", tc.body)
		assert.Equal(t, http.StatusBadRequest, res.Status, tc.name)
		assert.Contains(t, res.Body, tc.want, tc.name)
		for _, s := range tc.leaks {
			assert.NotContains(t, res.Body, s, "%s: the error does not echo the URL", tc.name)
		}
	}
	assert.Empty(t, bundles(t, e, a.ns), "no rejected request created a Bundle")

	created(t, framework.PostBundle(t, base, "", ciBody(t, a.ns, fixtures.V2, nil)))
	assert.Len(t, bundles(t, e, a.ns), 1)
}

// TestCIAPI_WatchNamespace runs a controller with --watch-namespace: the
// Bundle API refuses another namespace with 403 (though that namespace has
// the Pipeline), and creates the Bundle in the watched namespace when the
// body names it or names none.
//
// Covers CIAPI-NS-01.
func TestCIAPI_WatchNamespace(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := pausedApp(t, e, e.Namespace(t), "test")
	other := pausedApp(t, e, e.Namespace(t), "test")
	v := e.ControllerVariant(t, a.ns, []string{"--watch-namespace=" + a.ns})

	res := framework.PostBundle(t, v.URL, "", ciBody(t, other.ns, fixtures.V2, nil))
	assert.Equal(t, http.StatusForbidden, res.Status)
	assert.Equal(t, fmt.Sprintf("namespace %q is not watched by this controller", other.ns), strings.TrimSpace(res.Body))
	assert.Empty(t, bundles(t, e, other.ns))

	_, ns := created(t, framework.PostBundle(t, v.URL, "", ciBody(t, a.ns, fixtures.V2, map[string]interface{}{"namespace": nil})))
	assert.Equal(t, a.ns, ns, "no namespace in the body: the watched namespace")
	_, ns = created(t, framework.PostBundle(t, v.URL, "", ciBody(t, a.ns, fixtures.V2, nil)))
	assert.Equal(t, a.ns, ns)
	assert.Len(t, bundles(t, e, a.ns), 2)
	assert.Empty(t, bundles(t, e, other.ns))
}

// bundleLimit is the Bundle API's rate limit per minute.
const bundleLimit = 60

// TestCIAPI_RateLimit checks the documented limit on a controller of its own:
// requests with the right token get 429 "rate limit exceeded" once 60 were
// counted in the current minute, including ones rejected with 400; requests
// with a wrong token are not limited (401). When the minute is over, exactly
// 60 more requests are served and the next one gets 429.
//
// Covers CIAPI-RATE-01.
func TestCIAPI_RateLimit(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := pausedApp(t, e, e.Namespace(t), "test")
	v := e.ControllerVariant(t, a.ns, nil)
	invalid := []byte(`{}`) // 400 "pipeline is required"

	limited := false
	// The window starts when the controller starts; allow for one reset
	// during the burst.
	for i := 0; i < 2*bundleLimit+1 && !limited; i++ {
		res := framework.PostBundle(t, v.URL, "", invalid)
		switch res.Status {
		case http.StatusBadRequest:
		case http.StatusTooManyRequests:
			limited = true
			assert.Equal(t, "rate limit exceeded", strings.TrimSpace(res.Body))
		default:
			t.Fatalf("request %d: HTTP %d %s", i+1, res.Status, res.Body)
		}
	}
	require.True(t, limited, "no 429 in %d requests", 2*bundleLimit+1)
	res := framework.PostBundle(t, v.URL, "Bearer wrong-token", invalid)
	assert.Equal(t, http.StatusUnauthorized, res.Status, "a wrong token is refused before the limit")

	// The first request served after the limit starts a new minute.
	framework.Eventually(t, 90*time.Second, "the rate limit window to reset", func(ctx context.Context) (bool, string) {
		res := framework.PostBundle(t, v.URL, "", invalid)
		return res.Status == http.StatusBadRequest, fmt.Sprintf("HTTP %d", res.Status)
	})
	for i := 2; i <= bundleLimit; i++ {
		res := framework.PostBundle(t, v.URL, "", invalid)
		require.Equal(t, http.StatusBadRequest, res.Status, "request %d of the new minute", i)
	}
	res = framework.PostBundle(t, v.URL, "", ciBody(t, a.ns, fixtures.V2, nil))
	assert.Equal(t, http.StatusTooManyRequests, res.Status, "request %d of the minute, although valid", bundleLimit+1)
	assert.Empty(t, bundles(t, e, a.ns))
}

// TestCIAPI_GitLabExample runs the promote job's script of the GitLab CI
// example in docs/ci-integration.md, as written (only the URL and the
// Pipeline name replaced), with GitLab's predefined variables set. The
// payload names no namespace, so it goes to a controller with
// --watch-namespace. It creates a Bundle with the image and provenance the
// variables give.
//
// Covers CIAPI-GITLAB-01.
func TestCIAPI_GitLabExample(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := pausedApp(t, e, e.Namespace(t), "test")
	v := e.ControllerVariant(t, a.ns, []string{"--watch-namespace=" + a.ns})

	script := docsYAMLBlock(t, "../../../docs/ci-integration.md", "### GitLab CI")
	var ci struct {
		Promote struct {
			Script []string `json:"script"`
		} `json:"promote"`
	}
	require.NoError(t, yaml.Unmarshal([]byte(script), &ci))
	require.Len(t, ci.Promote.Script, 1)
	run := ci.Promote.Script[0]
	require.Contains(t, run, "https://kardinal.example.com")
	require.Contains(t, run, `\"my-app\"`)
	run = strings.ReplaceAll(run, "https://kardinal.example.com", v.URL)
	run = strings.ReplaceAll(run, `\"my-app\"`, `\"`+pipelineName+`\"`)

	vars := map[string]string{
		"CI_REGISTRY_IMAGE": "registry.gitlab.example.com/group/my-app",
		"CI_COMMIT_SHA":     ciCommit,
		"IMAGE_DIGEST":      "sha256:" + strings.Repeat("ab", 32),
		"CI_PIPELINE_URL":   "https://gitlab.example.com/group/my-app/-/pipelines/4242",
		"GITLAB_USER_LOGIN": "gitlab-dev",
	}
	cmd := exec.Command("bash", "-eo", "pipefail", "-c", run)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "KARDINAL_TOKEN=" + framework.BundleToken(t)}
	for k, val := range vars {
		cmd.Env = append(cmd.Env, k+"="+val)
	}
	out, err := cmd.CombinedOutput()
	t.Logf("GitLab promote script output:\n%s", out)
	require.NoError(t, err)
	var resp struct{ Name, Namespace string }
	require.NoError(t, json.Unmarshal(out[strings.LastIndex(string(out), "{"):], &resp), "the script prints the 201 body")
	assert.Equal(t, a.ns, resp.Namespace)

	b := getBundle(t, e, a.ns, resp.Name)
	assert.Equal(t, "image", b.Spec.Type)
	assert.Equal(t, []v1alpha1.ImageRef{{Repository: vars["CI_REGISTRY_IMAGE"], Tag: ciCommit, Digest: vars["IMAGE_DIGEST"]}}, b.Spec.Images)
	require.NotNil(t, b.Spec.Provenance)
	assert.Equal(t, ciCommit, b.Spec.Provenance.CommitSHA)
	assert.Equal(t, vars["CI_PIPELINE_URL"], b.Spec.Provenance.CIRunURL)
	assert.Equal(t, vars["GITLAB_USER_LOGIN"], b.Spec.Provenance.Author)
}

// docsYAMLBlock returns the first ```yaml block after heading in a docs file.
func docsYAMLBlock(t *testing.T, path, heading string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	doc := string(raw)
	i := strings.Index(doc, "\n"+heading+"\n")
	require.GreaterOrEqual(t, i, 0, "%s has no heading %q", path, heading)
	rest := doc[i:]
	start := strings.Index(rest, "```yaml\n")
	require.GreaterOrEqual(t, start, 0, "no yaml block after %q", heading)
	rest = rest[start+len("```yaml\n"):]
	end := strings.Index(rest, "\n```")
	require.GreaterOrEqual(t, end, 0)
	return rest[:end]
}

// githubEnv is what a GitHub runner sets for the create-bundle step: the
// token secret the docs pass in env, and the run's context variables.
func githubEnv(t *testing.T) map[string]string {
	return map[string]string{
		"KARDINAL_TOKEN":    framework.BundleToken(t),
		"GITHUB_SHA":        ciCommit,
		"GITHUB_SERVER_URL": "https://github.com",
		"GITHUB_REPOSITORY": "myorg/my-app",
		"GITHUB_RUN_ID":     "987654",
		"GITHUB_ACTOR":      "octocat",
	}
}

// TestCIAPI_GitHubAction runs the create-bundle composite action (its
// action.yml and scripts, unchanged) on this host against the chart's
// controller, with the inputs of the single-image example in
// docs/ci-integration.md: an image with a tag plus the digest input. The
// Bundle carries the image with both its tag and its digest, and provenance
// from GITHUB_SHA, the run URL and GITHUB_ACTOR; the action's outputs name it
// and link the UI; the token is not in the log.
//
// Covers CIAPI-ACTION-01.
func TestCIAPI_GitHubAction(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := pausedApp(t, e, e.Namespace(t), "test")
	env := githubEnv(t)

	digest := "sha256:" + strings.Repeat("cd", 32)

	run := framework.RunAction(t, actionDir, map[string]string{
		"pipeline":     pipelineName,
		"image":        fixtures.Image + ":" + fixtures.V2,
		"digest":       digest,
		"namespace":    a.ns,
		"kardinal-url": framework.ControllerURL(t),
		"ui-url":       "https://kardinal-ui.example.com",
	}, env)
	require.NoError(t, run.Err)
	assert.NotContains(t, run.Log, env["KARDINAL_TOKEN"], "the action never prints the token")
	assert.Equal(t, a.ns, run.Outputs["bundle-namespace"])
	assert.Equal(t, "https://kardinal-ui.example.com/ui/#pipeline="+pipelineName, run.Outputs["bundle-status-url"])

	b := getBundle(t, e, a.ns, run.Outputs["bundle-name"])
	assert.Equal(t, pipelineName, b.Labels["kardinal.io/pipeline"])
	assert.Equal(t, "image", b.Spec.Type)
	assert.Equal(t, []v1alpha1.ImageRef{{Repository: fixtures.Image, Tag: fixtures.V2, Digest: digest}}, b.Spec.Images,
		"the digest input is sent with the image's tag")
	require.NotNil(t, b.Spec.Provenance)
	assert.Equal(t, ciCommit, b.Spec.Provenance.CommitSHA)
	assert.Equal(t, "https://github.com/myorg/my-app/actions/runs/987654", b.Spec.Provenance.CIRunURL)
	assert.Equal(t, "octocat", b.Spec.Provenance.Author)
}

// TestCIAPI_GitHubActionConfig runs the create-bundle action with type config
// (config-commit and config-repo) and type mixed (an image and
// config-commit): both create a Bundle with the configRef the inputs give.
// Type config without config-commit fails in the action, before any request.
//
// Covers CIAPI-ACTION-02.
func TestCIAPI_GitHubActionConfig(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := pausedApp(t, e, e.Namespace(t), "test")
	env := githubEnv(t)
	inputs := func(extra map[string]string) map[string]string {
		in := map[string]string{"pipeline": pipelineName, "namespace": a.ns, "kardinal-url": framework.ControllerURL(t)}
		for k, v := range extra {
			in[k] = v
		}
		return in
	}
	configSHA := strings.Repeat("c0ffee", 6) + "c0ff"

	run := framework.RunAction(t, actionDir, inputs(map[string]string{
		"type": "config", "config-commit": configSHA, "config-repo": a.repo.CloneURL,
	}), env)
	require.NoError(t, run.Err)
	b := getBundle(t, e, a.ns, run.Outputs["bundle-name"])
	assert.Equal(t, "config", b.Spec.Type)
	assert.Equal(t, &v1alpha1.ConfigRef{GitRepo: a.repo.CloneURL, CommitSHA: configSHA}, b.Spec.ConfigRef)
	assert.Empty(t, b.Spec.Images)

	run = framework.RunAction(t, actionDir, inputs(map[string]string{
		"type": "mixed", "image": fixtures.Image + ":" + fixtures.V2, "config-commit": configSHA,
	}), env)
	require.NoError(t, run.Err)
	b = getBundle(t, e, a.ns, run.Outputs["bundle-name"])
	assert.Equal(t, "mixed", b.Spec.Type)
	assert.Equal(t, &v1alpha1.ConfigRef{CommitSHA: configSHA}, b.Spec.ConfigRef,
		"no config-repo: gitRepo is left to default to the Pipeline's repository")
	assert.Equal(t, []v1alpha1.ImageRef{{Repository: fixtures.Image, Tag: fixtures.V2}}, b.Spec.Images)

	run = framework.RunAction(t, actionDir, inputs(map[string]string{"type": "config"}), env)
	require.Error(t, run.Err)
	assert.Contains(t, run.Log, "the config-commit input is required for type config")
	assert.Len(t, bundles(t, e, a.ns), 2)
}

// TestCIAPI_WebhookHealth checks GET /webhook/scm/health on a controller of
// its own, so the count is this test's: it reports webhookConfigured true and
// eventsProcessed 0 at start; Forgejo deliveries signed with the webhook
// secret (a hook test, a push event) are counted, a request the test signs
// adds one, and a bad signature gets 401 and adds none. A controller without
// KARDINAL_WEBHOOK_SECRET reports webhookConfigured false and refuses even
// signed events.
//
// Covers WEBHOOK-HEALTH-01.
func TestCIAPI_WebhookHealth(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ns := e.Namespace(t)
	secret := os.Getenv(framework.EnvWebhookSecret)
	require.NotEmpty(t, secret, "%s is not set", framework.EnvWebhookSecret)
	v := e.ControllerVariant(t, ns, nil)
	assert.Equal(t, framework.WebhookHealth{Status: "ok", WebhookConfigured: true, EventsProcessed: 0}, framework.WebhookHealthAt(t, v.URL))

	repo := e.Repo(t, ns, fixtures.KustomizeRepo(fixtures.App{Namespace: ns, Envs: []string{"test"}}))
	tester, ok := e.Git.(gitserver.HookTester)
	require.True(t, ok, "the git server cannot send test deliveries")
	require.NoError(t, tester.TestWebhook(context.Background(), repo, v.InClusterURL+"/webhook/scm", secret))
	// Forgejo sends the seed commit's push notification asynchronously, so the
	// new push hook may get it too, a few seconds later: the count is the test
	// delivery plus at most that push. Wait until it has not changed for 10s.
	var delivered int64
	var changed time.Time
	framework.Eventually(t, 2*time.Minute, "the Forgejo deliveries to be counted", func(context.Context) (bool, string) {
		n := framework.WebhookHealthAt(t, v.URL).EventsProcessed
		if n != delivered {
			delivered, changed = n, time.Now()
		}
		return n >= 1 && time.Since(changed) >= 10*time.Second, fmt.Sprintf("eventsProcessed %d", n)
	})
	t.Logf("Forgejo deliveries counted: %d", delivered)
	assert.LessOrEqual(t, delivered, int64(2), "the test delivery and at most the seed push")

	event := []byte(`{"action":"opened","number":1,"repository":{"full_name":"kardinal/e2e"}}`)
	bad := framework.HTTP(t, http.MethodPost, v.URL+"/webhook/scm",
		map[string]string{"Content-Type": "application/json", "X-Forgejo-Signature": framework.HMACHex("wrong-secret", event)}, event)
	assert.Equal(t, http.StatusUnauthorized, bad.Status, "a bad signature is refused")
	assert.Equal(t, delivered, framework.WebhookHealthAt(t, v.URL).EventsProcessed, "a refused request is not counted")
	good := framework.HTTP(t, http.MethodPost, v.URL+"/webhook/scm",
		map[string]string{"Content-Type": "application/json", "X-Forgejo-Signature": framework.HMACHex(secret, event)}, event)
	assert.Equal(t, http.StatusNoContent, good.Status, "a signed event that is not a merge")
	assert.Equal(t, delivered+1, framework.WebhookHealthAt(t, v.URL).EventsProcessed)

	off := e.ControllerVariant(t, ns, nil, "KARDINAL_WEBHOOK_SECRET")
	assert.Equal(t, framework.WebhookHealth{Status: "ok", WebhookConfigured: false, EventsProcessed: 0}, framework.WebhookHealthAt(t, off.URL))
	res := framework.HTTP(t, http.MethodPost, off.URL+"/webhook/scm",
		map[string]string{"Content-Type": "application/json", "X-Forgejo-Signature": framework.HMACHex(secret, event)}, event)
	assert.Equal(t, http.StatusUnauthorized, res.Status)
	assert.Equal(t, "webhook secret not configured", strings.TrimSpace(res.Body))
	assert.EqualValues(t, 0, framework.WebhookHealthAt(t, off.URL).EventsProcessed)
}
