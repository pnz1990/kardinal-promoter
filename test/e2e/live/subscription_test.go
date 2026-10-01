//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/gitserver"
)

// The Subscription tests watch the suite's plain-HTTP registry (anonymous
// pulls, like a public registry) and the tests' Forgejo repos. Their Bundles
// target a paused Pipeline: the kind node cannot pull from that registry, and
// the tests are about which Bundles are created, not about promotion.

// semverTags is a tagFilter for the seed's x.y.z tags.
const semverTags = `^\d+\.\d+\.\d+$`

// newSub creates Subscription ns/name for the test's Pipeline.
func newSub(t *testing.T, e *framework.Env, ns, name string, spec v1alpha1.SubscriptionSpec) {
	t.Helper()
	if spec.Pipeline == "" {
		spec.Pipeline = pipelineName
	}
	s := &v1alpha1.Subscription{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}, Spec: spec}
	require.NoError(t, e.Client.Create(context.Background(), s), "create Subscription %s", name)
}

// imageSub is an image Subscription spec.
func imageSub(registry, tagFilter, interval string) v1alpha1.SubscriptionSpec {
	return v1alpha1.SubscriptionSpec{Type: v1alpha1.SubscriptionTypeImage,
		Image: &v1alpha1.ImageSubscriptionSpec{Registry: registry, TagFilter: tagFilter, Interval: interval}}
}

// getSub reads Subscription ns/name.
func getSub(t *testing.T, e *framework.Env, ns, name string) *v1alpha1.Subscription {
	t.Helper()
	var s v1alpha1.Subscription
	require.NoError(t, e.Client.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: name}, &s))
	return &s
}

// waitSub waits until cond holds for Subscription ns/name and returns it.
func waitSub(t *testing.T, e *framework.Env, ns, name, what string, cond func(v1alpha1.SubscriptionStatus) bool) *v1alpha1.Subscription {
	t.Helper()
	var s v1alpha1.Subscription
	framework.Eventually(t, 2*time.Minute, "Subscription "+name+": "+what, func(ctx context.Context) (bool, string) {
		if err := e.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &s); err != nil {
			return false, err.Error()
		}
		return cond(s.Status), fmt.Sprintf("%+v", s.Status)
	})
	return &s
}

// waitBaseline waits for the first poll, which records digest and creates no
// Bundle.
func waitBaseline(t *testing.T, e *framework.Env, ns, name, digest string) *v1alpha1.Subscription {
	t.Helper()
	s := waitSub(t, e, ns, name, "the first poll to record "+digest, func(st v1alpha1.SubscriptionStatus) bool {
		return st.LastSeenDigest == digest
	})
	assert.Equal(t, "Watching", s.Status.Phase)
	assert.Empty(t, s.Status.LastBundleCreated, "the first poll creates no Bundle")
	assert.Empty(t, s.Status.Message)
	assert.Empty(t, subBundles(t, e, ns, name), "the first poll creates no Bundle")
	return s
}

// waitSubError waits for phase Error and returns the message.
func waitSubError(t *testing.T, e *framework.Env, ns, name string) string {
	t.Helper()
	s := waitSub(t, e, ns, name, "phase Error", func(st v1alpha1.SubscriptionStatus) bool { return st.Phase == "Error" })
	_, err := time.Parse(time.RFC3339, s.Status.LastCheckedAt)
	assert.NoError(t, err, "lastCheckedAt is RFC3339 in phase Error too")
	return s.Status.Message
}

// poke changes an annotation on the Subscription, which makes the controller
// poll at once instead of at the next interval.
func poke(t *testing.T, e *framework.Env, ns, name string) {
	t.Helper()
	s := getSub(t, e, ns, name)
	orig := s.DeepCopy()
	if s.Annotations == nil {
		s.Annotations = map[string]string{}
	}
	s.Annotations["e2e.kardinal.io/poke"] = time.Now().UTC().Format(time.RFC3339Nano)
	require.NoError(t, e.Client.Patch(context.Background(), s, client.MergeFrom(orig)))
}

