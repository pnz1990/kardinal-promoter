// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package health

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/netip"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	ststypes "github.com/aws/aws-sdk-go-v2/service/sts/types"
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
// decides which tenants may use it, and only roles matching
// AllowedRoleARNs are assumed at all. It is safe for concurrent use.
type EKSAuth struct {
	// AllowedRoleARNs are the role ARNs a Pipeline may name: path.Match
	// patterns (arn:aws:iam::111122223333:role/kardinal-health-*). Empty
	// allows none.
	AllowedRoleARNs []string
	// Transport carries the AWS API calls (STS) through the egress guard;
	// nil is egress.NewTransport. The credential chain's own requests
	// (instance metadata, the ECS and Pod Identity agents) go directly.
	Transport http.RoundTripper
	// STSEndpoint, when set, replaces the regional STS endpoint (tests).
	STSEndpoint string
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

// Allowed reports whether role matches AllowedRoleARNs.
func (a *EKSAuth) Allowed(role string) bool {
	for _, p := range a.AllowedRoleARNs {
		if ok, err := path.Match(p, role); err == nil && ok {
			return true
		}
	}
	return false
}

// TokenSource is the EKS token source of the Pipeline namespace/pipeline for
// cluster c, cached per role, cluster and Pipeline. A role outside
// AllowedRoleARNs is refused (ErrKubeconfigNotAllowed).
func (a *EKSAuth) TokenSource(c *v1alpha1.EKSCredentials, namespace, pipeline string) (*EKSTokenSource, error) {
	if !a.Allowed(c.RoleARN) {
		return nil, fmt.Errorf("%w: health.kubeconfigSecretRef.eks.roleARN %s is not one of the controller's "+
			"--health-eks-role-arns (Helm health.eks.allowedRoleARNs)", ErrKubeconfigNotAllowed, c.RoleARN)
	}
	k := eksKey{role: c.RoleARN, region: c.Region, cluster: c.ClusterName, namespace: namespace, pipeline: pipeline}
	a.mu.Lock()
	defer a.mu.Unlock()
	if s, ok := a.sources[k]; ok {
		return s, nil
	}
	s := &EKSTokenSource{auth: a, key: k}
	if a.sources == nil {
		a.sources = map[eksKey]*EKSTokenSource{}
	}
	a.sources[k] = s
	return s, nil
}

// EKSTokenSource signs and caches the EKS tokens of one role, cluster and
// Pipeline.
type EKSTokenSource struct {
	auth *EKSAuth
	key  eksKey

	mu       sync.Mutex
	presign  *sts.PresignClient
	token    string
	signedAt time.Time
}

// Token is a token for the cluster, signed again once it is
// eksTokenRefresh old.
func (s *EKSTokenSource) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.auth.now()
	if s.token != "" && now.Sub(s.signedAt) < eksTokenRefresh {
		return s.token, nil
	}
	if s.presign == nil {
		p, err := s.auth.presignClient(ctx, s.key)
		if err != nil {
			return "", err
		}
		s.presign = p
	}
	req, err := s.presign.PresignGetCallerIdentity(ctx, &sts.GetCallerIdentityInput{}, func(po *sts.PresignOptions) {
		po.ClientOptions = append(po.ClientOptions, sts.WithAPIOptions(eksClusterHeaderMiddleware(s.key.cluster)))
	})
	if err != nil {
		return "", fmt.Errorf("sign the EKS token for %s as %s: %w", s.key.cluster, s.key.role, err)
	}
	s.token = eksTokenPrefix + base64.RawURLEncoding.EncodeToString([]byte(req.URL))
	s.signedAt = now
	return s.token, nil
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

// presignClient is an STS presign client whose credentials are k.role,
// assumed from the controller's default chain with the session tags of
// k's Pipeline.
func (a *EKSAuth) presignClient(ctx context.Context, k eksKey) (*sts.PresignClient, error) {
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
	return sts.NewPresignClient(sts.NewFromConfig(cfg, stsOpts, func(o *sts.Options) { o.Credentials = assumed })), nil
}

// sessionChars are the characters an STS role session name may hold.
var sessionChars = regexp.MustCompile(`[^A-Za-z0-9+=,.@_-]`)

// eksSessionName is the role session name of a Pipeline's health checks
// (it shows in CloudTrail): kardinal-<namespace>-<pipeline>, at most 64
// characters.
func eksSessionName(namespace, pipeline string) string {
	n := sessionChars.ReplaceAllString("kardinal-"+namespace+"-"+pipeline, "-")
	if len(n) > 64 {
		n = n[:64]
	}
	return strings.TrimRight(n, "-")
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

// eksBearer adds the EKS token of src to every request to the cluster.
type eksBearer struct {
	rt  http.RoundTripper
	src *EKSTokenSource
}

func (b eksBearer) RoundTrip(r *http.Request) (*http.Response, error) {
	tok, err := b.src.Token(r.Context())
	if err != nil {
		return nil, err
	}
	r2 := r.Clone(r.Context())
	r2.Header.Set("Authorization", "Bearer "+tok)
	return b.rt.RoundTrip(r2)
}
