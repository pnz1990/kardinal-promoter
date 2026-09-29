// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
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

package main

import "net/http"

// uiContentSecurityPolicy is the policy sent with every UI server response.
//
//   - frame-ancestors 'none' (plus X-Frame-Options: DENY for older browsers)
//     stops another site from framing the UI and tricking an operator into
//     clicking Promote or Rollback.
//   - Scripts, API calls, images and styles load from the UI's own origin only.
//     style-src needs 'unsafe-inline' because components render <style> blocks
//     and web/index.html has one. No inline scripts are allowed.
//
// web/test/e2e/mock-server/server.mjs sends the same policy so the Playwright
// journeys run under it; keep the two in step.
const uiContentSecurityPolicy = "default-src 'self'; " +
	"script-src 'self'; " +
	"style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data:; " +
	"connect-src 'self'; " +
	"frame-ancestors 'none'; " +
	"base-uri 'self'; " +
	"form-action 'self'; " +
	"object-src 'none'"

// uiSecurityHeaders are set on every response of the UI server: the static
// React app at /ui/ and the JSON API at /api/v1/ui/*.
var uiSecurityHeaders = map[string]string{
	"Content-Security-Policy": uiContentSecurityPolicy,
	"X-Frame-Options":         "DENY",
	"X-Content-Type-Options":  "nosniff",
	"Referrer-Policy":         "no-referrer",
}

// withUISecurityHeaders wraps the UI server handler so that every response,
// including auth and CORS rejections, carries uiSecurityHeaders.
func withUISecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		for k, v := range uiSecurityHeaders {
			h.Set(k, v)
		}
		next.ServeHTTP(w, r)
	})
}