// subBundles lists the Bundles Subscription name created in ns.
func subBundles(t *testing.T, e *framework.Env, ns, name string) []v1alpha1.Bundle {
	t.Helper()
	return bundles(t, e, ns, client.MatchingLabels{"kardinal.io/subscription": name})
}

// digestHex is digest without its algorithm prefix.
func digestHex(digest string) string {
	if _, h, ok := strings.Cut(digest, ":"); ok {
		return h
	}
	return digest
}

// assertImageBundle checks the Bundle an image Subscription creates for tag
// at digest, as docs/subscription.md shows it.
func assertImageBundle(t *testing.T, e *framework.Env, ns, sub, name, repository, tag, digest string) {
	t.Helper()
	b := getBundle(t, e, ns, name)
	assert.Equal(t, map[string]string{
		"kardinal.io/pipeline":      pipelineName,
		"kardinal.io/subscription":  sub,
		"kardinal.io/source-digest": digestHex(digest)[:63],
	}, b.Labels)
	assert.Equal(t, "image", b.Spec.Type)
	assert.Equal(t, pipelineName, b.Spec.Pipeline)
	assert.Equal(t, []v1alpha1.ImageRef{{Repository: repository, Tag: tag, Digest: digest}}, b.Spec.Images)
	require.NotNil(t, b.Spec.Provenance)
	assert.Equal(t, digest, b.Spec.Provenance.CommitSHA)
}

// TestSub_ImageNewTag checks an image Subscription on a registry that allows
// anonymous pulls: the first poll records the digest of the highest matching
// tag and creates nothing; a new matching tag creates Bundle
// <subscription>-<tag>-<first 8 digest chars> with the documented labels,
// image and digest.
//
// Covers SUB-OCI-01.
func TestSub_ImageNewTag(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := pausedApp(t, e, e.Namespace(t), "test")
	reg := framework.NewRegistry(t)
	repo := a.ns + "/podinfo"
	d13 := reg.Copy(t, "6.13.0", repo, "6.13.0")

	newSub(t, e, a.ns, "podinfo-image", imageSub(reg.Ref(repo), semverTags, "30s"))
	waitBaseline(t, e, a.ns, "podinfo-image", d13)

	d14 := reg.Copy(t, "6.14.0", repo, "6.14.0")
	poke(t, e, a.ns, "podinfo-image")
	want := "podinfo-image-6-14-0-" + digestHex(d14)[:8]
	s := waitSub(t, e, a.ns, "podinfo-image", "a Bundle for 6.14.0", func(st v1alpha1.SubscriptionStatus) bool {
		return st.LastBundleCreated != ""
	})
	assert.Equal(t, want, s.Status.LastBundleCreated)
	assert.Equal(t, d14, s.Status.LastSeenDigest)
	assert.Equal(t, "Watching", s.Status.Phase)
	assertImageBundle(t, e, a.ns, "podinfo-image", want, reg.Repository(repo), "6.14.0", d14)
	assert.Len(t, bundles(t, e, a.ns), 1)
}

