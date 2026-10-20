// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cmd

import (
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"regexp"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

// kardinal quickstart --local (#1785): the demo's git repository on a git
// server in the cluster, so trying kardinal needs no GitHub account, fork or
// token. quickstart installs a single-pod Forgejo in localGitNS, seeds a
// repository with kardinal-demo's environments (quickstartDemoFS) and gives
// the controller and the Pipeline tokens of its own user.
//
// Nothing is exposed: the server's Service is a ClusterIP (no NodePort, no
// hostPort) and the CLI opens no listener; kubectl port-forward, which binds
// 127.0.0.1, shows it in a browser. No credential leaves the cluster: the
// user's password is random and discarded, and each token is read from
// kubectl exec's output into memory and written into a Secret, never into a
// file, an argument or the environment.
const (
	localGitNS   = "kardinal-git"
	localGitName = "kardinal-git"
	// localGitImage is hack/e2e/versions.env's FORGEJO_IMAGE, by digest
	// (TestQuickstartLocal_Versions checks the tag).
	localGitImage = "codeberg.org/forgejo/forgejo:16.0.5-rootless@sha256:5effb7305584aca479b29fde6f9631a6dbe86ae798ae02eeea33a3666f0c0bf8"
	localGitUser  = "kardinal"
	localGitRepo  = "kardinal-demo"
	// localGitSecret is the demo Pipeline's git Secret with --local.
	localGitSecret = "git-token"
	// localGitSeed is the ConfigMap of the repository's first commit.
	localGitSeed = "kardinal-demo-seed"
	// quickstartRecord is the ConfigMap, in localGitNS, of the install
	// ID of quickstart --local (key id), which --local --delete reads.
	quickstartRecord = "kardinal-quickstart"
	// quickstartIDLabel marks what quickstart --local created or installed
	// with its install ID: the namespaces it created (kardinal-git,
	// kro-system, argocd, kardinal-system, kardinal-test-app-<env>) and
	// the Helm releases of kro and kardinal-promoter (helm --labels).
	// --local --delete removes only what carries the ID of its record.
	quickstartIDLabel = "kardinal.io/quickstart-id"
	// localTokenScopes are the scopes docs/scm-providers.md lists for Forgejo.
	localTokenScopes = "write:repository,write:issue"
)

// localGitHost is the server's in-cluster name; localGitBase its URL (the
// controller's scm.apiURL) and localRepoURL the demo repository's.
const (
	localGitHost = localGitName + "." + localGitNS + ".svc.cluster.local"
	localGitBase = "http://" + localGitHost + ":3000"
	localRepoURL = localGitBase + "/" + localGitUser + "/" + localGitRepo + ".git"
)

// quickstartDemoFS is kardinal-demo's environments/ (github.com/pnz1990/
// kardinal-demo at f5631cea), the first commit of the local repository.
//
//go:embed quickstartdemo
var quickstartDemoFS embed.FS

// localTokenRE is a Forgejo access token (40 hex digits).
var localTokenRE = regexp.MustCompile(`(?m)^[0-9a-f]{40}$`)

// localGitSeedScript seeds the server, run in its container by kubectl
// exec: the user (a random password nobody keeps), and the repository,
// created by its first push (ENABLE_PUSH_CREATE_USER) with a token that
// stays in a shell variable and reaches git through GIT_ASKPASS. It holds no
// secret, and a re-run changes nothing.
const localGitSeedScript = `set -eu
cd /
if ! forgejo admin user list 2>/dev/null | awk 'NR > 1 {print $2}' | grep -qx ` + localGitUser + `; then
  forgejo admin user create --username ` + localGitUser + ` --email ` + localGitUser + `@quickstart.invalid \
    --random-password --must-change-password=false >/dev/null
fi
repo=http://localhost:3000/` + localGitUser + `/` + localGitRepo + `.git
if git ls-remote --exit-code "$repo" main >/dev/null 2>&1; then
  echo "repository already seeded"
  exit 0
fi
work=$(mktemp -d)
ask=$(mktemp)
trap 'rm -rf "$work" "$ask"' EXIT
cp -RL /seed/environments "$work/"
cd "$work"
git init -q -b main
git add -A
git -c user.name=kardinal -c user.email=` + localGitUser + `@quickstart.invalid commit -q -m "The demo's environments (kardinal quickstart --local)"
token=$(forgejo admin user generate-access-token --username ` + localGitUser + ` --token-name "quickstart-seed-$(date +%s)" --scopes write:repository --raw)
printf '#!/bin/sh\ncase "$1" in Username*) echo ` + localGitUser + ` ;; *) echo "$KARDINAL_SEED_TOKEN" ;; esac\n' > "$ask"
chmod 700 "$ask"
KARDINAL_SEED_TOKEN=$token GIT_ASKPASS=$ask GIT_TERMINAL_PROMPT=0 git push -q "$repo" main
echo "repository seeded"
`

// localGitLabels are the labels of what quickstart --local creates.
func localGitLabels() map[string]any {
	return map[string]any{quickstartMark: quickstartMarkValue, "app.kubernetes.io/name": localGitName}
}

// localDemoFiles are the files of quickstartDemoFS, by path.
func localDemoFiles() (map[string]string, error) {
	files := map[string]string{}
	err := fs.WalkDir(quickstartDemoFS, "quickstartdemo", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := quickstartDemoFS.ReadFile(path)
		if err != nil {
			return err
		}
		files[strings.TrimPrefix(path, "quickstartdemo/")] = string(b)
		return nil
	})
	return files, err
}

