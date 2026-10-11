//go:build e2e

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

package live

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
)

// TestCLI_VersionRefs checks that a version tag works where a Bundle name
// does: diff, logs --bundle, rollback --to and reject, and that a version
// two Bundles share, or none has, is refused with the candidates listed.
// Covers CLI-VERSIONREF-02.
func TestCLI_VersionRefs(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	c := e.CLI(t)
	a := newArgoApp(t, e, "test", "prod")
	a.apply(t, a.pipeline(nil))
	waitPipelineValid(t, e, a.ns, pipelineName)
	b2 := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2)
	e.WaitBundlePhase(t, a.ns, b2, "Verified", promoteTimeout)
	b3 := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V3)
	e.WaitBundlePhase(t, a.ns, b3, "Verified", promoteTimeout)

	// diff by versions is diff by names.
	assert.Equal(t, c.Must(a.ns, "diff", b2, b3), c.Must(a.ns, "diff", fixtures.V2, fixtures.V3))
	// logs --bundle by version.
	assert.Equal(t, c.Must(a.ns, "logs", pipelineName, "--bundle", b2), c.Must(a.ns, "logs", pipelineName, "--bundle", fixtures.V2))

	// rollback --to a version.
	out := c.Must(a.ns, "rollback", pipelineName, "prod", "--to", fixtures.V2)
	assert.Contains(t, out, "Rolling back "+pipelineName+" in prod from "+b3+" to "+b2)
	m := rbCreated.FindStringSubmatch(out)
	require.NotNil(t, m, out)
	rb := m[1]
	assert.Equal(t, b2, m[2])
	e.WaitBundlePhase(t, a.ns, rb, "Verified", promoteTimeout)

	// Now two Bundles ship V2 (b2 and the rollback): refused, both listed.
	r := c.Fail(a.ns, "reject", fixtures.V2, "--reason", "e2e")
	assert.Equal(t, 1, r.Code)
	assert.Contains(t, r.Stderr, `reject: version "`+fixtures.V2+`" names 2 Bundles in `+a.ns+`: `)
	assert.Contains(t, r.Stderr, framework.VersionCell(fixtures.V2, b2))
	assert.Contains(t, r.Stderr, framework.VersionCell(fixtures.V2, rb))
	assert.Contains(t, r.Stderr, "give the Bundle's name")
	// No Bundle has V1: refused, the newest listed.
	r = c.Fail(a.ns, "delete", "bundle", fixtures.V1)
	assert.Contains(t, r.Stderr, `no Bundle named "`+fixtures.V1+`", and no version "`+fixtures.V1+`" in `+a.ns+`; the newest are: `)

	// reject by a version only one Bundle ships.
	c.Must(a.ns, "reject", fixtures.V3, "--reason", "e2e")
	e.WaitBundlePhase(t, a.ns, b3, "Rejected", time.Minute)
	require.Equal(t, "Verified", getBundle(t, e, a.ns, b2).Status.Phase, "only the version named is rejected")
}
