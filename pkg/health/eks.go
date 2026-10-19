// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package health

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"net/url"
	"path"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	ststypes "github.com/aws/aws-sdk-go-v2/service/sts/types"
	"github.com/aws/smithy-go"
	"github.com/aws/smithy-go/middleware"
	smithyhttp "github.com/aws/smithy-go/transport/http"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/egress"
)

// EKS token constants, as aws-iam-authenticator and EKS use them.
const (
	// eksTokenPrefix prefixes the base64url presigned URL of an EKS token.
	eksTokenPrefix = "k8s-aws-v1."
	// eksClusterHeader carries the cluster name in the signed request, so a
	// token for one cluster is not accepted by another.
	eksClusterHeader = "x-k8s-aws-id"
	// eksTokenRefresh is how long a token is reused: EKS accepts one for 15
	// minutes after it is signed.
	eksTokenRefresh = 10 * time.Minute
	// eksClusterRefresh is how long a cluster's endpoint and CA, as
	// eks:DescribeCluster returned them, are used before they are read
	// again (a cluster created again under the same name has new ones).
	eksClusterRefresh = time.Hour
)

// Session tag keys set on every AssumeRole, for a role's trust policy to
// require (aws:RequestTag/kardinal-namespace).
const (
	EKSTagNamespace = "kardinal-namespace"
	EKSTagPipeline  = "kardinal-pipeline"
)

// EKSAuth signs EKS API tokens with the controller's own AWS identity (its
// default credential chain: IRSA, EKS Pod Identity, the instance role), as
// the role a Pipeline names (health.kubeconfigSecretRef.eks). A role is
// assumed with session tags naming the Pipeline, so a role's trust policy
// decides which tenants may use it, and only roles the operator allows
// (AllowedRoleARNs for every namespace, AllowedRoleARNsByNamespace for one)
// are assumed at all. The cluster's endpoint and CA come from
// eks:DescribeCluster, as the role: a token is sent nowhere else. It is safe
// for concurrent use.
type EKSAuth struct {
	// AllowedRoleARNs are the role ARNs a Pipeline of any namespace may
	// name: path.Match patterns (arn:aws:iam::111122223333:role/kardinal-health-*).
	AllowedRoleARNs []string
	// AllowedRoleARNsByNamespace are more patterns, each allowed for the
	// Pipelines of one namespace only. With both empty no role is allowed.
	AllowedRoleARNsByNamespace map[string][]string
	// Transport carries the AWS API calls (STS, EKS) through the egress
	// guard; nil is egress.NewTransport. The credential chain's own requests
	// (instance metadata, the ECS and Pod Identity agents) go directly.
	Transport http.RoundTripper
	// STSEndpoint and EKSEndpoint, when set, replace the regional STS and
	// EKS API endpoints (tests).
	STSEndpoint, EKSEndpoint string
	// NowFn returns the current time (tests); nil is time.Now.
	NowFn func() time.Time

	mu      sync.Mutex
	sources map[eksKey]*EKSTokenSource
}

// eksKey is one role, cluster and Pipeline: its own credentials and token.
type eksKey struct {
	role, region, cluster, namespace, pipeline string
}

func (a *EKSAuth) now() time.Time {
	if a.NowFn != nil {
		return a.NowFn()
	}
	return time.Now()
}

// Allowed reports whether a Pipeline of namespace may name role: it matches
// AllowedRoleARNs or namespace's AllowedRoleARNsByNamespace.
func (a *EKSAuth) Allowed(namespace, role string) bool {
	for _, patterns := range [][]string{a.AllowedRoleARNs, a.AllowedRoleARNsByNamespace[namespace]} {
		for _, p := range patterns {
			if ok, err := path.Match(p, role); err == nil && ok {
				return true
			}
		}
	}
	return false
}

// TokenSource is the EKS token source of the Pipeline namespace/pipeline for
// cluster c, cached per role, cluster and Pipeline. A role the Pipeline's
// namespace may not name is refused (ErrKubeconfigNotAllowed). A source
// unused for an hour is dropped, and at most remoteCacheMax are kept.
func (a *EKSAuth) TokenSource(c *v1alpha1.EKSCredentials, namespace, pipeline string) (*EKSTokenSource, error) {
	if !a.Allowed(namespace, c.RoleARN) {
		return nil, fmt.Errorf("%w: health.kubeconfigSecretRef.eks.roleARN %s is not one of the roles the controller "+
			"allows for namespace %s (--health-eks-role-arns and --health-eks-role-arns-by-namespace, Helm "+
			"health.eks.allowedRoleARNs and allowedRoleARNsByNamespace)", ErrKubeconfigNotAllowed, c.RoleARN, namespace)
	}
	k := eksKey{role: c.RoleARN, region: c.Region, cluster: c.ClusterName, namespace: namespace, pipeline: pipeline}
	now := a.now()
	a.mu.Lock()
	defer a.mu.Unlock()
	s, ok := a.sources[k]
	if !ok {
		s = &EKSTokenSource{auth: a, key: k}
		if a.sources == nil {
			a.sources = map[eksKey]*EKSTokenSource{}
		}
		a.sources[k] = s
	}
	s.touch(now)
	a.evict(now)
	return s, nil
}