// localGitObjects are the server's objects quickstart applies: the seed
// ConfigMap, the Deployment and a ClusterIP Service (never a NodePort:
// nothing outside the cluster reaches it). Its volume (localGitVolume) is
// created once, not applied.
func localGitObjects() ([]*unstructured.Unstructured, error) {
	files, err := localDemoFiles()
	if err != nil {
		return nil, fmt.Errorf("read the demo's files: %w", err)
	}
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	slices.Sort(paths)
	data := map[string]any{}
	items := make([]any, 0, len(paths))
	for _, p := range paths {
		key := strings.ReplaceAll(p, "/", "__")
		data[key] = files[p]
		items = append(items, map[string]any{"key": key, "path": p})
	}
	meta := func(name string) map[string]any {
		return map[string]any{"name": name, "namespace": localGitNS, "labels": localGitLabels()}
	}
	env := func(kv ...string) []any {
		out := make([]any, 0, len(kv)/2)
		for i := 0; i+1 < len(kv); i += 2 {
			out = append(out, map[string]any{"name": kv[i], "value": kv[i+1]})
		}
		return out
	}
	selector := map[string]any{"app.kubernetes.io/name": localGitName}
	return []*unstructured.Unstructured{
		{Object: map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "metadata": meta(localGitSeed), "data": data}},
		{Object: map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": meta(localGitName),
			"spec": map[string]any{
				"replicas": int64(1), "strategy": map[string]any{"type": "Recreate"},
				"selector": map[string]any{"matchLabels": selector},
				"template": map[string]any{
					"metadata": map[string]any{"labels": selector},
					"spec": map[string]any{
						"automountServiceAccountToken": false,
						"enableServiceLinks":           false,
						"securityContext": map[string]any{"runAsNonRoot": true, "runAsUser": int64(1000), "runAsGroup": int64(1000),
							"fsGroup": int64(1000), "seccompProfile": map[string]any{"type": "RuntimeDefault"}},
						"containers": []any{map[string]any{
							"name": "forgejo", "image": localGitImage, "imagePullPolicy": "IfNotPresent",
							"ports": []any{map[string]any{"name": "http", "containerPort": int64(3000)}},
							"env": env(
								"FORGEJO__security__INSTALL_LOCK", "true",
								"FORGEJO__database__DB_TYPE", "sqlite3",
								"FORGEJO__server__ROOT_URL", localGitBase+"/",
								"FORGEJO__server__HTTP_PORT", "3000",
								"FORGEJO__server__DISABLE_SSH", "true",
								"FORGEJO__server__OFFLINE_MODE", "true",
								"FORGEJO__service__DISABLE_REGISTRATION", "true",
								"FORGEJO__repository__DEFAULT_BRANCH", "main",
								"FORGEJO__repository__ENABLE_PUSH_CREATE_USER", "true",
								// The repository is read anonymously, in the cluster only
								// (Argo CD needs no credentials for it).
								"FORGEJO__repository__DEFAULT_PUSH_CREATE_PRIVATE", "false",
								"FORGEJO__indexer__REPO_INDEXER_ENABLED", "false",
								"FORGEJO__actions__ENABLED", "false",
								"FORGEJO__mailer__ENABLED", "false",
								"FORGEJO__cron_0X2E_update_checker__ENABLED", "false",
							),
							"securityContext": map[string]any{"allowPrivilegeEscalation": false,
								"capabilities": map[string]any{"drop": []any{"ALL"}}},
							"readinessProbe": map[string]any{"httpGet": map[string]any{"path": "/api/healthz", "port": "http"},
								"periodSeconds": int64(3)},
							"resources": map[string]any{"requests": map[string]any{"cpu": "50m", "memory": "128Mi"},
								"limits": map[string]any{"memory": "1Gi"}},
							"volumeMounts": []any{
								map[string]any{"name": "data", "mountPath": "/var/lib/gitea"},
								map[string]any{"name": "seed", "mountPath": "/seed", "readOnly": true},
							},
						}},
						"volumes": []any{
							map[string]any{"name": "data", "persistentVolumeClaim": map[string]any{"claimName": localGitName}},
							map[string]any{"name": "seed", "configMap": map[string]any{"name": localGitSeed, "items": items}},
						},
					},
				},
			}}},
		{Object: map[string]any{"apiVersion": "v1", "kind": "Service", "metadata": meta(localGitName),
			"spec": map[string]any{"type": "ClusterIP", "selector": selector,
				"ports": []any{map[string]any{"name": "http", "port": int64(3000), "targetPort": "http"}}}}},
	}, nil
}

