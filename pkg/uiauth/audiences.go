// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package uiauth

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"
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
