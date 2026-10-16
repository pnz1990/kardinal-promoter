// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package uiauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	authv1 "k8s.io/api/authentication/v1"
)

// ServiceAccountTokenPath is where a Pod's ServiceAccount token is mounted.
const ServiceAccountTokenPath = "/var/run/secrets/kubernetes.io/serviceaccount/token"

// wellKnownAPIServerAudiences are the API server audiences clusters commonly
// use; they are refused even when the controller's own token cannot be read.
var wellKnownAPIServerAudiences = []string{
	"https://kubernetes.default.svc", "https://kubernetes.default.svc.cluster.local", "kubernetes.default.svc",
}

// APIServerAudiences returns the audiences of the ServiceAccount token at
// path, which the API server issued for itself, plus the well-known API
// server audiences. The token is only decoded, not verified: it is the
// controller's own.
func APIServerAudiences(path string) []string {
	out := append([]string{}, wellKnownAPIServerAudiences...)
	raw, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	parts := strings.Split(strings.TrimSpace(string(raw)), ".")
	if len(parts) != 3 {
		return out
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return out
	}
	var claims struct {
		Aud json.RawMessage `json:"aud"`
	}
	if json.Unmarshal(payload, &claims) != nil {
		return out
	}
	var one string
	var many []string
	switch {
	case json.Unmarshal(claims.Aud, &many) == nil:
		out = append(out, many...)
	case json.Unmarshal(claims.Aud, &one) == nil:
		out = append(out, one)
	}
	return out
}

// CheckAudiences refuses a configured audience that is the API server's own:
// a token for it also works against the API server. Accepting such tokens is
// the separate, explicit --tokenreview-accept-apiserver-audience.
func CheckAudiences(audiences, apiServer []string) error {
	for _, a := range audiences {
		for _, s := range apiServer {
			if a == s {
				return fmt.Errorf("--tokenreview-audiences (tokenReview.audiences) has %q, the API server's own audience: "+
					"tokens for it also work against the API server; use an audience of your own such as kardinal-promoter, "+
					"and set --tokenreview-accept-apiserver-audience (tokenReview.acceptAPIServerAudience) to accept "+
					"API server tokens explicitly", a)
			}
		}
	}
	return nil
}

// serviceAccountUserPrefix starts the username of every ServiceAccount
// token (system:serviceaccount:<namespace>:<name>).
const serviceAccountUserPrefix = "system:serviceaccount:"

// audienceRefusalPrefix starts the Error of a refusal by RequireAudience.
// Unlike the API server's own errors, such a reason names only kardinal's
// rule, so Middleware returns it in the 401 body (AudienceRefusal).
const audienceRefusalPrefix = "kardinal audience rule: "

// AudienceRefusal returns why RequireAudience refused st, and false when st
// is not such a refusal (an invalid token, an API server error).
func AudienceRefusal(st *authv1.TokenReviewStatus) (string, bool) {
	if st == nil || st.Authenticated {
		return "", false
	}
	return strings.CutPrefix(st.Error, audienceRefusalPrefix)
}

// audiencePolicy is one API's rule over a shared TokenReviewer (#1930).
type audiencePolicy struct {
	inner           TokenReviewer
	audiences       []string
	acceptAPIServer bool
}

// RequireAudience applies one API's audience rule to the reviews of inner,
// which may be shared by several APIs (one cache, one rate limit) and built
// to accept the API server's audience for any of them. A token is accepted
// when its review names one of audiences. Any other authenticated token
// (the API server's audience, or an authenticator that is not
// audience-aware, such as OIDC) is accepted only when acceptAPIServer is
// set, and never for a ServiceAccount: a ServiceAccount can always mint a
// token for kardinal's audience, so its API server token (the one every
// Pod mounts) is refused. The rule is applied after inner's cache, so a
// token one API accepts is never accepted by another through it.
func RequireAudience(inner TokenReviewer, audiences []string, acceptAPIServer bool) TokenReviewer {
	return &audiencePolicy{inner: inner, audiences: audiences, acceptAPIServer: acceptAPIServer}
}

func (p *audiencePolicy) Review(ctx context.Context, token string) (*authv1.TokenReviewStatus, error) {
	st, err := p.inner.Review(ctx, token)
	if err != nil || st == nil || !st.Authenticated || hasAudience(st.Audiences, p.audiences) {
		return st, err
	}
	want := strings.Join(p.audiences, " or ")
	if want == "" {
		want = "an audience of kardinal's"
	}
	switch {
	case !p.acceptAPIServer:
		return &authv1.TokenReviewStatus{Authenticated: false,
			Error: audienceRefusalPrefix + fmt.Sprintf("token audience is not %s; this API does not accept the API server's audience", want)}, nil
	case strings.HasPrefix(st.User.Username, serviceAccountUserPrefix):
		return &authv1.TokenReviewStatus{Authenticated: false,
			Error: audienceRefusalPrefix + fmt.Sprintf("a ServiceAccount token for the API server's audience is refused: mint one for %s "+
				"(kubectl create token <sa> --audience %s)", want, DefaultAudience)}, nil
	}
	return st, nil
}