// TestSub_ImageTagFilter checks tag selection: among the tags matching a
// regular expression the highest semantic version wins (a higher version
// outside the filter is ignored); among non-semver tags the most recently
// built image wins. No matching tag, and more than 50 non-semver matching
// tags, give phase Error with the documented messages and no Bundle.
//
// Covers SUB-OCI-02.
func TestSub_ImageTagFilter(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := pausedApp(t, e, e.Namespace(t), "test")
	reg := framework.NewRegistry(t)
	repo := a.ns + "/podinfo"
	digest := map[string]string{}
	for _, tag := range framework.SeedTags {
		digest[tag] = reg.Copy(t, tag, repo, tag)
	}
	// Non-semver tags: build-old is podinfo 6.13.0, build-new 6.15.0 (built
	// later), build-mid 6.14.0.
	reg.Copy(t, "6.13.0", repo, "build-old")
	reg.Copy(t, "6.15.0", repo, "build-new")
	reg.Copy(t, "6.14.0", repo, "build-mid")

	// 6.14.x only: 6.15.0 exists but does not match.
	newSub(t, e, a.ns, "minor", imageSub(reg.Ref(repo), `^6\.14\.\d+$`, "30s"))
	waitBaseline(t, e, a.ns, "minor", digest["6.14.0"])
	d := reg.Copy(t, "6.15.0", repo, "6.14.1") // a 6.14 patch release
	poke(t, e, a.ns, "minor")
	s := waitSub(t, e, a.ns, "minor", "a Bundle for 6.14.1", func(st v1alpha1.SubscriptionStatus) bool {
		return st.LastBundleCreated != ""
	})
	assert.Equal(t, "minor-6-14-1-"+digestHex(d)[:8], s.Status.LastBundleCreated)
	assertImageBundle(t, e, a.ns, "minor", s.Status.LastBundleCreated, reg.Repository(repo), "6.14.1", d)

	newSub(t, e, a.ns, "builds", imageSub(reg.Ref(repo), `^build-`, "30s"))
	waitBaseline(t, e, a.ns, "builds", digest["6.15.0"])

	newSub(t, e, a.ns, "none", imageSub(reg.Ref(repo), `^v99\.`, "30s"))
	assert.Equal(t, fmt.Sprintf(`OCIWatcher: no tag of %q matches tagFilter %q (%d tags listed)`, reg.Ref(repo), `^v99\.`, 7),
		waitSubError(t, e, a.ns, "none"))

	for i := 1; i <= 51; i++ {
		reg.Copy(t, framework.SeedTags[i%3], repo, fmt.Sprintf("nightly-%02d", i))
	}
	newSub(t, e, a.ns, "nightly", imageSub(reg.Ref(repo), `^nightly-`, "30s"))
	msg := waitSubError(t, e, a.ns, "nightly")
	assert.True(t, strings.HasPrefix(msg, "OCIWatcher: "+reg.Ref(repo)+": 51 tags match tagFilter and they are not all semantic versions;"), msg)
	assert.Contains(t, msg, "limited to 50 tags")

	for _, sub := range []string{"builds", "none", "nightly"} {
		assert.Empty(t, subBundles(t, e, a.ns, sub), sub)
	}
	assert.Len(t, bundles(t, e, a.ns), 1)
}

// TestSub_ImageMovingTag checks a Subscription on one moving tag (tagFilter
// ^main$): each push of main with a new digest creates a new Bundle, named
// after the tag and the new digest, so the pushes do not collide.
//
// Covers SUB-OCI-03.
func TestSub_ImageMovingTag(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := pausedApp(t, e, e.Namespace(t), "test")
	reg := framework.NewRegistry(t)
	repo := a.ns + "/podinfo"
	reg.Copy(t, "6.15.0", repo, "6.15.0") // other tags do not matter
	d13 := reg.Copy(t, "6.13.0", repo, "main")

	newSub(t, e, a.ns, "main", imageSub(reg.Ref(repo), "^main$", "30s"))
	waitBaseline(t, e, a.ns, "main", d13)

	var names []string
	for _, src := range []string{"6.14.0", "6.15.0"} {
		d := reg.Copy(t, src, repo, "main")
		poke(t, e, a.ns, "main")
		want := "main-main-" + digestHex(d)[:8]
		waitSub(t, e, a.ns, "main", "a Bundle for main at "+d, func(st v1alpha1.SubscriptionStatus) bool {
			return st.LastBundleCreated == want
		})
		assertImageBundle(t, e, a.ns, "main", want, reg.Repository(repo), "main", d)
		names = append(names, want)
	}
	var got []string
	for _, b := range subBundles(t, e, a.ns, "main") {
		got = append(got, b.Name)
	}
	assert.ElementsMatch(t, names, got, "one Bundle per digest of main")
}