// eksNamespace is a namespace name (an RFC 1123 label).
var eksNamespace = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)

// ParseEKSRoleARNsByNamespace reads namespace=pattern entries
// (--health-eks-role-arns-by-namespace) into AllowedRoleARNsByNamespace: a
// namespace name, and an IAM role ARN path.Match pattern. A namespace may
// have several entries.
func ParseEKSRoleARNsByNamespace(entries []string) (map[string][]string, error) {
	var out map[string][]string
	for _, e := range entries {
		ns, p, ok := strings.Cut(strings.TrimSpace(e), "=")
		ns, p = strings.TrimSpace(ns), strings.TrimSpace(p)
		if _, err := path.Match(p, ""); !ok || !eksNamespace.MatchString(ns) || !strings.HasPrefix(p, "arn:") || err != nil {
			return nil, fmt.Errorf("EKS role entry %q: want namespace=pattern, a namespace name and an IAM role ARN pattern", e)
		}
		if out == nil {
			out = map[string][]string{}
		}
		out[ns] = append(out[ns], p)
	}
	return out, nil
}

// Len is the number of cached token sources.
func (a *EKSAuth) Len() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.sources)
}

// evict drops the sources unused for remoteCacheTTL, then the least
// recently used ones over remoteCacheMax, as RemoteClusters does. a.mu is
// held.
func (a *EKSAuth) evict(now time.Time) {
	for k, s := range a.sources {
		if now.Sub(s.used()) > remoteCacheTTL {
			delete(a.sources, k)
		}
	}
	for len(a.sources) > remoteCacheMax {
		var oldest eksKey
		var at time.Time
		first := true
		for k, s := range a.sources {
			if u := s.used(); first || u.Before(at) {
				oldest, at, first = k, u, false
			}
		}
		delete(a.sources, oldest)
	}
}

// EKSTokenSource signs and caches the EKS tokens of one role, cluster and
// Pipeline, and reads the cluster's endpoint and CA as that role.
type EKSTokenSource struct {
	auth *EKSAuth
	key  eksKey
	// lastUsed is when the source was last asked for (Unix nanoseconds),
	// for eviction.
	lastUsed atomic.Int64

	mu       sync.Mutex
	clients  *eksClients
	token    string
	signedAt time.Time
}

// eksClients are the AWS clients of one source, with the role's
// credentials.
type eksClients struct {
	presign *sts.PresignClient
	eks     *eks.Client
}

func (s *EKSTokenSource) touch(now time.Time) { s.lastUsed.Store(now.UnixNano()) }

func (s *EKSTokenSource) used() time.Time { return time.Unix(0, s.lastUsed.Load()) }

// clientsLocked builds the source's AWS clients once. s.mu is held.
func (s *EKSTokenSource) clientsLocked(ctx context.Context) (*eksClients, error) {
	if s.clients == nil {
		c, err := s.auth.newClients(ctx, s.key)
		if err != nil {
			return nil, err
		}
		s.clients = c
	}
	return s.clients, nil
}

// Token is a token for the cluster, signed again once it is
// eksTokenRefresh old.
func (s *EKSTokenSource) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.auth.now()
	s.touch(now)
	if s.token != "" && now.Sub(s.signedAt) < eksTokenRefresh {
		return s.token, nil
	}
	c, err := s.clientsLocked(ctx)
	if err != nil {
		return "", err
	}
	req, err := c.presign.PresignGetCallerIdentity(ctx, &sts.GetCallerIdentityInput{}, func(po *sts.PresignOptions) {
		po.ClientOptions = append(po.ClientOptions, sts.WithAPIOptions(eksClusterHeaderMiddleware(s.key.cluster)))
	})
	if err != nil {
		return "", fmt.Errorf("sign the EKS token for %s as %s: %w", s.key.cluster, s.key.role, err)
	}
	s.token = eksTokenPrefix + base64.RawURLEncoding.EncodeToString([]byte(req.URL))
	s.signedAt = now
	return s.token, nil
}

// EKSCluster is where an EKS cluster's API server is, as
// eks:DescribeCluster returned it.
type EKSCluster struct {
	// Endpoint is the API server's https URL.
	Endpoint string
	// CAData is the PEM of the CA its certificate is checked against.
	CAData []byte
}

