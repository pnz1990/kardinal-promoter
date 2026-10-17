// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package health_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/health"
)

// fakeSTS answers AssumeRole (recording each request's form) and
// GetCallerIdentity (the presigned request EKS sends, which must carry the
// assumed credentials and the cluster header), never real AWS.
type fakeSTS struct {
	srv     *httptest.Server
	mu      sync.Mutex
	assumes []url.Values
	ids     atomic.Int32
}

const (
	baseKey    = "AKIDBASEFAKE"
	assumedKey = "ASIAASSUMEDFAKE"
	testRole   = "arn:aws:iam::111122223333:role/kardinal-health-team-a"
)

func newFakeSTS(t *testing.T) *fakeSTS {
	t.Helper()
	f := &fakeSTS{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		switch r.Form.Get("Action") {
		case "AssumeRole":
			assert.Contains(t, r.Header.Get("Authorization"), "Credential="+baseKey, "assumed with the controller's own identity")
			f.mu.Lock()
			f.assumes = append(f.assumes, r.Form)
			f.mu.Unlock()
			_, _ = fmt.Fprintf(w, `<AssumeRoleResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><AssumeRoleResult>`+
				`<Credentials><AccessKeyId>%s</AccessKeyId><SecretAccessKey>s</SecretAccessKey><SessionToken>session-token</SessionToken>`+
				`<Expiration>%s</Expiration></Credentials><AssumedRoleUser><Arn>arn:aws:sts::111122223333:assumed-role/kardinal-health-team-a/%s</Arn>`+
				`<AssumedRoleId>AROAFAKE:%s</AssumedRoleId></AssumedRoleUser></AssumeRoleResult><ResponseMetadata><RequestId>1</RequestId></ResponseMetadata></AssumeRoleResponse>`,
				assumedKey, time.Now().Add(time.Hour).UTC().Format(time.RFC3339), r.Form.Get("RoleSessionName"), r.Form.Get("RoleSessionName"))
		case "GetCallerIdentity":
			if !strings.HasPrefix(r.Form.Get("X-Amz-Credential"), assumedKey) || r.Form.Get("X-Amz-Security-Token") != "session-token" ||
				r.Header.Get("x-k8s-aws-id") == "" {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			f.ids.Add(1)
			_, _ = fmt.Fprint(w, `<GetCallerIdentityResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><GetCallerIdentityResult>`+
				`<Arn>arn:aws:sts::111122223333:assumed-role/kardinal-health-team-a/kardinal-team-a-web</Arn><UserId>AROAFAKE:x</UserId>`+
				`<Account>111122223333</Account></GetCallerIdentityResult><ResponseMetadata><RequestId>2</RequestId></ResponseMetadata></GetCallerIdentityResponse>`)
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// fakeEKS is an EKS API server: it accepts a request whose bearer token is
// a presigned sts:GetCallerIdentity for cluster (the signed
// x-k8s-aws-id, X-Amz-Expires 60) that STS answers, as EKS does, and
// serves the Deployment prod/web.
func fakeEKS(t *testing.T, cluster string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var authed atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer k8s-aws-v1.")
		if !ok {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		raw, err := base64.RawURLEncoding.DecodeString(tok)
		require.NoError(t, err)
		u, err := url.Parse(string(raw))
		require.NoError(t, err)
		q := u.Query()
		if q.Get("Action") != "GetCallerIdentity" || q.Get("X-Amz-Expires") != "60" ||
			!strings.Contains(q.Get("X-Amz-SignedHeaders"), "x-k8s-aws-id") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		req, _ := http.NewRequest(http.MethodGet, u.String(), nil)
		req.Header.Set("x-k8s-aws-id", cluster)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		authed.Add(1)
		if r.URL.Path != "/apis/apps/v1/namespaces/prod/deployments/web" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"apiVersion": "apps/v1", "kind": "Deployment",
			"metadata": map[string]interface{}{"name": "web", "namespace": "prod", "generation": 1},
			"spec":     map[string]interface{}{"replicas": 1},
			"status": map[string]interface{}{"observedGeneration": 1, "replicas": 1, "updatedReplicas": 1, "readyReplicas": 1,
				"availableReplicas": 1, "conditions": []interface{}{map[string]interface{}{"type": "Available", "status": "True"}}}})
	}))
	t.Cleanup(srv.Close)
	return srv, &authed
}