// TestSub_ImagePrivate checks that a registry that requires credentials (the
// suite's second registry answers 401 with a Basic challenge) gives phase
// Error saying so, and no Bundle: only public registries are supported.
//
// Covers SUB-OCI-04.
func TestSub_ImagePrivate(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := pausedApp(t, e, e.Namespace(t), "test")
	private := strings.TrimRight(os.Getenv(framework.EnvPrivateRegistry), "/")
	require.NotEmpty(t, private, "%s is not set", framework.EnvPrivateRegistry)
	host := strings.TrimPrefix(private, "http://")

	newSub(t, e, a.ns, "private", imageSub(private+"/e2e/podinfo", "", "30s"))
	msg := waitSubError(t, e, a.ns, "private")
	assert.Contains(t, msg, fmt.Sprintf("registry %s requires credentials (HTTP 401, Basic auth); Subscriptions only poll public repositories", host))
	assert.Empty(t, getSub(t, e, a.ns, "private").Status.LastSeenDigest)
	assert.Empty(t, bundles(t, e, a.ns))
}

// TestSub_GitNewCommit checks a git Subscription with no branch set (the
// default is main): the first poll records the branch head; a new commit
// creates config Bundle <subscription>-<first 8 SHA chars> whose configRef
// is the Subscription's repoURL at that commit.
//
// Covers SUB-GIT-01.
func TestSub_GitNewCommit(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := pausedApp(t, e, e.Namespace(t), "test")
	git := committer(t, e)
	head, err := git.BranchSHA(context.Background(), a.repo)
	require.NoError(t, err)

	newSub(t, e, a.ns, "config", v1alpha1.SubscriptionSpec{Type: v1alpha1.SubscriptionTypeGit,
		Git: &v1alpha1.GitSubscriptionSpec{RepoURL: a.repo.CloneURL, Interval: "30s"}})
	assert.Equal(t, "main", getSub(t, e, a.ns, "config").Spec.Git.Branch, "branch defaults to main")
	waitBaseline(t, e, a.ns, "config", head)

	sha := commitNote(t, git, a.repo, "config change 1")
	poke(t, e, a.ns, "config")
	want := "config-" + sha[:8]
	s := waitSub(t, e, a.ns, "config", "a Bundle for "+sha, func(st v1alpha1.SubscriptionStatus) bool {
		return st.LastBundleCreated != ""
	})
	assert.Equal(t, want, s.Status.LastBundleCreated)
	assert.Equal(t, sha, s.Status.LastSeenDigest)

	b := getBundle(t, e, a.ns, want)
	assert.Equal(t, map[string]string{
		"kardinal.io/pipeline":      pipelineName,
		"kardinal.io/subscription":  "config",
		"kardinal.io/source-digest": sha,
	}, b.Labels)
	assert.Equal(t, "config", b.Spec.Type)
	assert.Equal(t, &v1alpha1.ConfigRef{GitRepo: a.repo.CloneURL, CommitSHA: sha}, b.Spec.ConfigRef)
	assert.Empty(t, b.Spec.Images)
	require.NotNil(t, b.Spec.Provenance)
	assert.Equal(t, sha, b.Spec.Provenance.CommitSHA)
	assert.Len(t, bundles(t, e, a.ns), 1)
}

// committer is the git server's commit API.
func committer(t *testing.T, e *framework.Env) gitserver.Committer {
	t.Helper()
	c, ok := e.Git.(gitserver.Committer)
	require.True(t, ok, "the %s git server cannot commit files", e.Git.Kind())
	return c
}

// commitNote commits a new file to repo's branch and returns the commit SHA.
func commitNote(t *testing.T, c gitserver.Committer, repo gitserver.Repo, message string) string {
	t.Helper()
	path := fmt.Sprintf("notes/%d.txt", time.Now().UnixNano())
	sha, err := c.CommitFiles(context.Background(), repo, message, map[string][]byte{path: []byte(message + "\n")})
	require.NoError(t, err)
	t.Logf("committed %s to %s: %s", path, repo.Name, sha)
	return sha
}