// Cluster reads the cluster's endpoint and CA with eks:DescribeCluster, as
// the role. An error is short enough for status (the AWS operation and
// error code, or ClassifyRemoteError's reason) and wraps the full one.
func (s *EKSTokenSource) Cluster(ctx context.Context) (EKSCluster, error) {
	s.mu.Lock()
	c, err := s.clientsLocked(ctx)
	s.mu.Unlock()
	if err != nil {
		return EKSCluster{}, err
	}
	fail := func(reason string, err error) (EKSCluster, error) {
		return EKSCluster{}, &eksClusterError{cluster: s.key.cluster, role: s.key.role, reason: reason, err: err}
	}
	out, err := c.eks.DescribeCluster(ctx, &eks.DescribeClusterInput{Name: aws.String(s.key.cluster)})
	if err != nil {
		return fail(awsReason(err), err)
	}
	if out.Cluster == nil || aws.ToString(out.Cluster.Endpoint) == "" {
		return fail("the cluster has no endpoint yet", errors.New("no endpoint"))
	}
	endpoint := aws.ToString(out.Cluster.Endpoint)
	if u, err := url.Parse(endpoint); err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil ||
		strings.Trim(u.Path, "/") != "" || u.RawQuery != "" {
		return fail("the cluster's endpoint is not an https URL", fmt.Errorf("endpoint %q", endpoint))
	}
	var ca []byte
	if out.Cluster.CertificateAuthority != nil {
		if ca, err = base64.StdEncoding.DecodeString(aws.ToString(out.Cluster.CertificateAuthority.Data)); err != nil {
			return fail("the cluster's certificate authority is not base64", fmt.Errorf("certificateAuthority.data: %w", err))
		}
	}
	if b, _ := pem.Decode(ca); b == nil || b.Type != "CERTIFICATE" {
		return fail("the cluster has no certificate authority", errors.New("certificateAuthority.data is not a PEM certificate"))
	}
	return EKSCluster{Endpoint: endpoint, CAData: ca}, nil
}

// eksClusterError is a failed eks:DescribeCluster: its text, written to
// status, is the cluster, role and a short reason; it wraps the AWS error.
type eksClusterError struct {
	cluster, role, reason string
	err                   error
}

func (e *eksClusterError) Error() string {
	return fmt.Sprintf("EKS cluster %s: eks:DescribeCluster as %s: %s", e.cluster, e.role, e.reason)
}

func (e *eksClusterError) Unwrap() error { return e.err }

// awsReason is a short reason for a failed AWS call: the innermost AWS
// operation and its error code (STS AssumeRole: AccessDenied, when the
// role's trust policy refused the controller), or ClassifyRemoteError's
// reason for a transport error.
func awsReason(err error) string {
	op := ""
	for e := err; e != nil; e = errors.Unwrap(e) {
		if o, ok := e.(*smithy.OperationError); ok {
			op = o.Service() + " " + o.Operation()
		}
	}
	var api smithy.APIError
	switch {
	case errors.As(err, &api) && op != "":
		return op + ": " + api.ErrorCode()
	case errors.As(err, &api):
		return api.ErrorCode()
	case op != "":
		return op + ": " + ClassifyRemoteError(err)
	}
	return ClassifyRemoteError(err)
}

// eksClusterHeaderMiddleware signs the cluster name header into the
// presigned request and sets its lifetime to 60 seconds, as EKS expects.
func eksClusterHeaderMiddleware(cluster string) func(*middleware.Stack) error {
	return func(stack *middleware.Stack) error {
		return stack.Build.Add(middleware.BuildMiddlewareFunc("KardinalEKSClusterHeader",
			func(ctx context.Context, in middleware.BuildInput, next middleware.BuildHandler) (middleware.BuildOutput, middleware.Metadata, error) {
				if req, ok := in.Request.(*smithyhttp.Request); ok {
					q := req.URL.Query()
					q.Set("X-Amz-Expires", "60")
					req.URL.RawQuery = q.Encode()
					req.Header.Set(eksClusterHeader, cluster)
				}
				return next.HandleBuild(ctx, in)
			}), middleware.After)
	}
}