// localGitVolume is the server's PersistentVolumeClaim. quickstart creates
// it when it is missing and never applies it: the API server fills in its
// StorageClass and volume, and a later apply would try to change them.
func localGitVolume() *corev1.PersistentVolumeClaim {
	pvc := &corev1.PersistentVolumeClaim{}
	pvc.Name, pvc.Namespace = localGitName, localGitNS
	pvc.Labels = map[string]string{quickstartMark: quickstartMarkValue, "app.kubernetes.io/name": localGitName}
	pvc.Spec.AccessModes = []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}
	pvc.Spec.Resources.Requests = corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")}
	return pvc
}

// defaultStorageClass is the name of the cluster's default StorageClass,
// "" when it has none.
func (q *quickstart) defaultStorageClass() (string, error) {
	var list storagev1.StorageClassList
	if err := q.c.List(q.ctx, &list); err != nil {
		return "", fmt.Errorf("list StorageClasses: %w", HumanError(err))
	}
	for _, sc := range list.Items {
		if sc.Annotations["storageclass.kubernetes.io/is-default-class"] == "true" {
			return sc.Name, nil
		}
	}
	return "", nil
}

// gitServer installs and seeds the in-cluster git server (--local).
func (q *quickstart) gitServer() error {
	what := "Git server " + localGitNS + "/" + localGitName
	changed := false
	if err := q.c.Create(q.ctx, localGitVolume()); err == nil {
		changed = true
	} else if !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("quickstart --local: create PersistentVolumeClaim %s/%s: %w", localGitNS, localGitName, HumanError(err))
	}
	objs, err := localGitObjects()
	if err != nil {
		return fmt.Errorf("quickstart --local: %w", err)
	}
	for _, u := range objs {
		c, err := q.apply(u)
		if err != nil {
			return fmt.Errorf("quickstart --local: %w", err)
		}
		changed = changed || c
	}
	if _, err := quickstartRun(q.ctx, "kubectl", append([]string{"-n", localGitNS, "rollout", "status", "deployment/" + localGitName,
		"--timeout=300s"}, q.kubectlFlags()...)...); err != nil {
		return fmt.Errorf("quickstart --local: the git server did not start: %w (kubectl -n %s describe pod)", err, localGitNS)
	}
	out, err := quickstartRun(q.ctx, "kubectl", q.execArgs("sh", "-c", localGitSeedScript)...)
	if err != nil {
		return fmt.Errorf("quickstart --local: seed the git server: %s", redactTokens(err.Error()))
	}
	done := "running"
	switch {
	case strings.Contains(out, "repository seeded"):
		done = "installed, repository " + localGitUser + "/" + localGitRepo + " seeded"
	case !changed:
		done = "already running"
	}
	q.say(what+" ("+localRepoURL+")", done)
	return nil
}

// execArgs are the kubectl arguments that run command in the git server's
// container. The context flags come before "--": after it, kubectl hands
// every argument to the command, and would itself fall back to the default
// kubeconfig context.
func (q *quickstart) execArgs(command ...string) []string {
	args := append(q.kubectlFlags(), "-n", localGitNS, "exec", "deployment/"+localGitName, "-c", "forgejo", "--")
	return append(args, command...)
}