// TestSub_GitPathGlob checks that pathGlob, which is not implemented, gives
// phase Error with the documented message and creates no Bundle, even for a
// new commit inside the glob.
//
// Covers SUB-GIT-02.
func TestSub_GitPathGlob(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := pausedApp(t, e, e.Namespace(t), "test")

	newSub(t, e, a.ns, "filtered", v1alpha1.SubscriptionSpec{Type: v1alpha1.SubscriptionTypeGit,
		Git: &v1alpha1.GitSubscriptionSpec{RepoURL: a.repo.CloneURL, PathGlob: "notes/**", Interval: "30s"}})
	want := `GitWatcher: pathGlob "notes/**" is set but path filtering is not implemented; ` +
		`remove spec.git.pathGlob (every commit on the branch would create a Bundle)`
	assert.Equal(t, want, waitSubError(t, e, a.ns, "filtered"))

	commitNote(t, committer(t, e), a.repo, "inside the glob")
	poke(t, e, a.ns, "filtered")
	framework.Consistently(t, 10*time.Second, "no Bundle and phase Error", func(ctx context.Context) (bool, string) {
		s := getSub(t, e, a.ns, "filtered")
		n := len(bundles(t, e, a.ns))
		return s.Status.Phase == "Error" && s.Status.Message == want && n == 0,
			fmt.Sprintf("%+v, %d Bundles", s.Status, n)
	})
	assert.Empty(t, getSub(t, e, a.ns, "filtered").Status.LastSeenDigest)
}

// TestSub_Dedupe checks deduplication: polls that see the same digest create
// nothing, and a poll that sees a digest other than status.lastSeenDigest
// (as after a status reset) finds the existing Bundle by its
// kardinal.io/subscription and kardinal.io/source-digest labels instead of
// creating a second one. The documented kubectl query by digest finds it.
//
// Covers SUB-DEDUPE-01.
func TestSub_Dedupe(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := pausedApp(t, e, e.Namespace(t), "test")
	reg := framework.NewRegistry(t)
	repo := a.ns + "/podinfo"
	d13 := reg.Copy(t, "6.13.0", repo, "6.13.0")
	newSub(t, e, a.ns, "dedupe", imageSub(reg.Ref(repo), semverTags, "30s"))
	first := waitBaseline(t, e, a.ns, "dedupe", d13)

	// Polls of an unchanged digest create nothing.
	poke(t, e, a.ns, "dedupe")
	waitSub(t, e, a.ns, "dedupe", "a second poll", func(st v1alpha1.SubscriptionStatus) bool {
		return st.LastCheckedAt != first.Status.LastCheckedAt
	})
	assert.Empty(t, subBundles(t, e, a.ns, "dedupe"))

	d14 := reg.Copy(t, "6.14.0", repo, "6.14.0")
	poke(t, e, a.ns, "dedupe")
	name := "dedupe-6-14-0-" + digestHex(d14)[:8]
	waitSub(t, e, a.ns, "dedupe", "a Bundle for 6.14.0", func(st v1alpha1.SubscriptionStatus) bool {
		return st.LastBundleCreated == name
	})

	// Reset lastSeenDigest to the old digest: the next poll sees 6.14.0 as
	// new, and only the labels stop a second Bundle.
	s := getSub(t, e, a.ns, "dedupe")
	orig := s.DeepCopy()
	s.Status.LastSeenDigest = d13
	s.Status.LastBundleCreated = ""
	require.NoError(t, e.Client.Status().Patch(context.Background(), s, client.MergeFrom(orig)))
	poke(t, e, a.ns, "dedupe")
	waitSub(t, e, a.ns, "dedupe", "the poll after the reset", func(st v1alpha1.SubscriptionStatus) bool {
		return st.LastSeenDigest == d14
	})
	s = getSub(t, e, a.ns, "dedupe")
	assert.Equal(t, name, s.Status.LastBundleCreated, "the existing Bundle is reported")
	list := subBundles(t, e, a.ns, "dedupe")
	require.Len(t, list, 1, "the same digest never creates a second Bundle")
	assert.Equal(t, name, list[0].Name)

	out := e.Kubectl(t, a.ns, "", "get", "bundles", "-o", "name",
		"-l", "kardinal.io/source-digest="+digestHex(d14)[:63])
	assert.Equal(t, "bundle.kardinal.io/"+name, strings.TrimSpace(out))
}