// awsTestEnv gives the AWS SDK static base credentials and no other source
// (no profile, metadata or web identity), so nothing reaches real AWS.
func awsTestEnv(t *testing.T) {
	t.Helper()
	for _, v := range []string{"AWS_PROFILE", "AWS_DEFAULT_PROFILE", "AWS_SESSION_TOKEN", "AWS_ROLE_ARN",
		"AWS_WEB_IDENTITY_TOKEN_FILE", "AWS_CONTAINER_CREDENTIALS_FULL_URI", "AWS_CONTAINER_CREDENTIALS_RELATIVE_URI"} {
		t.Setenv(v, "")
	}
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	t.Setenv("AWS_ACCESS_KEY_ID", baseKey)
	t.Setenv("AWS_SECRET_ACCESS_KEY", "base-secret")
	t.Setenv("AWS_CONFIG_FILE", "/dev/null")
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", "/dev/null")
}

// eksKubeconfig is a kubeconfig of server and its CA, with user.
func eksKubeconfig(srv *httptest.Server, user string) []byte {
	ca := base64.StdEncoding.EncodeToString(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}))
	return []byte(fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- name: c
  cluster: {server: %q, certificate-authority-data: %s}
users:
- name: u
  user: {%s}
contexts:
- name: c
  context: {cluster: c, user: u}
current-context: c
`, srv.URL, ca, user))
}

func eksSecret(data []byte) *corev1.Secret {
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "prod-eu", Namespace: "team-a", UID: "u1", ResourceVersion: "1"},
		Data: map[string][]byte{"kubeconfig": data}}
}

// TestEKS_HealthCheckAuthenticates (#2008): with kubeconfigSecretRef.eks the
// health check reaches an EKS cluster with a token the controller signs: it
// assumes the role with its own identity and session tags naming the
// Pipeline, presigns sts:GetCallerIdentity for the cluster, and EKS (here a
// fake, with a fake STS) accepts it. The token is reused for 10 minutes,
// then signed again; the role is not assumed again while its credentials
// last.
//
// Covers HEALTH-EKS-01.
func TestEKS_HealthCheckAuthenticates(t *testing.T) {
	awsTestEnv(t)
	sts := newFakeSTS(t)
	eks, authed := fakeEKS(t, "prod-eu")
	now := time.Now()
	r := &health.RemoteClusters{Dial: loopback, EKS: &health.EKSAuth{
		AllowedRoleARNs: []string{"arn:aws:iam::111122223333:role/kardinal-health-*"},
		Transport:       http.DefaultTransport, STSEndpoint: sts.srv.URL, NowFn: func() time.Time { return now }}}
	creds := &v1alpha1.EKSCredentials{ClusterName: "prod-eu", Region: "eu-west-1", RoleARN: testRole}
	d, err := r.DetectorWith(eksSecret(eksKubeconfig(eks, "")), "", creds, "web")
	require.NoError(t, err)
	a, err := d.Select(context.Background(), "resource")
	require.NoError(t, err)
	check := func() {
		t.Helper()
		_, err := a.Check(context.Background(), health.CheckOptions{Type: "resource",
			Resource: health.ResourceConfig{Name: "web", Namespace: "prod"}})
		require.NoError(t, err)
	}
	check()
	perCheck := authed.Load()
	assert.Positive(t, perCheck, "EKS accepted the token")
	sts.mu.Lock()
	require.Len(t, sts.assumes, 1)
	f := sts.assumes[0]
	sts.mu.Unlock()
	assert.Equal(t, testRole, f.Get("RoleArn"))
	assert.Equal(t, "kardinal-team-a-web", f.Get("RoleSessionName"))
	tags := map[string]string{}
	for i := 1; f.Get(fmt.Sprintf("Tags.member.%d.Key", i)) != ""; i++ {
		tags[f.Get(fmt.Sprintf("Tags.member.%d.Key", i))] = f.Get(fmt.Sprintf("Tags.member.%d.Value", i))
	}
	assert.Equal(t, map[string]string{health.EKSTagNamespace: "team-a", health.EKSTagPipeline: "web"}, tags,
		"the session tags name the Pipeline, for the role's trust policy")

	check()
	ids := sts.ids.Load()
	now = now.Add(11 * time.Minute)
	check()
	assert.Equal(t, 3*perCheck, authed.Load(), "every request accepted, before and after the token was signed again")
	assert.GreaterOrEqual(t, sts.ids.Load(), ids)
	sts.mu.Lock()
	assert.Len(t, sts.assumes, 1, "the role's credentials are reused while they last")
	sts.mu.Unlock()
}

// TestEKS_TokenRefreshed: a token is reused for 10 minutes and signed again
// after, with a new signing time.
func TestEKS_TokenRefreshed(t *testing.T) {
	awsTestEnv(t)
	sts := newFakeSTS(t)
	now := time.Now()
	a := &health.EKSAuth{AllowedRoleARNs: []string{testRole}, Transport: http.DefaultTransport, STSEndpoint: sts.srv.URL,
		NowFn: func() time.Time { return now }}
	src, err := a.TokenSource(&v1alpha1.EKSCredentials{ClusterName: "prod-eu", Region: "eu-west-1", RoleARN: testRole}, "team-a", "web")
	require.NoError(t, err)
	first, err := src.Token(context.Background())
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(first, "k8s-aws-v1."))
	now = now.Add(9 * time.Minute)
	again, err := src.Token(context.Background())
	require.NoError(t, err)
	assert.Equal(t, first, again, "reused under 10 minutes")
	now = now.Add(2 * time.Minute)
	time.Sleep(1100 * time.Millisecond) // the signature's X-Amz-Date has a one-second resolution
	later, err := src.Token(context.Background())
	require.NoError(t, err)
	assert.NotEqual(t, first, later, "signed again after 10 minutes")
}

// TestEKS_Refused: the controller signs nothing unless the operator allows
// the role (an empty list, the default, allows none, so a Pipeline cannot
// have the controller assume another team's role), and a kubeconfig with
// credentials of its own is refused with eks. Both fail the step
// (ErrKubeconfigNotAllowed) before any AWS call.
func TestEKS_Refused(t *testing.T) {
	awsTestEnv(t)
	sts := newFakeSTS(t)
	eks, _ := fakeEKS(t, "prod-eu")
	creds := &v1alpha1.EKSCredentials{ClusterName: "prod-eu", Region: "eu-west-1", RoleARN: testRole}
	for name, tc := range map[string]struct {
		r    *health.RemoteClusters
		kc   []byte
		want string
	}{
		"feature off": {&health.RemoteClusters{Dial: loopback}, eksKubeconfig(eks, ""), "signs no EKS tokens"},
		"empty allowlist": {&health.RemoteClusters{Dial: loopback, EKS: &health.EKSAuth{STSEndpoint: sts.srv.URL}},
			eksKubeconfig(eks, ""), "is not one of the controller's --health-eks-role-arns"},
		"another team's role": {&health.RemoteClusters{Dial: loopback, EKS: &health.EKSAuth{STSEndpoint: sts.srv.URL,
			AllowedRoleARNs: []string{"arn:aws:iam::111122223333:role/kardinal-health-team-b"}}},
			eksKubeconfig(eks, ""), "is not one of"},
		"a token in the kubeconfig": {&health.RemoteClusters{Dial: loopback, EKS: &health.EKSAuth{STSEndpoint: sts.srv.URL,
			AllowedRoleARNs: []string{testRole}}}, eksKubeconfig(eks, "token: long-lived"), "remove users[].user's credentials"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := tc.r.DetectorWith(eksSecret(tc.kc), "", creds, "web")
			require.ErrorIs(t, err, health.ErrKubeconfigNotAllowed)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
	sts.mu.Lock()
	assert.Empty(t, sts.assumes, "no AWS call for a refused configuration")
	sts.mu.Unlock()
}

// loopback lets the tests reach httptest servers, which the egress guard
// refuses.
var loopback = (&net.Dialer{}).DialContext