// localToken is a new access token of the git server's user, with
// localTokenScopes, named for what uses it. It is read from kubectl exec's
// output and never written anywhere but the Secret the caller makes.
func (q *quickstart) localToken(name string) (string, error) {
	out, err := quickstartRun(q.ctx, "kubectl", q.execArgs("forgejo", "admin", "user", "generate-access-token", "--username", localGitUser,
		"--token-name", fmt.Sprintf("%s-%d", name, time.Now().UnixNano()), "--scopes", localTokenScopes, "--raw")...)
	if err != nil {
		return "", fmt.Errorf("create a token on the git server: %s", redactTokens(err.Error()))
	}
	m := localTokenRE.FindAllString(out, -1)
	if len(m) == 0 {
		return "", fmt.Errorf("create a token on the git server: no token in its output")
	}
	return m[len(m)-1], nil
}

// redactTokens masks anything in s that looks like a Forgejo token.
func redactTokens(s string) string {
	return regexp.MustCompile(`[0-9a-f]{40}`).ReplaceAllString(s, "xxxxx")
}

// localSecret creates Secret ns/name from a new token of the git server
// (labelled with labels and quickstartMark), or keeps the one there.
func (q *quickstart) localSecret(ns, name, tokenName, purpose string, labels map[string]string) error {
	what := "Secret " + ns + "/" + name
	var existing corev1.Secret
	switch err := q.c.Get(q.ctx, types.NamespacedName{Namespace: ns, Name: name}, &existing); {
	case err == nil:
		q.say(what, "kept")
		return nil
	case !apierrors.IsNotFound(err):
		return fmt.Errorf("quickstart --local: get %s: %w", what, HumanError(err))
	}
	token, err := q.localToken(tokenName)
	if err != nil {
		return fmt.Errorf("quickstart --local: %w", err)
	}
	s := &corev1.Secret{}
	s.Name, s.Namespace = name, ns
	s.Labels = map[string]string{quickstartMark: quickstartMarkValue, quickstartIDLabel: q.id}
	for k, v := range labels {
		s.Labels[k] = v
	}
	s.StringData = map[string]string{"token": token}
	if err := q.c.Create(q.ctx, s); err != nil {
		return fmt.Errorf("quickstart --local: create %s: %w", what, HumanError(err))
	}
	q.say(what, "created ("+purpose+")")
	return nil
}

// localHelmValues are the chart values of a kardinal-promoter installed
// for the local git server: its provider and API, and its one repository.
func localHelmValues() []string {
	return []string{"scm.provider=forgejo", "scm.apiURL=" + localGitBase,
		"scm.allowedRepositories={" + localGitHost + "/" + localGitUser + "/" + localGitRepo + "}"}
}