// TestSub_IntervalMinimum checks that an interval under 30s is raised to
// 30s: with interval 10s the controller polls about every 30 seconds (not
// every 10s, and not at the 5m default).
//
// Covers SUB-INTERVAL-01.
func TestSub_IntervalMinimum(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := pausedApp(t, e, e.Namespace(t), "test")
	reg := framework.NewRegistry(t)
	repo := a.ns + "/podinfo"
	d13 := reg.Copy(t, "6.13.0", repo, "6.13.0")
	newSub(t, e, a.ns, "fast", imageSub(reg.Ref(repo), semverTags, "10s"))
	waitBaseline(t, e, a.ns, "fast", d13)

	// lastCheckedAt has one-second resolution; collect three polls.
	var seen []time.Time
	framework.Eventually(t, 3*time.Minute, "three polls", func(ctx context.Context) (bool, string) {
		at, err := time.Parse(time.RFC3339, getSub(t, e, a.ns, "fast").Status.LastCheckedAt)
		if err != nil {
			return false, err.Error()
		}
		if len(seen) == 0 || !at.Equal(seen[len(seen)-1]) {
			seen = append(seen, at)
		}
		return len(seen) >= 3, fmt.Sprintf("polls at %v", seen)
	})
	for i := 1; i < len(seen); i++ {
		gap := seen[i].Sub(seen[i-1])
		assert.GreaterOrEqual(t, gap, 29*time.Second, "poll %d came %s after the previous one", i+1, gap)
		assert.Less(t, gap, 50*time.Second, "poll %d came %s after the previous one", i+1, gap)
	}
}

// TestSub_Status checks the status fields and the two listings: a Watching
// Subscription with lastBundleCreated, lastSeenDigest and an RFC3339
// lastCheckedAt, and one in phase Error with its message; kubectl shows the
// printer columns NAME TYPE PIPELINE PHASE LAST-BUNDLE AGE of
// docs/subscription.md, and `kardinal get subscriptions -A` lists both with
// their namespace.
//
// Covers SUB-STATUS-01, CLI-GET-SUBS-01.
func TestSub_Status(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := pausedApp(t, e, e.Namespace(t), "test")
	reg := framework.NewRegistry(t)
	repo := a.ns + "/podinfo"
	d13 := reg.Copy(t, "6.13.0", repo, "6.13.0")
	newSub(t, e, a.ns, "ok", imageSub(reg.Ref(repo), semverTags, "30s"))
	waitBaseline(t, e, a.ns, "ok", d13)
	d14 := reg.Copy(t, "6.14.0", repo, "6.14.0")
	poke(t, e, a.ns, "ok")
	bundle := "ok-6-14-0-" + digestHex(d14)[:8]
	s := waitSub(t, e, a.ns, "ok", "a Bundle", func(st v1alpha1.SubscriptionStatus) bool { return st.LastBundleCreated == bundle })
	assert.Equal(t, "Watching", s.Status.Phase)
	assert.Equal(t, d14, s.Status.LastSeenDigest)
	assert.Empty(t, s.Status.Message)
	checked, err := time.Parse(time.RFC3339, s.Status.LastCheckedAt)
	require.NoError(t, err)
	assert.WithinDuration(t, time.Now(), checked, 2*time.Minute)

	newSub(t, e, a.ns, "broken", imageSub(reg.Ref(repo), `^v99\.`, "30s"))
	msg := waitSubError(t, e, a.ns, "broken")
	assert.Contains(t, msg, "matches tagFilter")

	out := e.Kubectl(t, a.ns, "", "get", "subscriptions")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	require.Len(t, lines, 3, out)
	assert.Equal(t, []string{"NAME", "TYPE", "PIPELINE", "PHASE", "LAST-BUNDLE", "AGE"}, strings.Fields(lines[0]))
	rows := map[string][]string{}
	for _, l := range lines[1:] {
		f := strings.Fields(l)
		rows[f[0]] = f
	}
	require.Len(t, rows["ok"], 6, out)
	assert.Equal(t, []string{"ok", "image", pipelineName, "Watching", bundle}, rows["ok"][:5])
	require.Len(t, rows["broken"], 5, "an Error Subscription has no LAST-BUNDLE: %s", out)
	assert.Equal(t, []string{"broken", "image", pipelineName, "Error"}, rows["broken"][:4])

	out = e.MustKardinal(t, "", "get", "subscriptions", "-A")
	lines = strings.Split(strings.TrimSpace(out), "\n")
	assert.Equal(t, []string{"NAMESPACE", "NAME", "TYPE", "PIPELINE", "PHASE", "LAST-CHECK", "LAST-BUNDLE", "AGE"},
		strings.Fields(lines[0]))
	mine := map[string]string{}
	for _, l := range lines[1:] {
		if f := strings.Fields(l); len(f) > 1 && f[0] == a.ns {
			mine[f[1]] = l
		}
	}
	require.Len(t, mine, 2, "both Subscriptions of %s are listed:\n%s", a.ns, out)
	assert.Regexp(t, `^`+a.ns+`\s+ok\s+image\s+`+pipelineName+`\s+Watching\s+\S+ ago\s+`+bundle+`\s+\S+$`, mine["ok"])
	assert.Regexp(t, `^`+a.ns+`\s+broken\s+image\s+`+pipelineName+`\s+Error\s+\S+ ago\s+-\s+\S+$`, mine["broken"])
}