// newClients are k's STS presign and EKS clients, whose credentials are
// k.role, assumed from the controller's default chain with the session
// tags of k's Pipeline. Their requests go through the egress transport, and
// redirects are not followed.
func (a *EKSAuth) newClients(ctx context.Context, k eksKey) (*eksClients, error) {
	transport := a.Transport
	if transport == nil {
		transport = egress.NewTransport(http.ProxyFromEnvironment)
	}
	direct := http.DefaultTransport.(*http.Transport).Clone()
	direct.Proxy = nil
	httpClient := &http.Client{Timeout: remoteTimeout, Transport: awsTransport{egress: transport, credentials: direct},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(k.region), config.WithHTTPClient(httpClient))
	if err != nil {
		return nil, fmt.Errorf("the controller's AWS configuration: %w", err)
	}
	stsOpts := func(o *sts.Options) {
		if a.STSEndpoint != "" {
			o.BaseEndpoint = aws.String(a.STSEndpoint)
		}
	}
	assumed := aws.NewCredentialsCache(stscreds.NewAssumeRoleProvider(sts.NewFromConfig(cfg, stsOpts), k.role,
		func(o *stscreds.AssumeRoleOptions) {
			o.RoleSessionName = eksSessionName(k.namespace, k.pipeline)
			o.Tags = []ststypes.Tag{
				{Key: aws.String(EKSTagNamespace), Value: aws.String(k.namespace)},
				{Key: aws.String(EKSTagPipeline), Value: aws.String(k.pipeline)},
			}
			o.Duration = time.Hour
		}))
	eksClient := eks.NewFromConfig(cfg, func(o *eks.Options) {
		o.Credentials = assumed
		if a.EKSEndpoint != "" {
			o.BaseEndpoint = aws.String(a.EKSEndpoint)
		}
	})
	return &eksClients{
		presign: sts.NewPresignClient(sts.NewFromConfig(cfg, stsOpts, func(o *sts.Options) { o.Credentials = assumed })),
		eks:     eksClient,
	}, nil
}

// sessionChars are the characters an STS role session name may hold.
var sessionChars = regexp.MustCompile(`[^A-Za-z0-9+=,.@_-]`)

// eksSessionName is the role session name of a Pipeline's health checks
// (it shows in CloudTrail): kardinal-<namespace>-<pipeline>, cut to fit, and
// the first 8 hex characters of the SHA-256 of <namespace>/<pipeline>, so
// two Pipelines never share one (kardinal-a-b/c and kardinal-a/b-c would);
// at most 64 characters, the STS limit.
func eksSessionName(namespace, pipeline string) string {
	sum := sha256.Sum256([]byte(namespace + "/" + pipeline))
	suffix := "-" + hex.EncodeToString(sum[:4])
	n := sessionChars.ReplaceAllString("kardinal-"+namespace+"-"+pipeline, "-")
	if limit := 64 - len(suffix); len(n) > limit {
		n = n[:limit]
	}
	return strings.TrimRight(n, "-") + suffix
}

// awsCredentialEndpoints are where the AWS SDK's default credential chain
// asks for credentials: instance metadata, the ECS task role and the EKS
// Pod Identity agent. They are link-local or metadata addresses, which the
// egress guard denies to the URLs users write; the SDK's own requests to
// them are not user input.
var awsCredentialEndpoints = map[netip.Addr]bool{
	netip.MustParseAddr("169.254.169.254"): true,
	netip.MustParseAddr("169.254.170.2"):   true,
	netip.MustParseAddr("169.254.170.23"):  true,
	netip.MustParseAddr("fd00:ec2::254"):   true,
	netip.MustParseAddr("fd00:ec2::23"):    true,
}

// awsTransport sends the credential chain's requests directly and every
// other request (STS) through the egress transport. Redirects are not
// followed, so a response cannot send a request to a credential endpoint.
type awsTransport struct{ egress, credentials http.RoundTripper }

func (t awsTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if ip, err := netip.ParseAddr(strings.Trim(req.URL.Hostname(), "[]")); err == nil && awsCredentialEndpoints[ip.Unmap()] {
		return t.credentials.RoundTrip(req)
	}
	return t.egress.RoundTrip(req)
}

// eksBearer adds the EKS token of src to every request to the cluster's
// endpoint (host, as eks:DescribeCluster returned it) and refuses any other
// request, so a redirect cannot carry the token elsewhere.
type eksBearer struct {
	rt   http.RoundTripper
	src  *EKSTokenSource
	host string
}

func (b eksBearer) RoundTrip(r *http.Request) (*http.Response, error) {
	if hostKey(r.URL) != b.host {
		return nil, fmt.Errorf("%w: the EKS token of %s is sent only to its endpoint, not to %s",
			ErrKubeconfigNotAllowed, b.src.key.cluster, r.URL.Host)
	}
	tok, err := b.src.Token(r.Context())
	if err != nil {
		return nil, err
	}
	r2 := r.Clone(r.Context())
	r2.Header.Set("Authorization", "Bearer "+tok)
	return b.rt.RoundTrip(r2)
}

// hostKey is u's host, lower case, with an https URL's :443 dropped, so
// two spellings of one server compare equal.
func hostKey(u *url.URL) string {
	h := strings.ToLower(u.Host)
	if u.Scheme == "https" {
		h = strings.TrimSuffix(h, ":443")
	}
	return h
}