// localKardinalMatches reports whether the installed kardinal-promoter
// release is set up for the local git server (helm get values).
func (q *quickstart) localKardinalMatches() (bool, error) {
	out, err := quickstartRun(q.ctx, "helm", append([]string{"get", "values", "kardinal-promoter", "--namespace", quickstartControllerNS,
		"--output", "json"}, q.helmFlags()...)...)
	if err != nil {
		return false, nil
	}
	var v struct {
		SCM struct {
			Provider string `json:"provider"`
			APIURL   string `json:"apiURL"`
		} `json:"scm"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &v); err != nil {
		return false, nil
	}
	return v.SCM.Provider == "forgejo" && strings.TrimSuffix(v.SCM.APIURL, "/") == localGitBase, nil
}

// localPreflight runs every check that can refuse quickstart --local
// before anything is installed: helm can label a release (3.13 or later),
// the namespace localGitNS is quickstart's or absent (and then the cluster
// has a default StorageClass for the server's volume), and a
// kardinal-promoter already installed is one for the local git server.
func (q *quickstart) localPreflight() error {
	out, err := quickstartRun(q.ctx, "helm", "version", "--short")
	switch v := helmMinor(out); {
	case err != nil || v < 0:
		return fmt.Errorf("quickstart --local needs helm 3.13 or later (it labels the releases it installs), and helm version --short "+
			"did not say which helm this is (%q), so nothing was changed: see https://helm.sh/docs/intro/install/", strings.TrimSpace(out))
	case v < 13:
		return fmt.Errorf("quickstart --local needs helm 3.13 or later (it labels the releases it installs), and %s is installed, "+
			"so nothing was changed: see https://helm.sh/docs/intro/install/", strings.TrimSpace(out))
	}
	var ns corev1.Namespace
	switch err := q.c.Get(q.ctx, types.NamespacedName{Name: localGitNS}, &ns); {
	case err == nil:
		if ns.Labels[quickstartMark] != quickstartMarkValue || ns.Labels[quickstartIDLabel] == "" {
			return fmt.Errorf("quickstart --local: the namespace %s exists and quickstart did not create it, so nothing was changed: "+
				"delete it, or use quickstart --repo <your fork of kardinal-demo>", localGitNS)
		}
		q.id = ns.Labels[quickstartIDLabel]
	case apierrors.IsNotFound(err):
		sc, err := q.defaultStorageClass()
		if err != nil {
			return fmt.Errorf("quickstart --local: %w", err)
		}
		if sc == "" {
			return fmt.Errorf("quickstart --local: the cluster has no default StorageClass for the git server's volume, so nothing was changed: " +
				"kind, k3d, minikube and Docker Desktop have one; mark one default (kubectl patch storageclass <name> -p " +
				`'{"metadata":{"annotations":{"storageclass.kubernetes.io/is-default-class":"true"}}}')`)
		}
	default:
		return fmt.Errorf("quickstart --local: get namespace %s: %w", localGitNS, HumanError(err))
	}
	if name, ok := kardinalInstalled(q.ctx, q.c); ok {
		if match, _ := q.localKardinalMatches(); !match {
			return fmt.Errorf("quickstart --local: kardinal-promoter is already installed in %s (%s), and not for the in-cluster git server, "+
				"so nothing was changed: uninstall it (helm uninstall kardinal-promoter -n %s), or use quickstart --repo <your fork of kardinal-demo>",
				quickstartControllerNS, name, quickstartControllerNS)
		}
	}
	return nil
}

// helmMinor is the minor version helm version --short printed (v3.22.0+g…
// is 22), or -1 when it cannot be read; a helm 4 counts as new enough.
func helmMinor(out string) int {
	m := regexp.MustCompile(`v(\d+)\.(\d+)\.`).FindStringSubmatch(out)
	if m == nil {
		return -1
	}
	if m[1] != "3" {
		return 99
	}
	n := 0
	for _, c := range m[2] {
		n = n*10 + int(c-'0')
	}
	return n
}

// localBegin marks the install before its first part: the namespace
// localGitNS, labelled with a new install ID (or the ID a run before gave
// it), and the record of the ID in it.
func (q *quickstart) localBegin() error {
	if q.id == "" {
		b := make([]byte, 6)
		if _, err := rand.Read(b); err != nil {
			return fmt.Errorf("quickstart --local: an install ID: %w", err)
		}
		q.id = hex.EncodeToString(b)
	}
	if err := q.ownNamespace(localGitNS); err != nil {
		return err
	}
	var cm corev1.ConfigMap
	switch err := q.c.Get(q.ctx, types.NamespacedName{Namespace: localGitNS, Name: quickstartRecord}, &cm); {
	case apierrors.IsNotFound(err):
		cm = corev1.ConfigMap{}
		cm.Name, cm.Namespace = quickstartRecord, localGitNS
		cm.Labels = map[string]string{quickstartMark: quickstartMarkValue, quickstartIDLabel: q.id}
		cm.Data = map[string]string{"id": q.id}
		if err := q.c.Create(q.ctx, &cm); err != nil {
			return fmt.Errorf("quickstart --local: record the install ID (ConfigMap %s/%s): %w", localGitNS, quickstartRecord, HumanError(err))
		}
	case err != nil:
		return fmt.Errorf("quickstart --local: get ConfigMap %s/%s: %w", localGitNS, quickstartRecord, HumanError(err))
	}
	return nil
}

// ownNamespace creates namespace name, labelled with quickstartMark and the
// install ID, when it does not exist (with --local; without, it does
// nothing). One that exists is left as it is: it is not quickstart's.
func (q *quickstart) ownNamespace(name string) error {
	if !q.o.local {
		return nil
	}
	ns := &corev1.Namespace{}
	ns.Name = name
	ns.Labels = map[string]string{quickstartMark: quickstartMarkValue, quickstartIDLabel: q.id}
	if err := q.c.Create(q.ctx, ns); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("quickstart --local: create namespace %s: %w", name, HumanError(err))
	}
	return nil
}

// withIDLabels is meta with, under --local, the install ID as a label: the
// ApplicationSet and, through its template, its Applications carry it, and
// --local --delete knows them by it. Without --local meta is unchanged.
func (q *quickstart) withIDLabels(meta map[string]any) map[string]any {
	if q.o.local && q.id != "" {
		meta["labels"] = map[string]any{quickstartIDLabel: q.id}
	}
	return meta
}

// helmLabels are the helm flags that mark a release quickstart --local
// installs with its install ID (none without --local).
func (q *quickstart) helmLabels() []string {
	if !q.o.local {
		return nil
	}
	return []string{"--labels", quickstartIDLabel + "=" + q.id}
}