// TestSub_Namespace checks that spec.namespace naming another namespace
// gives phase Error with the documented message, and creates no Bundle in
// either namespace, although the other namespace has the Pipeline.
//
// Covers SUB-NS-01.
func TestSub_Namespace(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := pausedApp(t, e, e.Namespace(t), "test")
	other := pausedApp(t, e, e.Namespace(t), "test")
	reg := framework.NewRegistry(t)
	repo := a.ns + "/podinfo"
	reg.Copy(t, "6.13.0", repo, "6.13.0")

	spec := imageSub(reg.Ref(repo), semverTags, "30s")
	spec.Namespace = other.ns
	newSub(t, e, a.ns, "elsewhere", spec)
	assert.Equal(t, fmt.Sprintf("spec.namespace %q is not allowed: a Subscription creates Bundles only in its own namespace %q; "+
		"remove spec.namespace or create the Subscription in %q", other.ns, a.ns, other.ns),
		waitSubError(t, e, a.ns, "elsewhere"))
	reg.Copy(t, "6.14.0", repo, "6.14.0")
	poke(t, e, a.ns, "elsewhere")
	framework.Consistently(t, 10*time.Second, "no Bundle in either namespace", func(ctx context.Context) (bool, string) {
		n, m := len(bundles(t, e, a.ns)), len(bundles(t, e, other.ns))
		return n == 0 && m == 0, fmt.Sprintf("%d and %d Bundles", n, m)
	})
	assert.Empty(t, getSub(t, e, a.ns, "elsewhere").Status.LastSeenDigest, "the source is never polled")
}

// TestSub_Egress records that the Subscription watchers have no egress guard
// (docs/guides/security.md guards only NotificationHook and MetricCheck
// requests): an image and a git Subscription on the controller's own
// loopback health port (127.0.0.1:8081) reach it and report its 404. If a
// guard is added, this test must change to expect its refusal.
//
// Covers SUB-EGRESS-01.
func TestSub_Egress(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := pausedApp(t, e, e.Namespace(t), "test")

	newSub(t, e, a.ns, "loopback-image", imageSub("http://127.0.0.1:8081/e2e/podinfo", "", "30s"))
	newSub(t, e, a.ns, "loopback-git", v1alpha1.SubscriptionSpec{Type: v1alpha1.SubscriptionTypeGit,
		Git: &v1alpha1.GitSubscriptionSpec{RepoURL: "http://127.0.0.1:8081/kardinal/podinfo.git", Interval: "30s"}})
	img := waitSubError(t, e, a.ns, "loopback-image")
	assert.Equal(t, `OCIWatcher: list tags for "http://127.0.0.1:8081/e2e/podinfo": `+
		`not found: http://127.0.0.1:8081/v2/e2e/podinfo/tags/list (HTTP 404)`, img,
		"the request reached the controller's own health server")
	gitMsg := waitSubError(t, e, a.ns, "loopback-git")
	assert.Equal(t, "GitWatcher: fetch latest SHA for http://127.0.0.1:8081/kardinal/podinfo.git@main: "+
		"repository not found: http://127.0.0.1:8081/kardinal/podinfo.git (HTTP 404)", gitMsg,
		"the request reached the controller's own health server")
	for _, m := range []string{img, gitMsg} {
		assert.NotContains(t, m, "destination address is not allowed")
	}
	assert.Empty(t, bundles(t, e, a.ns))
}

// TestSub_Examples applies the manifests in examples/subscription with the
// placeholders replaced (namespace, Pipeline, registry, repoURL) and every
// other field as written: the image example follows the moving main tag and
// the git example the main branch, and each creates a Bundle for a new
// artifact. Both poll every 5m, so the test makes them poll by annotating.
//
// Covers EX-SUB-01.
func TestSub_Examples(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := pausedApp(t, e, e.Namespace(t), "test")
	reg := framework.NewRegistry(t)
	repo := a.ns + "/podinfo"
	d13 := reg.Copy(t, "6.13.0", repo, "main")
	git := committer(t, e)
	head, err := git.BranchSHA(context.Background(), a.repo)
	require.NoError(t, err)

	img := exampleSub(t, "subscription-image.yaml")
	require.Equal(t, v1alpha1.SubscriptionTypeImage, img.Spec.Type)
	assert.Equal(t, "^main$", img.Spec.Image.TagFilter)
	assert.Equal(t, "5m", img.Spec.Image.Interval)
	img.Namespace, img.Spec.Pipeline, img.Spec.Image.Registry = a.ns, pipelineName, reg.Ref(repo)
	require.NoError(t, e.Client.Create(context.Background(), img))

	cfg := exampleSub(t, "subscription-git.yaml")
	require.Equal(t, v1alpha1.SubscriptionTypeGit, cfg.Spec.Type)
	assert.Equal(t, "main", cfg.Spec.Git.Branch)
	assert.Empty(t, cfg.Spec.Git.PathGlob)
	cfg.Namespace, cfg.Spec.Pipeline, cfg.Spec.Git.RepoURL = a.ns, pipelineName, a.repo.CloneURL
	require.NoError(t, e.Client.Create(context.Background(), cfg))

	waitBaseline(t, e, a.ns, img.Name, d13)
	waitBaseline(t, e, a.ns, cfg.Name, head)

	d14 := reg.Copy(t, "6.14.0", repo, "main")
	sha := commitNote(t, git, a.repo, "platform config change")
	poke(t, e, a.ns, img.Name)
	poke(t, e, a.ns, cfg.Name)

	imgBundle := img.Name + "-main-" + digestHex(d14)[:8]
	waitSub(t, e, a.ns, img.Name, "a Bundle for the new main", func(st v1alpha1.SubscriptionStatus) bool {
		return st.LastBundleCreated == imgBundle
	})
	assertImageBundle(t, e, a.ns, img.Name, imgBundle, reg.Repository(repo), "main", d14)

	cfgBundle := cfg.Name + "-" + sha[:8]
	waitSub(t, e, a.ns, cfg.Name, "a Bundle for the new commit", func(st v1alpha1.SubscriptionStatus) bool {
		return st.LastBundleCreated == cfgBundle
	})
	b := getBundle(t, e, a.ns, cfgBundle)
	assert.Equal(t, "config", b.Spec.Type)
	assert.Equal(t, &v1alpha1.ConfigRef{GitRepo: a.repo.CloneURL, CommitSHA: sha}, b.Spec.ConfigRef)
	assert.Len(t, bundles(t, e, a.ns), 2)
}

// exampleSub reads examples/subscription/<file>.
func exampleSub(t *testing.T, file string) *v1alpha1.Subscription {
	t.Helper()
	raw, err := os.ReadFile("../../../examples/subscription/" + file)
	require.NoError(t, err)
	var s v1alpha1.Subscription
	require.NoError(t, yaml.UnmarshalStrict(raw, &s), file)
	require.Equal(t, "default", s.Namespace, "%s: the example's namespace", file)
	return &s
}
